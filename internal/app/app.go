// Package app is the backend the page calls, bound into Wails by main
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"media-tools/internal/ffmpeg"
	"media-tools/internal/giffix"
	"media-tools/internal/jobs"
	"media-tools/internal/settings"
	"media-tools/internal/tools"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Bounds on a single user action (Power of Ten rule 2).
const (
	maxBatchFiles  = 500
	maxNameAttempt = 999
	appName        = "MediaTools"
	appVersion     = "2.2.0"
	cacheFileName  = "hardware.json"
)

// App is the object bound into the frontend as the whole backend API.
type App struct {
	ctx   context.Context
	store *settings.Store
	queue *jobs.Queue
	bins  ffmpeg.Binaries

	probed     sync.Once
	whisperOK  sync.Once
	whisper    bool // ffmpeg has the whisper filter
	revalidate bool // the hardware came from the cache and needs re-verifying

	hwMu sync.RWMutex
	hw   ffmpeg.Hardware

	// claimed holds the output paths of jobs still queued or running. Every
	// builder passes -y, so without it two inputs that resolve to the same
	// name (clip.mp4 and clip.mkv, both converted to MP4) would overwrite
	// each other, or be written by two ffmpeg processes at once.
	claimMu sync.Mutex
	claimed map[string]bool

	// retryMu makes checking for a running retry and submitting one a single step
	retryMu sync.Mutex

	notes    notifier
	download modelDownload
}

// NewApp constructs the application with its persisted settings loaded.
func NewApp() (*App, error) {
	store, err := settings.NewStore(appName)
	if err != nil {
		return nil, fmt.Errorf("app: load settings: %w", err)
	}
	return &App{store: store, claimed: make(map[string]bool, 64)}, nil
}

// startup runs once the Wails runtime is ready. It performs the hardware
// detection that the rest of the application depends on.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.ensureProbed()

	queue, err := jobs.NewQueue(a.store.Get().Concurrency, a.emitJob)
	if err != nil {
		runtime.LogError(ctx, "queue init: "+err.Error())
		return
	}
	a.queue = queue
	a.queue.Start()

	if a.revalidate {
		go a.revalidateHardware()
	}
	if a.store.Get().Notifications {
		go a.enableNotifications()
	}
	// Checked now, while the window loads, so the first look at the speech
	// status doesn't wait on it.
	go a.canTranscribe()
}

// ensureProbed performs binary discovery and encoder detection exactly once.
//
// Detection test-encodes twelve candidates and takes close to a second. It
// must not be left to finish in the background with nothing to show: the
// frontend calls GetEnvironment as soon as it loads, and would read an empty
// backend list and report that no encoder exists. So the result of the last
// run is used when it was measured against this same ffmpeg binary, and is
// re-verified in the background by revalidateHardware; otherwise detection
// runs here and callers wait for it.
func (a *App) ensureProbed() {
	a.probed.Do(func() {
		a.bins = ffmpeg.Locate()
		if hw, ok := ffmpeg.LoadDetectionCache(a.cachePath(), a.bins.FFmpeg); ok {
			a.setHardware(hw)
			a.revalidate = true
			return
		}
		a.setHardware(ffmpeg.DetectHardware(a.context()))
		a.saveHardwareCache()
	})
}

// revalidateHardware re-runs detection after a cached start and publishes the
// result if anything changed, such as a GPU removed or a driver broken.
func (a *App) revalidateHardware() {
	fresh := ffmpeg.DetectHardware(a.context())
	changed := !fresh.Same(a.hardware())
	a.setHardware(fresh)
	a.saveHardwareCache()
	if changed {
		a.emitEnvironment()
	}
}

// RedetectHardware re-runs encoder detection on request, for when a GPU or
// driver has changed while the application was open.
func (a *App) RedetectHardware() Environment {
	a.ensureProbed()
	a.setHardware(ffmpeg.DetectHardware(a.context()))
	a.saveHardwareCache()
	return a.GetEnvironment()
}

// setHardware installs a detection result and re-pins the default encoder.
func (a *App) setHardware(hw ffmpeg.Hardware) {
	a.hwMu.Lock()
	a.hw = hw
	a.hwMu.Unlock()
	a.adoptDefaultBackend(hw.Backends)
}

