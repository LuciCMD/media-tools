package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDialogStartSkipsAFolderThatsGone checks a remembered folder that was
// deleted opens the picker in the nearest folder above it, where Wails
// refused to open the picker at all
func TestDialogStartSkipsAFolderThatsGone(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "project", "clips")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := dialogStart(gone); got != gone {
		t.Errorf("an existing folder gave %q, want it unchanged", got)
	}
	if err := os.RemoveAll(filepath.Join(root, "project")); err != nil {
		t.Fatal(err)
	}
	if got := dialogStart(gone); got != root {
		t.Errorf("a deleted folder gave %q, want %q", got, root)
	}
	file := filepath.Join(root, "note.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dialogStart(file); got != root {
		t.Errorf("a file gave %q, want its folder %q", got, root)
	}
	if got := dialogStart(""); got != "" {
		t.Errorf("nothing remembered gave %q, want empty", got)
	}
}
