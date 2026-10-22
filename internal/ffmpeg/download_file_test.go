package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindDownloadSkipsUnfinishedFiles checks the finished video is found
// past yt-dlp's partial and fragment files, even when those are larger
func TestFindDownloadSkipsUnfinishedFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("Clip [a].mp4", 100)
	write("Clip [a].f137.mp4.part", 900)
	write("Clip [a].mp4.ytdl", 900)
	write("Clip [a].mp4.part-Frag3", 900)
	write("Clip [a].temp.mp4", 900)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := FindDownload(dir)
	if err != nil || got != filepath.Join(dir, "Clip [a].mp4") {
		t.Errorf("got %q %v, want the finished mp4", got, err)
	}

	write("Clip [a].webm", 200)
	if got, _ := FindDownload(dir); got != filepath.Join(dir, "Clip [a].webm") {
		t.Errorf("of two finished files got %q, want the larger", got)
	}
}

// TestFindDownloadKeepsTitlesThatLookUnfinished checks a finished video whose
// title holds .temp. or .part-frag is still found, where matching anywhere in
// the name skipped it and failed the job
func TestFindDownloadKeepsTitlesThatLookUnfinished(t *testing.T) {
	for _, name := range []string{"mix.temp.v2 [a1].mp4", "Song.part-frag_cut [b2].webm", "x.TEMP.y [c3].mkv"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("video"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := FindDownload(dir); err != nil || got != filepath.Join(dir, name) {
			t.Errorf("%s: got %q %v", name, got, err)
		}
	}
}

// TestFindDownloadFailsOnNothing checks an empty or missing folder is an
// error the job can show, not a conversion of nothing
func TestFindDownloadFailsOnNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindDownload(dir); err == nil {
		t.Error("an empty folder gave a file")
	}
	if err := os.WriteFile(filepath.Join(dir, "x.part"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FindDownload(dir); err == nil {
		t.Error("a folder with only a partial file gave a file")
	}
	if _, err := FindDownload(filepath.Join(dir, "gone")); err == nil {
		t.Error("a missing folder gave a file")
	}
}
