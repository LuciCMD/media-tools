package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// treeHuntPlan builds Download Video for opts against link, with the
// author's yt-dlp config and cache kept out, and returns its work folder
func treeHuntPlan(t *testing.T, link, out string, opts map[string]string) ([]string, string) {
	t.Helper()
	tool, _ := Get("download.video")
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatal(err)
	}
	work := testWorkDir(t, out)
	plan, err := tool.Build(Inputs{Path: link, Output: out, WorkDir: work, Options: valid}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Finish == nil {
		_ = os.Remove(work)
	}
	ytdlp, _ := exec.LookPath("yt-dlp")
	return append([]string{ytdlp, "--ignore-config", "--cache-dir", t.TempDir()}, plan.Args...), work
}

// stopLeftovers ends anything still mentioning out, by PID
func stopLeftovers(t *testing.T, out string) {
	t.Cleanup(func() {
		for _, p := range processesMentioning(t, out) {
			if pid, _ := strconv.Atoi(strings.Fields(p)[0]); pid > 0 {
				_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
			}
		}
	})
}

// TestYtdlpRunCompletesInsideTheJob checks a normal MP4 download and a GIF's
// clip, cut by ffmpeg under yt-dlp, through ffmpeg.Run still finish whole,
// now that the job ends whatever is left in it as Run returns
func TestYtdlpRunCompletesInsideTheJob(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("lists processes with PowerShell")
	}
	link := localClipServer(t, "clip.mp4", "testsrc2=s=320x180:r=30:d=12")
	for _, opts := range []map[string]string{
		{"container": "mp4"},
		{"container": "gif", "span": "clip", "start": "00:00:02", "end": "00:00:06"},
	} {
		out := t.TempDir()
		stopLeftovers(t, out)
		argv, work := treeHuntPlan(t, link, out, opts)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		res, err := ffmpeg.Run(ctx, ffmpeg.Request{Binary: argv[0], Args: argv[1:]}, nil)
		cancel()
		if err != nil {
			t.Fatalf("%v: %v\n%s", opts, err, res.Stderr)
		}
		// A GIF's clip waits in its work folder for the GIF to be made from it
		dir := out
		if opts["container"] == "gif" {
			dir = work
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("%v: got %d files, want 1: %v", opts, len(entries), entries)
		}
		path := filepath.Join(dir, entries[0].Name())
		d := probeDuration(t, path)
		want := 12.0
		if opts["container"] == "gif" {
			want = 4
		}
		if d < want-0.5 || d > want+0.5 {
			t.Errorf("%v: %s lasts %.2f s, want about %.0f", opts, entries[0].Name(), d, want)
		}
		if left := processesMentioning(t, out); len(left) > 0 {
			t.Errorf("%v: still running after Run returned:\n%s", opts, strings.Join(left, "\n"))
		}
	}
}