// hardware returns the current detection result.
func (a *App) hardware() ffmpeg.Hardware {
	a.hwMu.RLock()
	defer a.hwMu.RUnlock()
	return a.hw
}

// saveHardwareCache records the current detection result for the next start.
func (a *App) saveHardwareCache() {
	if a.bins.FFmpeg == "" {
		return
	}
	// A missing cache only costs the next start the detection time.
	_ = ffmpeg.SaveDetectionCache(a.cachePath(), a.bins.FFmpeg, a.hardware())
}

// cachePath places the detection cache beside the settings file.
func (a *App) cachePath() string {
	return filepath.Join(filepath.Dir(a.store.Path()), cacheFileName)
}

// context returns the runtime context, or a background one before startup.
func (a *App) context() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// emitEnvironment tells the frontend the detected capabilities changed.
func (a *App) emitEnvironment() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "env:update", a.GetEnvironment())
}

// adoptDefaultBackend pins a concrete encoder on first run, or when the stored
// one is no longer available. Without this the stored ID stays empty and the
// settings screen shows the first listed encoder while the encode silently
// uses a different, faster one.
func (a *App) adoptDefaultBackend(backends []ffmpeg.Backend) {
	cfg := a.store.Get()
	if backend, ok := ffmpeg.FindBackend(backends, cfg.BackendID); ok && backend.Available {
		return
	}
	cfg.BackendID = ffmpeg.DefaultBackend(backends).ID
	if _, err := a.store.Save(cfg); err != nil {
		// The chosen backend still applies this session; only persistence failed.
		return
	}
}

// Lifecycle hands main the hooks Wails calls at start and close
// A function rather than a method, since Wails offers every exported App method to the page
func Lifecycle(a *App) (startup, shutdown func(context.Context)) {
	return a.startup, a.shutdown
}

// shutdown stops outstanding work before the window closes.
func (a *App) shutdown(ctx context.Context) {
	if a.queue != nil {
		a.queue.Shutdown()
	}
}

// emitJob forwards a job state change to the frontend, and releases the
// job's output path once nothing can still be writing to it.
func (a *App) emitJob(job jobs.Job) {
	if job.State != jobs.StateQueued && job.State != jobs.StateRunning {
		a.releaseOutput(job.Output)
	}
	a.noteJob(job)
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "job:update", job)
}

// Environment describes what the host machine can do, for the settings screen.
type Environment struct {
	Version      string           `json:"version"`
	Speech       SpeechStatus     `json:"speech"`
	Backends     []ffmpeg.Backend `json:"backends"`
	Devices      []ffmpeg.Device  `json:"devices"`
	FFmpegPath   string           `json:"ffmpegPath"`
	FFprobePath  string           `json:"ffprobePath"`
	YTDLPPath    string           `json:"ytdlpPath"`
	SettingsPath string           `json:"settingsPath"`
	Warnings     []string         `json:"warnings"`
}

// GetEnvironment reports the detected capabilities and any missing dependency.
func (a *App) GetEnvironment() Environment {
	a.ensureProbed()
	hw := a.hardware()
	env := Environment{
		Version:      appVersion,
		Backends:     hw.Backends,
		Devices:      hw.Devices,
		FFmpegPath:   a.bins.FFmpeg,
		FFprobePath:  a.bins.FFprobe,
		YTDLPPath:    a.bins.YTDLP,
		SettingsPath: a.store.Path(),
		Speech:       a.speechStatus(),
		Warnings:     make([]string, 0, 3),
	}
	if a.bins.FFmpeg == "" {
		env.Warnings = append(env.Warnings,
			"ffmpeg was not found. Every audio, image and video tool needs it.")
	}
	if a.bins.FFprobe == "" {
		env.Warnings = append(env.Warnings,
			"ffprobe was not found. Progress percentages will be unavailable.")
	}
	if a.bins.YTDLP == "" {
		env.Warnings = append(env.Warnings,
			"yt-dlp was not found. The download tools need it.")
	}
	return env
}

// GetTools returns the full declarative tool registry.
func (a *App) GetTools() []tools.Tool {
	return tools.All()
}

