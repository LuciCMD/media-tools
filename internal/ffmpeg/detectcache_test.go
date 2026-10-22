package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sampleHardware is a small detection result for cache tests.
func sampleHardware() Hardware {
	return Hardware{
		Backends: []Backend{
			{ID: "nvidia-h264", Available: true, Detail: "verified by test encode"},
			{ID: "cpu-h264", Available: true},
			{ID: "intel-h264", Available: false, Detail: "device not found"},
		},
		Devices: []Device{{Index: -1, Name: "CPU (software)", Vendor: VendorCPU}},
	}
}

// fakeBinary writes a stand-in ffmpeg the cache can fingerprint.
func fakeBinary(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg.exe")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestDetectionCacheRoundTrip checks a saved result loads back unchanged.
func TestDetectionCacheRoundTrip(t *testing.T) {
	bin := fakeBinary(t, "ffmpeg v1")
	cache := filepath.Join(t.TempDir(), "hardware.json")

	if err := SaveDetectionCache(cache, bin, sampleHardware()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := LoadDetectionCache(cache, bin)
	if !ok {
		t.Fatal("cache written for this binary did not load")
	}
	if !got.Same(sampleHardware()) {
		t.Errorf("loaded %+v, want %+v", got, sampleHardware())
	}
}

// TestDetectionCacheRejectsChangedBinary checks that replacing ffmpeg
// invalidates the cache, so a new build is always re-tested.
func TestDetectionCacheRejectsChangedBinary(t *testing.T) {
	bin := fakeBinary(t, "ffmpeg v1")
	cache := filepath.Join(t.TempDir(), "hardware.json")
	if err := SaveDetectionCache(cache, bin, sampleHardware()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := os.WriteFile(bin, []byte("ffmpeg v2, a different build"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, ok := LoadDetectionCache(cache, bin); ok {
		t.Error("cache loaded after ffmpeg was replaced")
	}

	other := fakeBinary(t, "ffmpeg v1")
	if _, ok := LoadDetectionCache(cache, other); ok {
		t.Error("cache loaded for a binary at a different path")
	}
}

// TestDetectionCacheRejectsGarbage checks a corrupt cache is ignored.
func TestDetectionCacheRejectsGarbage(t *testing.T) {
	bin := fakeBinary(t, "ffmpeg")
	cache := filepath.Join(t.TempDir(), "hardware.json")
	if err := os.WriteFile(cache, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := LoadDetectionCache(cache, bin); ok {
		t.Error("corrupt cache loaded")
	}
	if _, ok := LoadDetectionCache(filepath.Join(t.TempDir(), "absent.json"), bin); ok {
		t.Error("missing cache loaded")
	}
}

// TestHardwareSame checks availability changes are noticed and failure
// wording is not.
func TestHardwareSame(t *testing.T) {
	a := sampleHardware()

	reworded := sampleHardware()
	reworded.Backends[2].Detail = "a differently worded failure"
	if !a.Same(reworded) {
		t.Error("a change of failure wording counted as a hardware change")
	}

	lost := sampleHardware()
	lost.Backends[0].Available = false
	if a.Same(lost) {
		t.Error("losing NVENC did not count as a hardware change")
	}

	moreDevices := sampleHardware()
	moreDevices.Devices = append(moreDevices.Devices, Device{Index: 0, Name: "GPU", Vendor: VendorNVIDIA})
	if a.Same(moreDevices) {
		t.Error("a new device did not count as a hardware change")
	}
}
