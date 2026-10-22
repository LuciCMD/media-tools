package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// TestJobsThenHelper stands in for a downloader, doing nothing unless
// MEDIA_TOOLS_JOBS_THEN is set: "touch path" writes a file there, "fail" fails
func TestJobsThenHelper(t *testing.T) {
	if os.Getenv("MEDIA_TOOLS_JOBS_THEN") != "1" {
		return
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 2 && args[0] == "touch" {
		if err := os.WriteFile(args[1], []byte("downloaded"), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(1)
}

// thenHelper is the helper's argument vector for mode
func thenHelper(mode ...string) []string {
	return append([]string{"-test.run=^TestJobsThenHelper$", "--"}, mode...)
}

// thenSetup gives a queue, ffmpeg, a work folder made for the job, and where
// the job's result belongs
func thenSetup(t *testing.T, waitFor State) (*Queue, *collector, ffmpeg.Binaries, string, string) {
	t.Helper()
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("MEDIA_TOOLS_JOBS_THEN", "1")
	out := t.TempDir()
	work, err := os.MkdirTemp(out, ".work-")
	if err != nil {
		t.Fatal(err)
	}
	c := newCollector(waitFor)
	q, err := NewQueue(1, c.listen)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	t.Cleanup(q.Shutdown)
	return q, c, bins, work, filepath.Join(out, "result.mp4")
}

// encodeStage is a Then stage that has ffmpeg encode seconds of video into
// work, read at its own pace when slow, to be kept at final
func encodeStage(bins ffmpeg.Binaries, work, final string, seconds int, slow bool) Stage {
	staged := filepath.Join(work, "result.mp4")
	args := ffmpeg.ProgressArgs()
	if slow {
		args = append(args, "-re")
	}
	args = append(args, "-f", "lavfi", "-i", "testsrc2=s=160x120:r=15:d="+itoa(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-y", staged)
	return Stage{
		Binary:   bins.FFmpeg,
		Totals:   ffmpeg.Totals{DurationS: float64(seconds)},
		Commands: Commands{Args: args, Steps: []string{"Encoding"}, Keep: &Move{From: staged, To: final}},
	}
}

// TestQueueThenRunsTheNextStage checks a job's next stage runs by its own
// program once the first part has succeeded, its file is moved into place,
// its step is named, the bar stays one bar, and the work folder goes
func TestQueueThenRunsTheNextStage(t *testing.T) {
	q, c, bins, work, final := thenSetup(t, StateSucceeded)
	downloaded := filepath.Join(work, "video.mp4")
	id, err := q.Submit(Spec{
		Tool: "t", Binary: os.Args[0], Output: filepath.Dir(final), WorkDir: work,
		Args: thenHelper("touch", downloaded), ThenCount: 1,
		Then: func(ctx context.Context) (Stage, error) {
			if _, err := os.Stat(downloaded); err != nil {
				return Stage{}, errors.New("Then ran before the download was there")
			}
			return encodeStage(bins, work, final, 2, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 60*time.Second)

	named := false
	for _, u := range c.snapshot() {
		if u.ID != id {
			continue
		}
		if u.State == StateFailed {
			t.Fatalf("the job failed: %s", u.Error)
		}
		if u.State != StateRunning || u.Progress.Passes == 0 {
			continue
		}
		if u.Progress.Passes != 2 {
			t.Errorf("a running update reported %d passes, want 2", u.Progress.Passes)
		}
		if u.Progress.Pass == 2 && u.Progress.Fraction >= 0 && u.Progress.Fraction < 0.5 {
			t.Errorf("the second stage's bar is at %.2f, behind the first's half", u.Progress.Fraction)
		}
		named = named || u.Progress.Stage == "Encoding"
	}
	if !named {
		t.Error("the stage's step was never named")
	}
	if _, err := os.Stat(final); err != nil {
		t.Errorf("the result wasn't moved into place: %v", err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("the work folder is still there: %v", err)
	}
}

// TestQueueThenIsSkippedWhenTheFirstPartFails checks a failed download
// never reaches its conversion, and the work folder still goes
func TestQueueThenIsSkippedWhenTheFirstPartFails(t *testing.T) {
	q, c, _, work, final := thenSetup(t, StateFailed)
	called := false
	_, err := q.Submit(Spec{
		Tool: "t", Binary: os.Args[0], Output: filepath.Dir(final), WorkDir: work,
		Args: thenHelper("fail"), ThenCount: 1,
		Then: func(ctx context.Context) (Stage, error) {
			called = true
			return Stage{}, errors.New("shouldn't run")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)
	if called {
		t.Error("Then ran after the first part failed")
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("the work folder is still there: %v", err)
	}
}

// TestQueueCancelDuringThenLeavesNothing checks cancelling during the next
// stage stops it, puts nothing at the result's place, and removes the work
// folder with the half-made file in it
func TestQueueCancelDuringThenLeavesNothing(t *testing.T) {
	q, c, bins, work, final := thenSetup(t, StateCanceled)
	downloaded := filepath.Join(work, "video.mp4")
	id, err := q.Submit(Spec{
		Tool: "t", Binary: os.Args[0], Output: filepath.Dir(final), WorkDir: work,
		Args: thenHelper("touch", downloaded), ThenCount: 1,
		Then: func(ctx context.Context) (Stage, error) {
			return encodeStage(bins, work, final, 60, true), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoding := func() bool {
		for _, u := range c.snapshot() {
			if u.ID == id && u.Progress.Stage == "Encoding" {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(30 * time.Second)
	for !encoding() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !encoding() {
		t.Fatal("the next stage never started")
	}
	if err := q.Cancel(id); err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("a cancelled job left a result: %v", err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("the work folder is still there: %v", err)
	}
}

// TestQueueCancelBeforeStartRemovesTheWorkFolder checks a job cancelled while
// still waiting, or still waiting when the queue shuts down, removes the
// folder made for it, where only a job that ran ever removed it
func TestQueueCancelBeforeStartRemovesTheWorkFolder(t *testing.T) {
	q, c, bins, work, final := thenSetup(t, StateRunning)
	busy, err := q.Submit(encodeSpec(bins.FFmpeg, filepath.Join(t.TempDir(), "busy.mp4"), 900))
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)
	called := false
	then := func(ctx context.Context) (Stage, error) {
		called = true
		return Stage{}, errors.New("shouldn't run")
	}
	waiting, err := q.Submit(Spec{Tool: "t", Binary: os.Args[0], Output: filepath.Dir(final), WorkDir: work,
		Args: thenHelper("touch", filepath.Join(work, "v.mp4")), ThenCount: 1, Then: then})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel(waiting); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("a job cancelled before starting left its work folder: %v", err)
	}

	second, err := os.MkdirTemp(filepath.Dir(final), ".work-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Submit(Spec{Tool: "t", Binary: os.Args[0], Output: filepath.Dir(final), WorkDir: second,
		Args: thenHelper("touch", filepath.Join(second, "v.mp4")), ThenCount: 1, Then: then}); err != nil {
		t.Fatal(err)
	}
	q.Shutdown()
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Errorf("a job still waiting at shutdown left its work folder: %v", err)
	}
	if called {
		t.Error("a cancelled job's next stage ran")
	}
	for _, j := range q.List() {
		if j.ID == busy && j.State != StateCanceled {
			t.Errorf("the busy job ended %s", j.State)
		}
	}
}

// TestStepStartsUnknownWithoutALength checks a named step whose length isn't
// known starts without a set point on the bar, where it jumped to its share
// and then back to unknown as the step's own samples came in
func TestStepStartsUnknownWithoutALength(t *testing.T) {
	q, c, bins, work, final := thenSetup(t, StateSucceeded)
	id, err := q.Submit(Spec{
		Tool: "t", Binary: os.Args[0], Output: filepath.Dir(final), WorkDir: work,
		Args: thenHelper("touch", filepath.Join(work, "v.mp4")), ThenCount: 1,
		Then: func(ctx context.Context) (Stage, error) {
			s := encodeStage(bins, work, final, 1, false)
			s.Totals = ffmpeg.Totals{}
			return s, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)
	for _, u := range c.snapshot() {
		if u.ID == id && u.State == StateRunning && u.Progress.Stage == "Encoding" {
			if u.Progress.Fraction >= 0 {
				t.Errorf("the step started at %.2f with no length known", u.Progress.Fraction)
			}
			return
		}
	}
	t.Error("the step was never shown")
}

// TestStepsReachTheBoardWithoutPrepare checks a job built up front, such as
// Convert to GIF, shows its step names, which only prepared jobs carried
func TestStepsReachTheBoardWithoutPrepare(t *testing.T) {
	cmds, err := commandsFor(context.Background(), Spec{Args: []string{"a"}, Passes: [][]string{{"b"}},
		Steps: []string{"Making the palette", "Drawing the GIF"}}, ffmpeg.Totals{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds.Steps) != 2 || cmds.Steps[1] != "Drawing the GIF" {
		t.Errorf("steps %q", cmds.Steps)
	}
}

// TestQueueThenMustMatchItsCount checks a next stage of a different size than
// declared fails the job rather than overrunning the bar, and a spec whose
// Then and ThenCount disagree, or that has too many commands, is refused
func TestQueueThenMustMatchItsCount(t *testing.T) {
	q, c, bins, work, final := thenSetup(t, StateFailed)
	_, err := q.Submit(Spec{
		Tool: "t", Binary: os.Args[0], Output: filepath.Dir(final), WorkDir: work,
		Args: thenHelper("touch", filepath.Join(work, "v.mp4")), ThenCount: 1,
		Then: func(ctx context.Context) (Stage, error) {
			s := encodeStage(bins, work, final, 1, false)
			s.Passes = [][]string{s.Args}
			return s, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("a stage of the wrong size still ran: %v", err)
	}

	then := func(context.Context) (Stage, error) { return Stage{}, nil }
	for name, spec := range map[string]Spec{
		"Then without a count": {Then: then},
		"a count without Then": {ThenCount: 1},
		"a negative count":     {Then: then, ThenCount: -1},
		"too many commands":    {Then: then, ThenCount: maxPasses},
	} {
		spec.Tool, spec.Binary, spec.Args = "t", "x", []string{"a"}
		if err := validateSpec(spec); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