// GetSettings returns the current persisted settings.
func (a *App) GetSettings() settings.Settings {
	return a.store.Get()
}

// SaveSettings validates and persists the supplied settings. Turning
// notifications on sets them up then and there, so the first batch after
// doesn't go unannounced.
func (a *App) SaveSettings(next settings.Settings) (settings.Settings, error) {
	before := a.store.Get()
	// The app changes these itself, while the page's copy may be from before
	// a folder was picked or a model downloaded, so they are never taken from
	// the page.
	next.SpeechModel = before.SpeechModel
	next.LastDirs = before.LastDirs
	next.RecentFiles = before.RecentFiles
	saved, err := a.store.Save(next)
	if err != nil {
		return saved, err
	}
	if saved.Notifications && !before.Notifications {
		go a.enableNotifications()
	}
	return saved, nil
}

// SelectFiles opens a multi-file picker filtered to the tool's accepted types.
func (a *App) SelectFiles(toolID string) ([]string, error) {
	tool, err := tools.Get(toolID)
	if err != nil {
		return nil, err
	}
	dialog := runtime.OpenDialogOptions{
		Title:            "Choose Files for " + tool.Name,
		DefaultDirectory: dialogStart(a.store.LastDir(toolID)),
		Filters:          buildFilters(tool.Accept),
	}

	if tool.Input == tools.InputFile {
		path, err := runtime.OpenFileDialog(a.ctx, dialog)
		if err != nil {
			return nil, err
		}
		return a.rememberSelection(toolID, single(path)), nil
	}

	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, dialog)
	if err != nil {
		return nil, err
	}
	if len(paths) > maxBatchFiles {
		return nil, fmt.Errorf("select at most %d files at once", maxBatchFiles)
	}
	return a.rememberSelection(toolID, paths), nil
}

// single wraps a possibly-empty path into a slice.
func single(path string) []string {
	if path == "" {
		return nil
	}
	return []string{path}
}

// rememberSelection records the directory of the first selected file.
func (a *App) rememberSelection(toolID string, paths []string) []string {
	if len(paths) > 0 {
		a.store.RememberSelection(toolID, filepath.Dir(paths[0]), paths[0])
	}
	return paths
}

// SelectOptionFile opens a picker for one of a tool's file options, filtered
// to the types it takes, starting in the folder of near (the file being
// worked on) when given. It returns "" when nothing is chosen.
func (a *App) SelectOptionFile(toolID, key, near string) (string, error) {
	tool, err := tools.Get(toolID)
	if err != nil {
		return "", err
	}
	var opt *tools.Option
	for i := range tool.Options {
		if tool.Options[i].Key == key && tool.Options[i].Kind == tools.OptFile {
			opt = &tool.Options[i]
		}
	}
	if opt == nil {
		return "", fmt.Errorf("%s has no file option %q", tool.Name, key)
	}

	purpose := toolID + "." + key
	start := a.store.LastDir(purpose)
	if near != "" {
		start = filepath.Dir(near)
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Choose a " + opt.Label,
		DefaultDirectory: dialogStart(start),
		Filters:          buildFilters(opt.Accept),
	})
	if err != nil || path == "" {
		return "", err
	}
	a.store.RememberDir(purpose, filepath.Dir(path))
	return path, nil
}

// SelectFolder opens a directory picker.
func (a *App) SelectFolder(purpose string) (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Choose a Folder",
		DefaultDirectory: dialogStart(a.store.LastDir(purpose)),
	})
	if err != nil {
		return "", err
	}
	if dir != "" {
		a.store.RememberDir(purpose, dir)
	}
	return dir, nil
}

