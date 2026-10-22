package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHunt2FindDownloadKeepsTitlesThatLookTemporary checks a finished video
// whose title happens to hold ".temp." or ".part-frag", which
// --restrict-filenames keeps, is still found
func TestHunt2FindDownloadKeepsTitlesThatLookTemporary(t *testing.T) {
	for _, name := range []string{
		"Config.temp.values_explained [a1].mp4",
		"Lecture_3.Part-Fragment_two [b2].mp4",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 100), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := FindDownload(dir)
		if err != nil || got != filepath.Join(dir, name) {
			t.Errorf("%q: got %q %v, want the finished video", name, got, err)
		}
	}
}

// TestHunt2FindDownloadSourceIsAGIF checks a site's own GIF, the only file,
// is what's found
func TestHunt2FindDownloadSourceIsAGIF(t *testing.T) {
	dir := t.TempDir()
	name := "Funny_cat [xyz].gif"
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := FindDownload(dir); err != nil || got != filepath.Join(dir, name) {
		t.Errorf("got %q %v", got, err)
	}
}
