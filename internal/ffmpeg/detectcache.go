package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// maxCacheBytes bounds the detection cache read (Power of Ten rule 2).
const maxCacheBytes = 256 << 10

// Hardware is the result of one encoder detection run.
type Hardware struct {
	Backends []Backend `json:"backends"`
	Devices  []Device  `json:"devices"`
}

// detectionCache is the on-disk form of a detection run, tied to the exact
// ffmpeg binary it was measured against.
type detectionCache struct {
	FFmpegPath string   `json:"ffmpegPath"`
	FFmpegSize int64    `json:"ffmpegSize"`
	FFmpegMod  int64    `json:"ffmpegModUnix"`
	DetectedAt int64    `json:"detectedAtUnix"`
	Hardware   Hardware `json:"hardware"`
	Version    int      `json:"version"`
}

// cacheVersion changes whenever the candidate list or its meaning changes, so
// a cache written by an older build is never trusted.
const cacheVersion = 1

// DetectHardware runs a fresh detection: every candidate encoder is
// test-encoded and the targetable devices are enumerated. It ignores any cache.
func DetectHardware(ctx context.Context) Hardware {
	backends := runDetection(ctx)
	return Hardware{Backends: backends, Devices: ListDevices(ctx, backends)}
}

// LoadDetectionCache returns the hardware recorded at path, provided it was
// measured against the ffmpeg binary at ffmpegPath as it exists right now.
// Replacing or updating ffmpeg changes its size or modification time, which
// invalidates the cache.
func LoadDetectionCache(path, ffmpegPath string) (Hardware, bool) {
	if path == "" || ffmpegPath == "" {
		return Hardware{}, false
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxCacheBytes {
		return Hardware{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Hardware{}, false
	}
	var c detectionCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return Hardware{}, false
	}
	size, mod, ok := fingerprint(ffmpegPath)
	if !ok || c.Version != cacheVersion || c.FFmpegPath != ffmpegPath ||
		c.FFmpegSize != size || c.FFmpegMod != mod || len(c.Hardware.Backends) == 0 {
		return Hardware{}, false
	}
	return c.Hardware, true
}

// SaveDetectionCache records hw as measured against the binary at ffmpegPath.
func SaveDetectionCache(path, ffmpegPath string, hw Hardware) error {
	if path == "" || ffmpegPath == "" {
		return errors.New("detection cache: empty path")
	}
	size, mod, ok := fingerprint(ffmpegPath)
	if !ok {
		return fmt.Errorf("detection cache: cannot stat %s", ffmpegPath)
	}
	raw, err := json.MarshalIndent(detectionCache{
		FFmpegPath: ffmpegPath,
		FFmpegSize: size,
		FFmpegMod:  mod,
		DetectedAt: time.Now().Unix(),
		Hardware:   hw,
		Version:    cacheVersion,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("detection cache: encode: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("detection cache: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best effort cleanup; the failure is reported below.
		_ = os.Remove(tmp)
		return fmt.Errorf("detection cache: replace: %w", err)
	}
	return nil
}

// fingerprint identifies a binary by size and modification time.
func fingerprint(path string) (int64, int64, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0, 0, false
	}
	return info.Size(), info.ModTime().Unix(), true
}

// Same reports whether two detection runs reached the same conclusions: the
// same encoders available and the same devices present. Failure wording is
// ignored, since it can vary between runs without anything having changed.
func (h Hardware) Same(other Hardware) bool {
	if len(h.Backends) != len(other.Backends) || len(h.Devices) != len(other.Devices) {
		return false
	}
	available := make(map[string]bool, len(h.Backends))
	for _, b := range h.Backends {
		available[b.ID] = b.Available
	}
	for _, b := range other.Backends {
		was, ok := available[b.ID]
		if !ok || was != b.Available {
			return false
		}
	}
	for i := range h.Devices {
		if h.Devices[i] != other.Devices[i] {
			return false
		}
	}
	return true
}
