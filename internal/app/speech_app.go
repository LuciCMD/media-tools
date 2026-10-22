package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"media-tools/internal/ffmpeg"
	"media-tools/internal/speech"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// modelsFolder holds downloaded speech models, beside the settings file.
const modelsFolder = "models"

// noWhisperMessage explains why an ffmpeg without the whisper filter can't
// transcribe.
const noWhisperMessage = "this ffmpeg can't transcribe speech: version 8.0 or later, built with whisper, is needed"

// modelDownload is the one download that may be in progress.
type modelDownload struct {
	mu     sync.Mutex
	id     string
	cancel context.CancelFunc
}

// SpeechStatus tells the interface which speech models exist and which one
// the Transcribe tool will use.
type SpeechStatus struct {
	Models      []SpeechModel `json:"models"`
	Active      string        `json:"active"`      // path of the model in use, "" for none
	Downloading string        `json:"downloading"` // ID of the model being fetched
	Folder      string        `json:"folder"`
	Supported   bool          `json:"supported"` // ffmpeg has the whisper filter
}

// SpeechModel is one catalogue model and whether it is on disk.
type SpeechModel struct {
	speech.Model
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
}

// speechStatus reports the models and the one in use.
func (a *App) speechStatus() SpeechStatus {
	dir := a.modelsDir()
	status := SpeechStatus{Active: usableModel(a.store.Get().SpeechModel), Folder: dir, Supported: a.canTranscribe()}
	for _, m := range speech.Catalog() {
		path, ok := speech.Installed(dir, m)
		status.Models = append(status.Models, SpeechModel{Model: m, Installed: ok, Path: path})
	}
	a.download.mu.Lock()
	status.Downloading = a.download.id
	a.download.mu.Unlock()
	return status
}

// canTranscribe reports whether ffmpeg has the whisper filter. Builds before
// 8.0, and many that leave optional parts out, don't. It is checked once.
func (a *App) canTranscribe() bool {
	a.whisperOK.Do(func() {
		a.ensureProbed()
		a.whisper = ffmpeg.HasFilter(a.context(), a.bins.FFmpeg, "whisper")
	})
	return a.whisper
}

// modelsDir places downloaded models beside the settings file.
func (a *App) modelsDir() string {
	return filepath.Join(filepath.Dir(a.store.Path()), modelsFolder)
}

// usableModel returns path when it names a file that still exists, and ""
// otherwise, so a deleted model reads as "none" rather than failing later.
func usableModel(path string) string {
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}

// DownloadSpeechModel starts fetching a model in the background. Progress
// arrives as "speech:progress" events and the outcome as "speech:done". This
// is the application's only network request, and it happens only here.
func (a *App) DownloadSpeechModel(id string) error {
	m, ok := speech.Find(id)
	if !ok {
		return errors.New("that model isn't one on offer")
	}
	a.download.mu.Lock()
	if a.download.id != "" {
		a.download.mu.Unlock()
		return errors.New("a model is already downloading")
	}
	ctx, cancel := context.WithCancel(a.context())
	a.download.id, a.download.cancel = id, cancel
	a.download.mu.Unlock()

	go a.runDownload(ctx, m)
	return nil
}

// runDownload fetches m, makes it the model in use, and reports the outcome.
func (a *App) runDownload(ctx context.Context, m speech.Model) {
	path, err := speech.Download(ctx, m, a.modelsDir(), func(p speech.Progress) {
		a.emit("speech:progress", p)
	})

	a.download.mu.Lock()
	a.download.cancel()
	a.download.id, a.download.cancel = "", nil
	a.download.mu.Unlock()

	result := map[string]string{"id": m.ID}
	switch {
	case err == nil:
		a.useModel(path)
	case errors.Is(err, context.Canceled):
		result["error"] = "canceled"
	default:
		result["error"] = err.Error()
	}
	a.emit("speech:done", result)
}

// CancelSpeechDownload stops the download in progress, if any.
func (a *App) CancelSpeechDownload() {
	a.download.mu.Lock()
	defer a.download.mu.Unlock()
	if a.download.cancel != nil {
		a.download.cancel()
	}
}

// UseSpeechModel makes an installed catalogue model the one in use.
func (a *App) UseSpeechModel(id string) (SpeechStatus, error) {
	m, ok := speech.Find(id)
	if !ok {
		return a.speechStatus(), errors.New("that model isn't one on offer")
	}
	path, installed := speech.Installed(a.modelsDir(), m)
	if !installed {
		return a.speechStatus(), errors.New("download that model first")
	}
	a.useModel(path)
	return a.speechStatus(), nil
}

// ChooseSpeechModelFile lets the user point at a model they already have.
func (a *App) ChooseSpeechModelFile() (SpeechStatus, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Choose a Speech Model",
		DefaultDirectory: dialogStart(a.store.LastDir("speech.model")),
		Filters:          []runtime.FileFilter{{DisplayName: "whisper.cpp Models", Pattern: "*.bin"}},
	})
	if err != nil || path == "" {
		return a.speechStatus(), err
	}
	if err := speech.LooksLikeModel(path); err != nil {
		return a.speechStatus(), err
	}
	a.store.RememberDir("speech.model", filepath.Dir(path))
	a.useModel(path)
	return a.speechStatus(), nil
}

// RemoveSpeechModel deletes a downloaded catalogue model. Only files in the
// app's own models folder are ever deleted; a model the user chose from
// elsewhere is only forgotten.
func (a *App) RemoveSpeechModel(id string) (SpeechStatus, error) {
	m, ok := speech.Find(id)
	if !ok {
		return a.speechStatus(), errors.New("that model isn't one on offer")
	}
	path := filepath.Join(a.modelsDir(), m.File)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return a.speechStatus(), err
	}
	if claimKey(a.store.Get().SpeechModel) == claimKey(path) {
		a.useModel("")
	}
	return a.speechStatus(), nil
}

// useModel records the model the Transcribe tool will use.
func (a *App) useModel(path string) {
	cfg := a.store.Get()
	cfg.SpeechModel = path
	// The model still applies this session if only the write failed.
	_, _ = a.store.Save(cfg)
}

// emit sends an event to the frontend once the runtime is up.
func (a *App) emit(name string, data interface{}) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, name, data)
	}
}
