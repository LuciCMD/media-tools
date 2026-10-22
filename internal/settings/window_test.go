package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOldWindowFilesOpenAtTheNewSize loads files as older versions wrote them
// and checks the window each one opens at
func TestOldWindowFilesOpenAtTheNewSize(t *testing.T) {
	cases := []struct {
		name, json string
		w, h       int
	}{
		{"old default", `{"windowWidth":1180,"windowHeight":780}`, 1475, 975},
		{"no window keys", `{"preset":"medium"}`, 1475, 975},
		{"zero size", `{"windowWidth":0,"windowHeight":0}`, 1475, 975},
		{"custom kept", `{"windowWidth":1600,"windowHeight":1000}`, 1600, 1000},
		{"old width only", `{"windowWidth":1180}`, 1180, 975},
		{"tiny clamped", `{"windowWidth":10,"windowHeight":10}`, 900, 600},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(c.json), 0o600); err != nil {
			t.Fatal(err)
		}
		got := openStore(path).Get()
		if got.WindowWidth != c.w || got.WindowHeight != c.h {
			t.Errorf("%s: opens at %dx%d, want %dx%d", c.name, got.WindowWidth, got.WindowHeight, c.w, c.h)
		}
	}
}

// TestNewWindowSizeIsSavedBack checks a migrated size is what the next save
// writes, so the old pair doesn't linger in the file
func TestNewWindowSizeIsSavedBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"windowWidth":1180,"windowHeight":780}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openStore(path)
	if _, err := store.Save(store.Get()); err != nil {
		t.Fatal(err)
	}
	raw, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if raw.WindowWidth != 1475 || raw.WindowHeight != 975 {
		t.Errorf("saved %dx%d, want 1475x975", raw.WindowWidth, raw.WindowHeight)
	}
}
