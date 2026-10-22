package jobs

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
)

// TestSpreadOverPassesMapsOntoOneBar checks each pass fills its share of the
// bar, the time left counts passes still to come, and only the last pass
// reports done.
func TestSpreadOverPassesMapsOntoOneBar(t *testing.T) {
	totals := ffmpeg.Totals{DurationS: 60}
	half := ffmpeg.Progress{Fraction: 0.5, ETASeconds: 15, Speed: 2, Done: true}

	first := spreadOverPasses(half, 0, 2, totals)
	if first.Fraction != 0.25 || first.Pass != 1 || first.Passes != 2 {
		t.Errorf("first pass at half = %+v, want fraction 0.25 of pass 1 of 2", first)
	}
	if first.Done {
		t.Error("the first pass must not report the job done")
	}
	if first.ETASeconds != 15+60/2 {
		t.Errorf("first pass ETA = %v, want 45 (15 left plus a 30 s pass to come)", first.ETASeconds)
	}

	last := spreadOverPasses(half, 1, 2, totals)
	if last.Fraction != 0.75 || !last.Done || last.ETASeconds != 15 {
		t.Errorf("last pass at half = %+v, want fraction 0.75, done, ETA 15", last)
	}

	unknown := spreadOverPasses(ffmpeg.Progress{Fraction: -1, ETASeconds: -1}, 0, 2, totals)
	if unknown.Fraction != -1 || unknown.ETASeconds != -1 {
		t.Errorf("an unknown fraction or ETA must stay unknown, got %+v", unknown)
	}

	single := spreadOverPasses(half, 0, 1, totals)
	if single.Fraction != 0.5 || !single.Done {
		t.Errorf("a single pass must pass through unchanged, got %+v", single)
	}
}

// TestCommandsForChecksPreparedCommands checks Prepare's output is bounded
// and its errors are passed on.
func TestCommandsForChecksPreparedCommands(t *testing.T) {
	fixed := Spec{Args: []string{"-i", "a"}, Cleanup: []string{"x"}}
	cmds, err := commandsFor(context.Background(), fixed, ffmpeg.Totals{})
	if err != nil || cmds.Args[1] != "a" || cmds.Cleanup[0] != "x" {
		t.Errorf("a spec without Prepare = %+v, %v; want its own commands", cmds, err)
	}

	var seen ffmpeg.Totals
	prepared := Spec{Prepare: func(_ context.Context, tt ffmpeg.Totals) (Commands, error) {
		seen = tt
		return Commands{Args: []string{"-i", "b"}}, nil
	}}
	if _, err := commandsFor(context.Background(), prepared, ffmpeg.Totals{DurationS: 42}); err != nil || seen.DurationS != 42 {
		t.Errorf("Prepare saw %+v (%v), want the probed 42 s", seen, err)
	}

	empty := Spec{Prepare: func(context.Context, ffmpeg.Totals) (Commands, error) { return Commands{}, nil }}
	if _, err := commandsFor(context.Background(), empty, ffmpeg.Totals{}); err == nil {
		t.Error("an empty prepared command must be refused")
	}
	tooMany := Spec{Prepare: func(context.Context, ffmpeg.Totals) (Commands, error) {
		return Commands{Args: []string{"a"}, Passes: make([][]string, maxPasses)}, nil
	}}
	if _, err := commandsFor(context.Background(), tooMany, ffmpeg.Totals{}); err == nil {
		t.Error("more passes than maxPasses must be refused")
	}
	failing := Spec{Prepare: func(context.Context, ffmpeg.Totals) (Commands, error) { return Commands{}, errors.New("nope") }}
	if _, err := commandsFor(context.Background(), failing, ffmpeg.Totals{}); err == nil || err.Error() != "nope" {
		t.Errorf("Prepare's error = %v, want it passed on", err)
	}
}

// makeClip writes a short test video with ffmpeg directly.
func makeClip(t *testing.T, bin, path string, seconds int) {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "testsrc2=s=320x240:r=30:d="+itoa(seconds), "-c:v", "libx264", "-preset", "ultrafast",
		"-y", path).CombinedOutput()
	if err != nil {
		t.Fatalf("making %s: %v\n%s", filepath.Base(path), err, out)
	}
}

// TestQueueRunsPassesAndTidiesUp runs a two-command job whose second pass
// reads a list file the job writes, and checks the passes report as one bar
// and every temporary file is gone afterwards.
func TestQueueRunsPassesAndTidiesUp(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	middle := filepath.Join(dir, "middle.mp4")
	list := filepath.Join(dir, "list.txt")
	output := filepath.Join(dir, "out.mp4")

	c := newCollector(StateSucceeded)
	q, err := NewQueue(1, c.listen)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Shutdown()

	first := append(ffmpeg.ProgressArgs(), "-f", "lavfi", "-i", "testsrc2=s=320x240:r=30:d=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-y", middle)
	second := append(ffmpeg.ProgressArgs(), "-f", "concat", "-safe", "0", "-i", list, "-c", "copy", "-y", output)
	id, err := q.Submit(Spec{
		Tool: "t", Binary: bins.FFmpeg, Output: output,
		Args: first, Passes: [][]string{second},
		Files:   []File{{Path: list, Content: "ffconcat version 1.0\nfile '" + filepath.ToSlash(middle) + "'\n"}},
		Cleanup: []string{middle},
		Totals:  ffmpeg.Totals{DurationS: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 60*time.Second)

	var lastPass int
	for _, u := range c.snapshot() {
		// The update announcing the start comes before any progress.
		if u.ID != id || u.State != StateRunning || u.Progress.Passes == 0 {
			continue
		}
		if u.Progress.Passes != 2 {
			t.Fatalf("a running update reported %d passes, want 2", u.Progress.Passes)
		}
		if u.Progress.Pass < lastPass {
			t.Errorf("the pass went backwards from %d to %d", lastPass, u.Progress.Pass)
		}
		lastPass = u.Progress.Pass
	}
	if lastPass != 2 {
		t.Errorf("the last pass reported was %d, want 2", lastPass)
	}
	if _, err := os.Stat(output); err != nil {
		t.Errorf("the second pass wrote nothing: %v", err)
	}
	for _, leftover := range []string{middle, list} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("%s was left behind", filepath.Base(leftover))
		}
	}
}

// TestQueuePrepareSeesSummedLength checks a job over several files is
// prepared with their lengths added up, and a failed Prepare fails the job
// with its message.
func TestQueuePrepareSeesSummedLength(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg or ffprobe is not installed")
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.mp4"), filepath.Join(dir, "b.mp4")
	makeClip(t, bins.FFmpeg, a, 2)
	makeClip(t, bins.FFmpeg, b, 3)

	var mu sync.Mutex
	var seen float64
	c := newCollector(StateFailed)
	q, err := NewQueue(1, c.listen)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Shutdown()

	id, err := q.Submit(Spec{Tool: "t", Binary: bins.FFmpeg, ProbePaths: []string{a, b},
		Prepare: func(_ context.Context, tt ffmpeg.Totals) (Commands, error) {
			mu.Lock()
			seen = tt.DurationS
			mu.Unlock()
			return Commands{}, errors.New("this clip is too long for that size")
		}})
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)

	mu.Lock()
	got := seen
	mu.Unlock()
	if got < 4.5 || got > 5.5 {
		t.Errorf("Prepare saw %.2fs, want about 5s (2 + 3)", got)
	}
	for _, u := range c.snapshot() {
		if u.ID == id && u.State == StateFailed && !strings.Contains(u.Error, "too long") {
			t.Errorf("the job failed with %q, want Prepare's message", u.Error)
		}
	}
}