// dialogStart returns dir, or the nearest folder above it that still exists,
// since Wails refuses to open a picker in a folder that's gone
func dialogStart(dir string) string {
	for dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// buildFilters converts accepted extensions into a dialog filter set.
func buildFilters(accept []string) []runtime.FileFilter {
	if len(accept) == 0 {
		return nil
	}
	patterns := make([]string, 0, len(accept))
	for _, ext := range accept {
		patterns = append(patterns, "*."+ext)
	}
	return []runtime.FileFilter{
		{DisplayName: "Supported Files", Pattern: strings.Join(patterns, ";")},
		{DisplayName: "All Files", Pattern: "*.*"},
	}
}

// Inspect returns full media metadata for one file.
func (a *App) Inspect(path string) (ffmpeg.MediaInfo, error) {
	a.ensureProbed()
	if a.bins.FFprobe == "" {
		return ffmpeg.MediaInfo{}, errors.New("ffprobe is not installed, so files cannot be inspected")
	}
	return ffmpeg.Probe(a.ctx, path)
}

// RunRequest is one submission from the frontend.
type RunRequest struct {
	ToolID    string            `json:"toolId"`
	Paths     []string          `json:"paths"`
	URL       string            `json:"url"`
	Options   map[string]string `json:"options"`
	OutputDir string            `json:"outputDir"`
}

// Run validates a request and enqueues one job per input.
func (a *App) Run(req RunRequest) ([]string, error) {
	tool, err := tools.Get(req.ToolID)
	if err != nil {
		return nil, err
	}
	opts, err := tool.Validate(req.Options)
	if err != nil {
		return nil, err
	}
	if err := a.checkDependencies(tool); err != nil {
		return nil, err
	}
	if err := checkFileOptions(tool, opts); err != nil {
		return nil, err
	}

	if tool.Input == tools.InputURL {
		id, err := a.submitDownload(tool, req, opts)
		if err != nil {
			return nil, err
		}
		return []string{id}, nil
	}
	return a.submitConversions(tool, req, opts)
}

// checkDependencies rejects a run whose required binary is missing.
func (a *App) checkDependencies(tool tools.Tool) error {
	a.ensureProbed()
	if tool.Input == tools.InputURL {
		return a.bins.Verify(false, true)
	}
	if err := a.bins.Verify(true, false); err != nil {
		return err
	}
	if tool.Requires == tools.RequireSpeechModel && !a.canTranscribe() {
		return errors.New(noWhisperMessage)
	}
	return nil
}

// submitDownload enqueues a single yt-dlp job.
func (a *App) submitDownload(tool tools.Tool, req RunRequest, opts map[string]string) (string, error) {
	if err := tools.ValidateURL(req.URL); err != nil {
		return "", err
	}
	outDir := req.OutputDir
	if strings.TrimSpace(outDir) == "" {
		return "", errors.New("choose a folder to download into")
	}
	if err := ensureDir(outDir); err != nil {
		return "", err
	}

	in := tools.Inputs{Path: req.URL, Output: outDir, Options: opts}
	enc := a.encodeProfile(tool)
	plan, err := tool.Build(in, enc)
	// A download that's converted once it lands, such as a GIF, lands in a folder of its own
	if errors.Is(err, tools.ErrNeedsWorkDir) {
		if in.WorkDir, err = os.MkdirTemp(outDir, ".mediatools-"); err != nil {
			return "", fmt.Errorf("couldn't make a work folder in %s: %w", outDir, err)
		}
		if plan, err = tool.Build(in, enc); err != nil {
			_ = os.Remove(in.WorkDir)
			return "", err
		}
	}
	if err != nil {
		return "", err
	}
	id, err := a.queue.Submit(downloadSpec(tool, req.URL, outDir, in.WorkDir, plan, a.bins))
	if err != nil && in.WorkDir != "" {
		_ = os.RemoveAll(in.WorkDir)
	}
	return id, err
}

// downloadSpec builds the job for a download, which ffmpeg finishes from
// work when the plan converts what lands there
func downloadSpec(tool tools.Tool, link, outDir, work string, plan tools.Plan, bins ffmpeg.Binaries) jobs.Spec {
	spec := jobs.Spec{
		Tool:   tool.ID,
		Label:  tool.Name,
		Input:  link,
		Output: outDir,
		Binary: bins.YTDLP,
		Args:   append(ffmpeg.DownloadProgressArgs(), plan.Args...),
	}
	if plan.Finish == nil {
		return spec
	}
	spec.WorkDir = work
	spec.ThenCount = plan.FinishCount
	spec.Then = func(ctx context.Context) (jobs.Stage, error) {
		return finishStage(ctx, plan, work, bins.FFmpeg)
	}
	return spec
}

// finishStage builds the commands that turn the one file a download left in
// work into its result, measuring the file so the bar can show a percentage
func finishStage(ctx context.Context, plan tools.Plan, work, ffmpegBin string) (jobs.Stage, error) {
	src, err := ffmpeg.FindDownload(work)
	if err != nil {
		return jobs.Stage{}, err
	}
	// An unreadable file costs only the percentage and the colour check
	var totals ffmpeg.Totals
	var color tools.SourceColor
	if info, err := ffmpeg.Probe(ctx, src); err == nil {
		totals = ffmpeg.Totals{DurationS: info.DurationS, Frames: info.TotalFrame}
		color = videoColor(info)
	}
	p, err := plan.Finish(src, color)
	if err != nil {
		return jobs.Stage{}, err
	}
	return jobs.Stage{Binary: ffmpegBin, Totals: totals, Commands: commandsOf(p)}, nil
}

// submitConversions enqueues one job per selected input file.
func (a *App) submitConversions(tool tools.Tool, req RunRequest, opts map[string]string) ([]string, error) {
	if len(req.Paths) == 0 {
		return nil, errors.New("select at least one file first")
	}
	if len(req.Paths) > maxBatchFiles {
		return nil, fmt.Errorf("select at most %d files at once", maxBatchFiles)
	}

	enc := a.encodeProfile(tool)
	if tool.Combine {
		id, err := a.submitJob(tool, req, opts, enc, req.Paths)
		if err != nil {
			return nil, err
		}
		return []string{id}, nil
	}

	ids := make([]string, 0, len(req.Paths))
	for i := 0; i < len(req.Paths); i++ {
		id, err := a.submitJob(tool, req, opts, enc, req.Paths[i:i+1])
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// submitJob builds and enqueues one job over paths: a single file, or every
// file of a tool that combines them.
func (a *App) submitJob(tool tools.Tool, req RunRequest, opts map[string]string,
	enc tools.Encode, paths []string) (string, error) {

	first := paths[0]
	target, err := a.resolveOutput(tool, req, opts, first)
	if err != nil {
		return "", err
	}
	in := tools.Inputs{Path: first, Paths: paths, Output: target.file, Options: opts,
		Sidecar: findSidecar(first, tool.Sidecars), SubtitleTracks: tools.UnknownCount}

	// A tool whose command depends on the length, tracks, colours or size is
	// built again by the worker once the source is probed, and building it now
	// with stand-ins still checks every option while the user is looking
	deferred := tool.NeedsDuration || tool.NeedsStreams || tool.NeedsColor || tool.NeedsSize
	check := in
	if tool.NeedsDuration {
		check.DurationS = validationDurationS
	}
	plan, err := tool.Build(check, enc)
	if err != nil {
		a.releaseOutput(target.claim)
		if len(paths) == 1 {
			return "", fmt.Errorf("%s: %w", filepath.Base(first), err)
		}
		return "", err
	}

	spec := jobs.Spec{
		Tool:   tool.ID,
		Label:  jobLabel(tool, paths),
		Input:  first,
		Output: target.claim,
		Binary: a.bins.FFmpeg,
	}
	// A plan that counts its own frames needs no probing for the bar, and
	// stills have no length to probe for
	if plan.Frames > 0 {
		spec.Totals = ffmpeg.Totals{Frames: int64(plan.Frames)}
	}
	if deferred {
		if plan.Frames <= 0 {
			spec.ProbePaths = paths
		}
		spec.Prepare = func(ctx context.Context, totals ffmpeg.Totals) (jobs.Commands, error) {
			probed, err := probedInputs(ctx, tool, in, totals)
			if err != nil {
				return jobs.Commands{}, err
			}
			p, err := tool.Build(probed, enc)
			if err != nil {
				return jobs.Commands{}, err
			}
			return commandsOf(p), nil
		}
	} else {
		cmds := commandsOf(plan)
		spec.Args, spec.Passes, spec.Files, spec.Cleanup = cmds.Args, cmds.Passes, cmds.Files, cmds.Cleanup
		spec.Steps = cmds.Steps
		// The worker probes the sources just before running, so a large batch
		// is queued at once instead of waiting here on one ffprobe per file.
		if plan.NeedsProbe && plan.Frames <= 0 {
			spec.ProbePaths = paths
		}
	}

	id, err := a.queue.Submit(spec)
	if err != nil {
		a.releaseOutput(target.claim)
	}
	return id, err
}

// probedInputs fills in what the worker learned about the source just
// before the job runs, its length, and for a tool that needs them, its
// tracks and colours
func probedInputs(ctx context.Context, tool tools.Tool, in tools.Inputs, totals ffmpeg.Totals) (tools.Inputs, error) {
	if tool.NeedsDuration && totals.DurationS <= 0 {
		return in, errors.New("the length of this video couldn't be read, so it can't be sized")
	}
	in.DurationS = totals.DurationS
	if tool.NeedsSize {
		w, h, err := ffmpeg.ShownSize(ctx, in.Path)
		if err != nil {
			return in, fmt.Errorf("couldn't read the size of %s", filepath.Base(in.Path))
		}
		in.Color = tools.SourceColor{Width: w, Height: h}
	}
	if !tool.NeedsStreams && !tool.NeedsColor {
		return in, nil
	}
	info, err := ffmpeg.Probe(ctx, in.Path)
	if err != nil && tool.NeedsStreams {
		return in, fmt.Errorf("couldn't read which tracks this video has: %w", err)
	}
	// Colours are a refinement, so without them the job runs unchecked
	if err != nil {
		return in, nil
	}
	if tool.NeedsStreams {
		in.SubtitleTracks = 0
		for _, s := range info.Streams {
			if s.Type == "subtitle" {
				in.SubtitleTracks++
			}
		}
	}
	in.Color = videoColor(info)
	return in, nil
}

// videoColor describes the first video stream's colours, or zero without one
func videoColor(info ffmpeg.MediaInfo) tools.SourceColor {
	for _, s := range info.Streams {
		if s.Type == "video" {
			return tools.SourceColor{Space: s.ColorSpace, Width: s.Width, Height: s.Height}
		}
	}
	return tools.SourceColor{}
}

// validationDurationS is the stand-in length used to check a length-dependent
// tool's options before the real length is known. It is short, so a size
// that suits the real clip is never refused as too small for a longer one.
const validationDurationS = 1

// commandsOf converts a plan into the queue's form, putting ffmpeg's
// progress flags in front of every pass.
func commandsOf(p tools.Plan) jobs.Commands {
	passes := make([][]string, 0, len(p.Passes))
	for _, pass := range p.Passes {
		passes = append(passes, append(ffmpeg.ProgressArgs(), pass...))
	}
	files := make([]jobs.File, 0, len(p.Files))
	for _, f := range p.Files {
		files = append(files, jobs.File{Path: f.Path, Content: f.Content})
	}
	cmds := jobs.Commands{
		Args:    append(ffmpeg.ProgressArgs(), p.Args...),
		Passes:  passes,
		Files:   files,
		Cleanup: p.Cleanup,
		Steps:   p.Steps,
	}
	if p.Staged != "" {
		cmds.Keep = &jobs.Move{From: p.Staged, To: p.OutputPath}
	}
	if p.Disposal != 0 && p.Staged != "" {
		staged, method := p.Staged, p.Disposal
		cmds.Fixup = func() error { return giffix.SetDisposal(staged, method) }
	}
	return cmds
}

// jobLabel names a job by its tool and its file, or its first file and how
// many follow.
func jobLabel(tool tools.Tool, paths []string) string {
	label := tool.Name + " - " + filepath.Base(paths[0])
	if len(paths) > 1 {
		label += fmt.Sprintf(" and %d more", len(paths)-1)
	}
	return label
}

// encodeProfile resolves the active hardware and quality settings.
func (a *App) encodeProfile(tool tools.Tool) tools.Encode {
	a.ensureProbed()
	cfg := a.store.Get()
	backends := a.hardware().Backends
	backend, ok := ffmpeg.FindBackend(backends, cfg.BackendID)
	if !ok || !backend.Available {
		backend = ffmpeg.DefaultBackend(backends)
	}
	return tools.Encode{
		Encoder:     backend.Encoder,
		HWAccel:     backend.HWAccel,
		RateFlag:    backend.RateFlag,
		Quality:     cfg.Quality,
		Preset:      cfg.Preset,
		DeviceIndex: cfg.DeviceIndex,
		AudioKbps:   cfg.AudioBitrateKb,
		UseHardware: backend.Vendor != ffmpeg.VendorCPU,
		SpeechModel: usableModel(cfg.SpeechModel),
	}
}

// outputTarget is where one job writes. file is what ffmpeg is given; claim is
// the path reserved against other jobs, which for a frame sequence is the
// folder holding it rather than the numbered pattern.
type outputTarget struct {
	file  string
	claim string
}

// resolveOutput decides where a tool's output for one input should be written
// and reserves it until the job finishes.
func (a *App) resolveOutput(tool tools.Tool, req RunRequest,
	opts map[string]string, input string) (outputTarget, error) {

	cfg := a.store.Get()
	dir := outputDir(cfg, req, input)
	if err := ensureDir(dir); err != nil {
		return outputTarget{}, err
	}

	base := stemOf(input)
	if optIsTrue(opts["rename"]) {
		token, err := randomToken()
		if err != nil {
			return outputTarget{}, err
		}
		base = token
	}

	if tool.ID == "video.frames" {
		return a.claimFrameFolder(dir, base, opts, cfg.OverwritePolicy)
	}
	candidate := outputName(dir, base+tool.Suffix, tool.ExtensionFor(opts, input), input)
	path, err := a.claimOutput(candidate, cfg.OverwritePolicy)
	return outputTarget{file: path, claim: path}, err
}

// outputName joins an output path, and renames it when it would be the input
// itself: same folder, same name, same type. Writing there would destroy the
// file being read, mid-encode.
func outputName(dir, stem, ext, input string) string {
	candidate := filepath.Join(dir, stem+"."+ext)
	if claimKey(candidate) == claimKey(input) {
		candidate = filepath.Join(dir, stem+" (converted)."+ext)
	}
	return candidate
}

// outputDir picks the destination folder for an input.
func outputDir(cfg settings.Settings, req RunRequest, input string) string {
	if strings.TrimSpace(req.OutputDir) != "" {
		return req.OutputDir
	}
	if cfg.OutputMode == "fixed" && strings.TrimSpace(cfg.OutputDir) != "" {
		return cfg.OutputDir
	}
	return filepath.Dir(input)
}

// claimFrameFolder reserves the folder a frame extraction writes into and
// returns the numbered pattern inside it.
func (a *App) claimFrameFolder(dir, base string, opts map[string]string,
	policy string) (outputTarget, error) {

	ext := strings.TrimSpace(opts["format"])
	if ext == "" {
		ext = "png"
	}
	folder, err := a.claimOutput(filepath.Join(dir, base+"_frames"), policy)
	if err != nil {
		return outputTarget{}, err
	}
	if err := ensureDir(folder); err != nil {
		a.releaseOutput(folder)
		return outputTarget{}, err
	}
	return outputTarget{file: filepath.Join(folder, "frame_%05d."+ext), claim: folder}, nil
}

// claimOutput applies the overwrite policy to a candidate output path and
// reserves the result. A path held by a job that is still queued or running is
// never handed out again, whatever the policy: overwriting a finished file is
// the user's choice, two processes writing one file at once is not.
func (a *App) claimOutput(path, policy string) (string, error) {
	a.claimMu.Lock()
	defer a.claimMu.Unlock()

	if a.claimFreeLocked(path, policy) {
		return path, nil
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	// Bounded search for a free name (Power of Ten rule 2).
	for i := 1; i <= maxNameAttempt; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if a.claimFreeLocked(candidate, policy) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free filename beside %s", filepath.Base(path))
}

// claimFreeLocked claims path when no live job holds it and the policy allows
// it. The caller must hold claimMu.
func (a *App) claimFreeLocked(path, policy string) bool {
	key := claimKey(path)
	if a.claimed[key] {
		return false
	}
	if policy != "overwrite" {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	a.claimed[key] = true
	return true
}

// releaseOutput frees a path claimed by claimOutput. Unclaimed paths, such as
// a download's folder, are ignored.
func (a *App) releaseOutput(path string) {
	if path == "" {
		return
	}
	a.claimMu.Lock()
	delete(a.claimed, claimKey(path))
	a.claimMu.Unlock()
}

// claimKey normalises a path for comparison. Windows and macOS file systems
// ignore case, so clip.MP4 and clip.mp4 are the same file there; folding case
// everywhere costs Linux at most an unneeded " (1)".
func claimKey(path string) string {
	return strings.ToLower(filepath.Clean(path))
}

// ensureDir creates a directory when it does not already exist.
func ensureDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("no output folder selected")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("could not create %s: %w", dir, err)
	}
	return nil
}

// stemOf returns a filename without its directory or extension.
func stemOf(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// optIsTrue reports whether a raw option value means true.
func optIsTrue(v string) bool {
	return strings.TrimSpace(v) == "true"
}

// ListJobs returns every job known to the queue.
func (a *App) ListJobs() []jobs.Job {
	if a.queue == nil {
		return nil
	}
	return a.queue.List()
}

// CancelJob stops one job.
func (a *App) CancelJob(id string) error {
	if a.queue == nil {
		return errors.New("the job queue is not running")
	}
	return a.queue.Cancel(id)
}

// RetryJob runs a failed or cancelled job again with exactly the same command,
// writing to the same output. It returns the new job's ID.
func (a *App) RetryJob(id string) (string, error) {
	if a.queue == nil {
		return "", errors.New("the job queue is not running")
	}
	spec, finished, err := a.queue.Spec(id)
	if err != nil {
		return "", err
	}
	if !finished {
		return "", errors.New("that job is still queued or running")
	}
	a.retryMu.Lock()
	defer a.retryMu.Unlock()
	// A retry runs in the same work folder, which the first to end removes
	// under the other, and the output claim doesn't stop it since any
	// download ending in that folder releases the folder's claim
	if spec.WorkDir != "" && a.workDirBusy(spec.WorkDir) {
		return "", errors.New("this download is already being retried, so wait for that to finish")
	}
	// The command names its output, so it can only be retried if no live job
	// has since claimed the same path. Overwrite is right here: whatever is
	// there is this job's own partial output.
	path, err := a.claimOutput(spec.Output, "overwrite")
	if err != nil {
		return "", err
	}
	if claimKey(path) != claimKey(spec.Output) {
		a.releaseOutput(path)
		return "", errors.New("another job is writing to that output now, so wait for it to finish")
	}
	spec.ID = ""
	if spec.Binary == a.bins.YTDLP {
		spec.Args = retryDownloadArgs(spec.Args)
	}
	newID, err := a.queue.Submit(spec)
	if err != nil {
		a.releaseOutput(path)
	}
	return newID, err
}

// workDirBusy reports a job still queued or running in work
func (a *App) workDirBusy(work string) bool {
	for _, j := range a.queue.List() {
		if j.WorkDir == work && (j.State == jobs.StateQueued || j.State == jobs.StateRunning) {
			return true
		}
	}
	return false
}

// retryDownloadArgs makes yt-dlp redo a download rather than keep what the
// failed or cancelled run left under the final name, such as a remux or a
// GIF cut short, which it would otherwise report as already downloaded
func retryDownloadArgs(args []string) []string {
	if slices.Contains(args, "--force-overwrites") {
		return args
	}
	return append([]string{"--force-overwrites"}, args...)
}

// CancelAllJobs stops every queued and running job.
func (a *App) CancelAllJobs() {
	if a.queue != nil {
		a.queue.CancelAll()
	}
}

// ClearFinishedJobs drops completed jobs from the list.
func (a *App) ClearFinishedJobs() int {
	if a.queue == nil {
		return 0
	}
	return a.queue.ClearFinished()
}

// RevealOutput opens the folder containing a produced file.
func (a *App) RevealOutput(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("no output path recorded for that job")
	}
	// A frame extraction or a download records the folder it wrote into, which
	// is opened itself; a single file opens the folder that holds it.
	dir := path
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("that folder no longer exists: %w", err)
	}
	return openInExplorer(dir)
}
