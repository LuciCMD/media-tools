package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestToolDefaultsSurviveManyTools checks that remembered options are not
// discarded once more than a handful of tools have been used. The limit was
// once 32, and trimming dropped a random subset on every save.
func TestToolDefaultsSurviveManyTools(t *testing.T) {
	store := openStore(filepath.Join(t.TempDir(), "settings.json"))

	next := store.Get()
	for i := 0; i < 300; i++ {
		next.ToolDefaults[fmt.Sprintf("tool%03d.option", i)] = "value"
	}
	saved, err := store.Save(next)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(saved.ToolDefaults) != 300 {
		t.Errorf("kept %d remembered options, want 300", len(saved.ToolDefaults))
	}

	reopened := openStore(store.Path())
	if got := len(reopened.Get().ToolDefaults); got != 300 {
		t.Errorf("reloaded %d remembered options, want 300", got)
	}
}

// TestTrimMapIsDeterministic checks that trimming keeps the same entries every
// time, rather than whichever ones map iteration happened to yield.
func TestTrimMapIsDeterministic(t *testing.T) {
	m := make(map[string]string, 50)
	for i := 0; i < 50; i++ {
		m[fmt.Sprintf("k%02d", i)] = "v"
	}
	for attempt := 0; attempt < 20; attempt++ {
		got := trimMap(m, 10)
		if len(got) != 10 {
			t.Fatalf("trimMap kept %d entries, want 10", len(got))
		}
		for i := 0; i < 10; i++ {
			if _, ok := got[fmt.Sprintf("k%02d", i)]; !ok {
				t.Fatalf("attempt %d dropped k%02d", attempt, i)
			}
		}
	}
}

// TestSanitizeClampsRanges checks every clamped field at both ends.
func TestSanitizeClampsRanges(t *testing.T) {
	low := sanitize(Settings{Quality: -5, AudioBitrateKb: 1, Concurrency: 0, DeviceIndex: -9,
		WindowWidth: 10, WindowHeight: 10, OutputMode: "nonsense"})
	if low.Quality != 0 || low.AudioBitrateKb != 32 || low.Concurrency != 1 ||
		low.DeviceIndex != -1 || low.WindowWidth != 900 || low.WindowHeight != 600 {
		t.Errorf("low values not clamped: %+v", low)
	}
	if low.OutputMode != "alongside" {
		t.Errorf("unknown output mode = %q, want alongside", low.OutputMode)
	}

	high := sanitize(Settings{Quality: 99, AudioBitrateKb: 9999, Concurrency: 64, DeviceIndex: 99,
		WindowWidth: 99999, WindowHeight: 99999, OutputMode: "fixed"})
	if high.Quality != 51 || high.AudioBitrateKb != 512 || high.Concurrency != 8 ||
		high.DeviceIndex != 16 || high.WindowWidth != 5120 || high.WindowHeight != 2880 {
		t.Errorf("high values not clamped: %+v", high)
	}
	if high.OutputMode != "fixed" {
		t.Errorf("fixed output mode was not kept")
	}
}

// TestMergeFillsMissingFields checks that a settings file from an older
// version, missing newer fields, loads with defaults rather than zeroes.
func TestMergeFillsMissingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"quality": 18}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := openStore(path).Get()
	want := Defaults()

	if got.Quality != 18 {
		t.Errorf("quality = %d, want the stored 18", got.Quality)
	}
	if got.Preset != want.Preset || got.Concurrency != want.Concurrency ||
		got.AudioBitrateKb != want.AudioBitrateKb || got.OverwritePolicy != want.OverwritePolicy {
		t.Errorf("missing fields not defaulted: %+v", got)
	}
	if got.ToolDefaults == nil || got.LastDirs == nil || got.RecentFiles == nil {
		t.Error("nil collections were not initialised")
	}
}

// TestMalformedFileFallsBackToDefaults checks that a corrupt settings file
// does not stop the application from starting.
func TestMalformedFileFallsBackToDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := openStore(path).Get(); got.Quality != Defaults().Quality {
		t.Errorf("corrupt file did not fall back to defaults: %+v", got)
	}
}

// TestSaveLeavesNoTempFile checks the atomic write cleans up after itself.
func TestSaveLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	store := openStore(filepath.Join(dir, "settings.json"))
	if _, err := store.Save(store.Get()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only settings.json", names)
	}
}

// TestRememberSelectionBoundsRecentFiles checks the recent list stays capped
// and most-recent-first without duplicates.
func TestRememberSelectionBoundsRecentFiles(t *testing.T) {
	store := openStore(filepath.Join(t.TempDir(), "settings.json"))
	for i := 0; i < MaxRecentPaths+5; i++ {
		store.RememberSelection("tool", "dir", fmt.Sprintf("file%02d", i))
	}
	store.RememberSelection("tool", "dir", "file03")

	got := store.Get()
	if len(got.RecentFiles) != MaxRecentPaths {
		t.Fatalf("recent list holds %d, want %d", len(got.RecentFiles), MaxRecentPaths)
	}
	if got.RecentFiles[0] != "file03" {
		t.Errorf("most recent = %q, want file03", got.RecentFiles[0])
	}
	seen := make(map[string]bool, len(got.RecentFiles))
	for _, f := range got.RecentFiles {
		if seen[f] {
			t.Errorf("duplicate %q in recent list", f)
		}
		seen[f] = true
	}
	if store.LastDir("tool") != "dir" {
		t.Errorf("last dir = %q, want dir", store.LastDir("tool"))
	}
}

// TestOldDefaultWindowIsBumped checks a file still holding the pre-2026-10-20
// default window opens at the new default, while any other size is kept
func TestOldDefaultWindowIsBumped(t *testing.T) {
	old := merge(Defaults(), Settings{WindowWidth: 1180, WindowHeight: 780})
	if old.WindowWidth != 1475 || old.WindowHeight != 975 {
		t.Errorf("old default became %dx%d, want 1475x975", old.WindowWidth, old.WindowHeight)
	}
	custom := merge(Defaults(), Settings{WindowWidth: 1180, WindowHeight: 900})
	if custom.WindowWidth != 1180 || custom.WindowHeight != 900 {
		t.Errorf("a custom size became %dx%d, want 1180x900", custom.WindowWidth, custom.WindowHeight)
	}
}
