package app

import (
	"os"
	"path/filepath"
	"testing"
)

// touch creates an empty file in dir.
func touch(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFindSidecarPrefersTheExactName checks clip.srt beats clip.en.srt, an
// unrelated file is ignored, and nothing is found when nothing matches.
func TestFindSidecarPrefersTheExactName(t *testing.T) {
	dir := t.TempDir()
	video := touch(t, dir, "Ocean's [cut].mp4")
	exts := []string{"srt", "ass", "vtt"}

	if got := findSidecar(video, exts); got != "" {
		t.Errorf("found %q with no subtitle file present", got)
	}
	touch(t, dir, "Ocean's [cut] extra.srt")
	touch(t, dir, "Ocean's [cut].en.fr.srt")
	if got := findSidecar(video, exts); got != "" {
		t.Errorf("found %q, which belongs to another name", got)
	}

	tagged := touch(t, dir, "Ocean's [cut].en.srt")
	if got := findSidecar(video, exts); got != tagged {
		t.Errorf("tagged: got %q, want %q", got, tagged)
	}
	exact := touch(t, dir, "Ocean's [cut].vtt")
	if got := findSidecar(video, exts); got != exact {
		t.Errorf("exact: got %q, want %q", got, exact)
	}
	if got := findSidecar(video, nil); got != "" {
		t.Errorf("a tool without sidecars found %q", got)
	}
}
