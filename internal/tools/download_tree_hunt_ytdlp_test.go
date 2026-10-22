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
// author's yt-dlp config and cache kept out
func treeHuntPlan(t *testing.T, link, out string, opts map[string]string) []string {
	t.Helper()
	tool, _ := Get("download.video")
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := tool.Build(Inputs{Path: link, Output: out, Options: valid}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	ytdlp, _ := exec.LookPath("yt-dlp")
	return append([]string{ytdlp, "--ignore-config", "--cache-dir", t.TempDir()}, plan.Args...)
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

// TestYtdlpRunCompletesInsideTheJob checks a normal MP4 and GIF download
// through ffmpeg.Run still finish whole, now that the job ends whatever is
// left in it as Run returns
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
		argv := treeHuntPlan(t, link, out, opts)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		res, err := ffmpeg.Run(ctx, ffmpeg.Request{Binary: argv[0], Args: argv[1:]}, nil)
		cancel()
		if err != nil {
			t.Fatalf("%v: %v\n%s", opts, err, res.Stderr)
		}
		entries, _ := os.ReadDir(out)
		if len(entries) != 1 {
			t.Fatalf("%v: got %d files, want 1: %v", opts, len(entries), entries)
		}
		path := filepath.Join(out, entries[0].Name())
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

// TestYtdlpCancelThenRetrySameCommand cancels a whole-video GIF during its
// conversion, then runs the exact same command again the way Retry does,
// which must succeed over the leftovers of the cancelled run
func TestYtdlpCancelThenRetrySameCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("lists processes with PowerShell")
	}
	link := localClipServer(t, "mid.mp4", "testsrc2=s=320x180:r=30:d=40")
	out := t.TempDir()
	stopLeftovers(t, out)
	argv := treeHuntPlan(t, link, out, map[string]string{"container": "gif", "span": "original",
		"sizing": "original", "pace": "original"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ffmpeg.Run(ctx, ffmpeg.Request{Binary: argv[0], Args: argv[1:]}, nil)
		done <- err
	}()
	deadline := time.Now().Add(90 * time.Second)
	converting := false
	for time.Now().Before(deadline) && !converting {
		for _, p := range processesMentioning(t, out) {
			if strings.Contains(p, "ffmpeg") && strings.Contains(p, "palettegen") {
				converting = true
			}
		}
		select {
		case err := <-done:
			t.Skipf("yt-dlp finished before the conversion could be cancelled: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !converting {
		t.Fatal("the GIF stage never started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled download reported success")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run didn't return after the cancel")
	}
	time.Sleep(500 * time.Millisecond)
	if left := processesMentioning(t, out); len(left) > 0 {
		t.Fatalf("still running after the cancel:\n%s", strings.Join(left, "\n"))
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Logf("left after the cancel: %v", names)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel2()
	res, err := ffmpeg.Run(ctx2, ffmpeg.Request{Binary: argv[0], Args: argv[1:]}, nil)
	if err != nil {
		t.Fatalf("the retry failed: %v\n%s", err, res.Stderr)
	}
	entries, _ = os.ReadDir(out)
	var gif string
	names = names[:0]
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.HasSuffix(e.Name(), ".gif") {
			gif = filepath.Join(out, e.Name())
		}
	}
	if gif == "" {
		t.Fatalf("the retry made no GIF, folder holds %v", names)
	}
	if d := probeDuration(t, gif); d < 39 || d > 41 {
		t.Errorf("the retried GIF lasts %.2f s, want about 40", d)
		// What yt-dlp says on a third go, stdout included
		log, _ := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		t.Logf("yt-dlp on the leftovers:\n%s", log)
	}
	if len(names) != 1 {
		t.Errorf("the retry left more than the GIF: %v", names)
	}
}
