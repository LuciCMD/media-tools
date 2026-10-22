// Package ffmpeg wraps discovery, probing and supervised execution of the
// external ffmpeg / ffprobe / yt-dlp binaries the application depends on.
package ffmpeg

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// maxSearchDirs bounds the sibling-directory scan (Power of Ten rule 2).
const maxSearchDirs = 8

// ErrNotFound reports that a required external binary is absent.
var ErrNotFound = errors.New("required binary not found")

// Binaries records the resolved absolute paths of the external tools.
type Binaries struct {
	FFmpeg  string
	FFprobe string
	YTDLP   string
}

var (
	binOnce   sync.Once
	binCached Binaries
)

// Locate resolves the external binaries once and caches the result. Lookup
// order is PATH, then directories beside the running executable.
func Locate() Binaries {
	binOnce.Do(func() {
		binCached = Binaries{
			FFmpeg:  findBinary("ffmpeg"),
			FFprobe: findBinary("ffprobe"),
			YTDLP:   findBinary("yt-dlp"),
		}
	})
	return binCached
}

// findBinary returns an absolute path for name, or "" when it cannot be found.
func findBinary(name string) string {
	if name == "" {
		return ""
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, dir := range siblingDirs() {
		candidate := filepath.Join(dir, name+exeSuffix())
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		return candidate
	}
	return ""
}

// siblingDirs lists bounded candidate directories next to the executable.
func siblingDirs() []string {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	base := filepath.Dir(self)
	dirs := []string{
		base,
		filepath.Join(base, "bin"),
		filepath.Join(base, "ffmpeg"),
		filepath.Join(base, "ffmpeg", "bin"),
		filepath.Join(base, "vendor"),
	}
	if len(dirs) > maxSearchDirs {
		dirs = dirs[:maxSearchDirs]
	}
	return dirs
}

// Verify reports a descriptive error when a needed binary is missing.
func (b Binaries) Verify(needFFmpeg, needYTDLP bool) error {
	if needFFmpeg && b.FFmpeg == "" {
		return errors.New("ffmpeg not found on PATH - install it from https://ffmpeg.org/download.html")
	}
	if needFFmpeg && b.FFprobe == "" {
		return errors.New("ffprobe not found on PATH - it ships alongside ffmpeg")
	}
	if needYTDLP && b.YTDLP == "" {
		return errors.New("yt-dlp not found on PATH - install it with: pip install yt-dlp")
	}
	return nil
}
