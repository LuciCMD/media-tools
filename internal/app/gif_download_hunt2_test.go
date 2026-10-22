package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
	"media-tools/internal/jobs"
)

// hunt2Bins finds ffmpeg and ffprobe or skips
func hunt2Bins(t *testing.T) ffmpeg.Binaries {
	t.Helper()
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg is not installed")
	}
	bins.YTDLP = "yt-dlp-stand-in"
	return bins
}

// hunt2FakeDownload swaps a GIF download's yt-dlp command for ffmpeg making
// the clip name in work from lavfi src, so the queue runs the real next stage
// without the network
func hunt2FakeDownload(spec jobs.Spec, bins ffmpeg.Binaries, work, name, src string) jobs.Spec {
	spec.Binary = bins.FFmpeg
	args := append(ffmpeg.ProgressArgs(), "-f", "lavfi", "-i", src)
	if strings.HasSuffix(name, ".gif") {
		args = append(args, "-y", filepath.Join(work, name))
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-y", filepath.Join(work, name))
	}
	spec.Args = args
	return spec
}

// hunt2Run runs spec on a real queue and returns how it ended and every update
func hunt2Run(t *testing.T, spec jobs.Spec) (jobs.Job, []jobs.Job) {
	t.Helper()
	var mu sync.Mutex
	var seen []jobs.Job
	done := make(chan jobs.Job, 1)
	q, err := jobs.NewQueue(1, func(j jobs.Job) {
		mu.Lock()
		seen = append(seen, j)
		mu.Unlock()
		if j.State == jobs.StateSucceeded || j.State == jobs.StateFailed || j.State == jobs.StateCanceled {
			select {
			case done <- j:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Shutdown()
	if _, err := q.Submit(spec); err != nil {
		t.Fatal(err)
	}
	select {
	case end := <-done:
		mu.Lock()
		defer mu.Unlock()
		return end, append([]jobs.Job(nil), seen...)
	case <-time.After(4 * time.Minute):
		t.Fatal("the job never ended")
	}
	return jobs.Job{}, nil
}

// TestHunt2PaletteStepIsShownWhileItRuns checks the job board names
// "Making the palette" while the palette is being made, not only once it's
// done, since palettegen gives ffmpeg no frame to report until the very end
// and on a long whole video that step runs for minutes
func TestHunt2PaletteStepIsShownWhileItRuns(t *testing.T) {
	bins := hunt2Bins(t)
	out := t.TempDir()
	tool, plan, work := gifPlan(t, "https://example.com/v", out,
		map[string]string{"container": "gif", "span": "original", "sizing": "original", "pace": "original"})
	spec := downloadSpec(tool, "https://example.com/v", out, work, plan, bins)
	spec = hunt2FakeDownload(spec, bins, work, "Long [l].mp4", "testsrc2=s=1280x720:r=30:d=150")

	end, seen := hunt2Run(t, spec)
	if end.State != jobs.StateSucceeded {
		t.Fatalf("the job ended %s: %s", end.State, end.Error)
	}
	var paletteAt, drawAt time.Time
	inProgress := 0
	for _, j := range seen {
		if j.State != jobs.StateRunning {
			continue
		}
		p := j.Progress
		if p.Stage == "Making the palette" {
			if paletteAt.IsZero() {
				paletteAt = time.Now()
			}
			// The palette's last sample sits at exactly two thirds
			if p.Fraction < 0.66 {
				inProgress++
			}
		}
		if p.Stage == "Drawing the GIF" && drawAt.IsZero() {
			drawAt = time.Now()
		}
	}
	if inProgress == 0 {
		t.Errorf("no update named the palette step while it ran, only its final sample, so the board shows the download's last step and a bar stuck at a third for the whole palette pass")
	}
}

// TestHunt2GIFFromAGIFIntoAnAwkwardFolder checks a site's own GIF is turned
// into the result in an output folder whose name holds the characters
// filtergraphs and shells treat as structure, and nothing else is left
func TestHunt2GIFFromAGIFIntoAnAwkwardFolder(t *testing.T) {
	bins := hunt2Bins(t)
	out := filepath.Join(t.TempDir(), "it's [a], b; c=1 %d ü")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	tool, plan, work := gifPlan(t, "https://example.com/v", out, map[string]string{"container": "gif"})
	spec := downloadSpec(tool, "https://example.com/v", out, work, plan, bins)
	spec = hunt2FakeDownload(spec, bins, work, "Cat's [1], x [c9].gif", "testsrc2=s=320x180:r=10:d=2")

	end, _ := hunt2Run(t, spec)
	if end.State != jobs.StateSucceeded {
		t.Fatalf("the job ended %s: %s", end.State, end.Error)
	}
	names := outFiles(t, out)
	if len(names) != 1 || names[0] != "Cat's [1], x [c9].gif" {
		t.Fatalf("out holds %q, want only the GIF", names)
	}
	info, err := exec.Command(bins.FFprobe, "-v", "error", "-show_entries", "stream=codec_name,width",
		"-of", "default=nw=1", filepath.Join(out, names[0])).Output()
	if err != nil || !strings.Contains(string(info), "codec_name=gif") || !strings.Contains(string(info), "width=480") {
		t.Errorf("the result isn't a 480 px GIF: %v\n%s", err, info)
	}
}

// TestHunt2GIFUnderALongPath checks the extra work folder level doesn't break
// a GIF whose downloaded name pushes its path past Windows' 260 characters
func TestHunt2GIFUnderALongPath(t *testing.T) {
	bins := hunt2Bins(t)
	out := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 40))
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	tool, plan, work := gifPlan(t, "https://example.com/v", out, map[string]string{"container": "gif"})
	// The 150 bytes yt-dlp keeps of a title, its id and extension
	name := strings.Repeat("T", 150) + " [abcdefghijk].mp4"
	if len(filepath.Join(work, name)) <= 260 {
		t.Fatalf("the path is only %d long", len(filepath.Join(work, name)))
	}
	spec := downloadSpec(tool, "https://example.com/v", out, work, plan, bins)
	spec = hunt2FakeDownload(spec, bins, work, name, "testsrc2=s=320x180:r=10:d=2")

	end, _ := hunt2Run(t, spec)
	if end.State != jobs.StateSucceeded {
		t.Fatalf("a GIF at %d characters ended %s: %s", len(filepath.Join(work, name)), end.State, end.Error)
	}
	names := outFiles(t, out)
	if len(names) != 1 || names[0] != strings.TrimSuffix(name, ".mp4")+".gif" {
		t.Errorf("out holds %q", names)
	}
}

// TestHunt2CancelAtEachStageLeavesNothing cancels a GIF job as it reaches
// each of its three steps, which must leave the output folder empty
func TestHunt2CancelAtEachStageLeavesNothing(t *testing.T) {
	bins := hunt2Bins(t)
	for _, at := range []string{"download", "Making the palette", "Drawing the GIF"} {
		t.Run(at, func(t *testing.T) {
			out := t.TempDir()
			tool, plan, work := gifPlan(t, "https://example.com/v", out,
				map[string]string{"container": "gif", "span": "original", "sizing": "original", "pace": "original"})
			spec := downloadSpec(tool, "https://example.com/v", out, work, plan, bins)
			spec = hunt2FakeDownload(spec, bins, work, "Clip [c].mp4", "testsrc2=s=1280x720:r=30:d=120")
			if at == "download" {
				// Slow the download down so there's time to cancel it
				spec.Args = append(append(ffmpeg.ProgressArgs(), "-re"), spec.Args[len(ffmpeg.ProgressArgs()):]...)
			}
			q, err := jobs.NewQueue(1, func(jobs.Job) {})
			if err != nil {
				t.Fatal(err)
			}
			q.Start()
			defer q.Shutdown()
			id, err := q.Submit(spec)
			if err != nil {
				t.Fatal(err)
			}
			cancelled := false
			started := time.Now()
			deadline := time.Now().Add(3 * time.Minute)
			var end jobs.Job
			for time.Now().Before(deadline) && end.ID == "" {
				for _, j := range q.List() {
					if j.ID != id {
						continue
					}
					switch {
					case j.State == jobs.StateSucceeded || j.State == jobs.StateFailed || j.State == jobs.StateCanceled:
						end = j
					case !cancelled && j.State == jobs.StateRunning &&
						((at == "download" && time.Since(started) > 2*time.Second) || j.Progress.Stage == at):
						if at == "download" && j.Progress.Pass > 1 {
							t.Fatalf("the download was already over at pass %d", j.Progress.Pass)
						}
						if err := q.Cancel(id); err != nil {
							t.Fatal(err)
						}
						cancelled = true
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if end.State != jobs.StateCanceled {
				t.Fatalf("cancelling at %q ended %s: %s", at, end.State, end.Error)
			}
			if names := outFiles(t, out); len(names) != 0 {
				t.Errorf("cancelling at %q left %q", at, names)
			}
		})
	}
}

// TestHunt2ThenFailsCleanlyOnNoFile checks a download that leaves nothing in
// its work folder fails with a message, and leaves nothing behind
func TestHunt2ThenFailsCleanlyOnNoFile(t *testing.T) {
	bins := hunt2Bins(t)
	out := t.TempDir()
	tool, plan, work := gifPlan(t, "https://example.com/v", out, map[string]string{"container": "gif"})
	spec := downloadSpec(tool, "https://example.com/v", out, work, plan, bins)
	// A download that succeeds but writes elsewhere
	spec = hunt2FakeDownload(spec, bins, t.TempDir(), "x.mp4", "testsrc2=s=160x90:r=10:d=1")
	end, _ := hunt2Run(t, spec)
	if end.State != jobs.StateFailed || !strings.Contains(end.Error, "no file") {
		t.Errorf("ended %s: %q", end.State, end.Error)
	}
	if names := outFiles(t, out); len(names) != 0 {
		t.Errorf("left %q", names)
	}
}

// TestHunt2Helper stands in for a slow yt-dlp when MEDIA_TOOLS_HUNT2 is set:
// it fails unless the file named after "--" exists, then sleeps
func TestHunt2Helper(t *testing.T) {
	if os.Getenv("MEDIA_TOOLS_HUNT2") != "1" {
		return
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) != 1 {
		os.Exit(3)
	}
	if _, err := os.Stat(args[0]); err != nil {
		os.Exit(1)
	}
	time.Sleep(4 * time.Second)
	os.Exit(0)
}

// TestHunt2RetryTwiceSharesTheWorkFolder checks a GIF job can't be retried a
// second time while its first retry runs, since both would use the one work
// folder and the first to end removes it under the other, here after another
// download into the same folder ends and releases the folder's claim
func TestHunt2RetryTwiceSharesTheWorkFolder(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT2", "1")
	out := t.TempDir()
	marker := filepath.Join(t.TempDir(), "go")
	a := newTestApp()
	q, err := jobs.NewQueue(4, a.emitJob)
	if err != nil {
		t.Fatal(err)
	}
	a.queue = q
	q.Start()
	t.Cleanup(q.Shutdown)

	work, err := os.MkdirTemp(out, ".mediatools-")
	if err != nil {
		t.Fatal(err)
	}
	helper := []string{"-test.run=^TestHunt2Helper$", "--", marker}
	gif := jobs.Spec{Tool: "download.video", Binary: os.Args[0], Args: helper, Output: out, WorkDir: work,
		ThenCount: 1, Then: func(context.Context) (jobs.Stage, error) { return jobs.Stage{}, errors.New("stop") }}
	waitEnd := func(id string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			for _, j := range q.List() {
				if j.ID == id && j.State != jobs.StateQueued && j.State != jobs.StateRunning {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s never ended", id)
	}
	first, err := q.Submit(gif)
	if err != nil {
		t.Fatal(err)
	}
	waitEnd(first)

	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	retry1, err := a.RetryJob(first)
	if err != nil {
		t.Fatal(err)
	}
	// Another download into the same folder, which fails at once
	other, err := q.Submit(jobs.Spec{Tool: "download.video", Binary: os.Args[0], Output: out,
		Args: []string{"-test.run=^TestHunt2Helper$", "--", filepath.Join(out, "missing")}})
	if err != nil {
		t.Fatal(err)
	}
	waitEnd(other)

	retry2, err := a.RetryJob(first)
	if err == nil {
		s1, _, _ := q.Spec(retry1)
		s2, _, _ := q.Spec(retry2)
		t.Errorf("a second Retry was accepted while the first runs, both in work folder %q (%v), so the first to end removes it under the other",
			s2.WorkDir, s1.WorkDir == s2.WorkDir)
	}
}
