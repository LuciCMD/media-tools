package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// hunt2Blocker is a slow real ffmpeg job that keeps the only worker busy
func hunt2Blocker(bins ffmpeg.Binaries, dir string) Spec {
	args := append(ffmpeg.ProgressArgs(), "-re", "-f", "lavfi", "-i", "testsrc2=s=160x120:r=15:d=60",
		"-c:v", "libx264", "-preset", "ultrafast", "-y", filepath.Join(dir, "blocker.mp4"))
	return Spec{Tool: "t", Binary: bins.FFmpeg, Args: args, Output: dir}
}

// hunt2WorkSpec is a job with a work folder of its own, as a GIF download has
func hunt2WorkSpec(t *testing.T, out string) (Spec, string) {
	t.Helper()
	work, err := os.MkdirTemp(out, ".mediatools-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "leftover.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Spec{
		Tool: "t", Binary: "never-run", Args: []string{"a"}, Output: out, WorkDir: work, ThenCount: 1,
		Then: func(context.Context) (Stage, error) { return Stage{}, nil },
	}, work
}

// hunt2WaitRunning waits until id is running
func hunt2WaitRunning(t *testing.T, q *Queue, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range q.List() {
			if j.ID == id && j.State == StateRunning {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the blocker never started")
}

// TestHunt2CancelWhileQueuedRemovesWorkDir checks a GIF download cancelled
// before a worker picks it up still has its work folder removed, since the
// folder was made at submission inside the person's output folder
func TestHunt2CancelWhileQueuedRemovesWorkDir(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}
	out := t.TempDir()
	c := newCollector(StateCanceled)
	q, err := NewQueue(1, c.listen)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	t.Cleanup(q.Shutdown)

	blocker, err := q.Submit(hunt2Blocker(bins, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	hunt2WaitRunning(t, q, blocker)
	spec, work := hunt2WorkSpec(t, out)
	id, err := q.Submit(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel(blocker); err != nil {
		t.Fatal(err)
	}
	// Give the worker time to pick up and drop the cancelled job
	time.Sleep(2 * time.Second)
	for _, j := range q.List() {
		if j.ID == id && j.State != StateCanceled {
			t.Fatalf("the job is %s, want canceled", j.State)
		}
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(out)
		t.Errorf("a GIF download cancelled while queued left its work folder in the output folder (%v), out holds %d entries", err, len(entries))
	}
}

// TestHunt2ShutdownRemovesQueuedWorkDir checks closing the app with a GIF
// download still queued doesn't leave its work folder behind
func TestHunt2ShutdownRemovesQueuedWorkDir(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}
	out := t.TempDir()
	c := newCollector(StateCanceled)
	q, err := NewQueue(1, c.listen)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	blocker, err := q.Submit(hunt2Blocker(bins, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	hunt2WaitRunning(t, q, blocker)
	spec, work := hunt2WorkSpec(t, out)
	if _, err := q.Submit(spec); err != nil {
		t.Fatal(err)
	}
	q.Shutdown()
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("closing with a GIF download queued left its work folder: %v", err)
	}
}
