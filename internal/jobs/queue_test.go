package jobs

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// collector accumulates listener callbacks in a race-safe way.
type collector struct {
	mu      sync.Mutex
	updates []Job
	done    chan struct{}
	once    sync.Once
	waitFor State
}

// newCollector builds a collector that closes done on reaching waitFor.
func newCollector(waitFor State) *collector {
	return &collector{done: make(chan struct{}), waitFor: waitFor}
}

// listen is the Listener passed to the queue.
func (c *collector) listen(job Job) {
	c.mu.Lock()
	c.updates = append(c.updates, job)
	c.mu.Unlock()

	if job.State == c.waitFor {
		c.once.Do(func() { close(c.done) })
	}
}

// snapshot returns a copy of the accumulated updates.
func (c *collector) snapshot() []Job {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Job, len(c.updates))
	copy(out, c.updates)
	return out
}

// await blocks until the target state is seen or the timeout expires.
func (c *collector) await(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for state %q", c.waitFor)
	}
}

// encodeSpec builds a real ffmpeg job of roughly the requested duration.
func encodeSpec(ffmpegPath, output string, seconds int) Spec {
	args := append(ffmpeg.ProgressArgs(),
		"-f", "lavfi", "-i",
		"testsrc=size=1280x720:rate=30:duration="+itoa(seconds),
		"-c:v", "libx264", "-preset", "medium", "-crf", "28",
		"-y", output,
	)
	return Spec{
		Tool:   "test.encode",
		Label:  "test encode",
		Output: output,
		Binary: ffmpegPath,
		Args:   args,
		Totals: ffmpeg.Totals{DurationS: float64(seconds), Frames: int64(seconds * 30)},
	}
}

// itoa avoids importing strconv for a single conversion.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// TestQueueRunsJobToCompletion drives a real encode through the queue and
// checks the lifecycle and progress reported to the listener.
func TestQueueRunsJobToCompletion(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}

	c := newCollector(StateSucceeded)
	q, err := NewQueue(2, c.listen)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	q.Start()
	defer q.Shutdown()

	output := filepath.Join(t.TempDir(), "out.mp4")
	id, err := q.Submit(encodeSpec(bins.FFmpeg, output, 10))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	c.await(t, 90*time.Second)

	assertLifecycle(t, c.snapshot(), id)
}

// assertLifecycle checks the states and progress of one job's updates.
func assertLifecycle(t *testing.T, updates []Job, id string) {
	t.Helper()

	var states []State
	var maxFraction float64
	for _, u := range updates {
		if u.ID != id {
			continue
		}
		if len(states) == 0 || states[len(states)-1] != u.State {
			states = append(states, u.State)
		}
		if u.Progress.Fraction > maxFraction {
			maxFraction = u.Progress.Fraction
		}
	}

	if len(states) < 3 {
		t.Fatalf("expected queued -> running -> succeeded, got %v", states)
	}
	if states[0] != StateQueued {
		t.Errorf("first state = %q, want queued", states[0])
	}
	if states[1] != StateRunning {
		t.Errorf("second state = %q, want running", states[1])
	}
	if states[len(states)-1] != StateSucceeded {
		t.Errorf("final state = %q, want succeeded", states[len(states)-1])
	}
	if maxFraction != 1 {
		t.Errorf("peak progress fraction = %v, want 1", maxFraction)
	}
}

// TestQueueCancelStopsRunningJob checks that cancelling a running job reaches
// the canceled state rather than failing or hanging.
func TestQueueCancelStopsRunningJob(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}

	running := newCollector(StateRunning)
	canceled := newCollector(StateCanceled)
	q, err := NewQueue(1, func(job Job) {
		running.listen(job)
		canceled.listen(job)
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	q.Start()
	defer q.Shutdown()

	output := filepath.Join(t.TempDir(), "cancel.mp4")
	// A long encode, so cancellation lands while it is genuinely running.
	id, err := q.Submit(encodeSpec(bins.FFmpeg, output, 900))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	running.await(t, 30*time.Second)
	if err := q.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	canceled.await(t, 30*time.Second)

	for _, job := range q.List() {
		if job.ID == id && job.State != StateCanceled {
			t.Errorf("final state = %q, want canceled", job.State)
		}
	}
}

// TestQueueRejectsInvalidSpec checks submission validation.
func TestQueueRejectsInvalidSpec(t *testing.T) {
	q, err := NewQueue(1, func(Job) {})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer q.Shutdown()

	cases := []struct {
		name string
		spec Spec
	}{
		{"no binary", Spec{Tool: "t", Args: []string{"-x"}}},
		{"no args", Spec{Tool: "t", Binary: "ffmpeg"}},
		{"no tool", Spec{Binary: "ffmpeg", Args: []string{"-x"}}},
	}
	for _, c := range cases {
		if _, err := q.Submit(c.spec); err == nil {
			t.Errorf("%s: Submit accepted an invalid spec", c.name)
		}
	}
}

// TestNewQueueClampsWorkers checks the worker count is bounded both ways.
func TestNewQueueClampsWorkers(t *testing.T) {
	low, err := NewQueue(0, func(Job) {})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer low.Shutdown()
	if low.workers != MinWorkers {
		t.Errorf("workers = %d, want clamped up to %d", low.workers, MinWorkers)
	}

	high, err := NewQueue(999, func(Job) {})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer high.Shutdown()
	if high.workers != MaxWorkers {
		t.Errorf("workers = %d, want clamped down to %d", high.workers, MaxWorkers)
	}
}

// TestNewQueueRequiresListener checks the nil-listener guard.
func TestNewQueueRequiresListener(t *testing.T) {
	if _, err := NewQueue(1, nil); err == nil {
		t.Error("NewQueue accepted a nil listener")
	}
}

// TestClearFinishedKeepsActiveJobs checks that clearing only drops terminal
// jobs and leaves queued work in place.
func TestClearFinishedKeepsActiveJobs(t *testing.T) {
	q, err := NewQueue(1, func(Job) {})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer q.Shutdown()

	// The queue is never started, so submitted jobs stay queued.
	id, err := q.Submit(Spec{Tool: "t", Binary: "ffmpeg", Args: []string{"-version"}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if removed := q.ClearFinished(); removed != 0 {
		t.Errorf("ClearFinished removed %d queued jobs, want 0", removed)
	}
	if len(q.List()) != 1 {
		t.Errorf("queued job was dropped by ClearFinished")
	}

	if err := q.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if removed := q.ClearFinished(); removed != 1 {
		t.Errorf("ClearFinished removed %d cancelled jobs, want 1", removed)
	}
	if len(q.List()) != 0 {
		t.Error("cancelled job survived ClearFinished")
	}
}

// TestCancelUnknownJob checks the error path for an unknown identifier.
func TestCancelUnknownJob(t *testing.T) {
	q, err := NewQueue(1, func(Job) {})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer q.Shutdown()

	if err := q.Cancel("does-not-exist"); err == nil {
		t.Error("Cancel accepted an unknown job ID")
	}
}

// TestQueueEvictsFinishedJobsWhenFull checks that finished jobs make room for
// new work instead of the queue refusing submissions for the rest of the
// session once MaxQueued jobs have ever been run.
func TestQueueEvictsFinishedJobsWhenFull(t *testing.T) {
	done := make(chan struct{}, MaxQueued+1)
	q, err := NewQueue(MaxWorkers, func(job Job) {
		if job.State == StateFailed {
			done <- struct{}{}
		}
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	q.Start()
	defer q.Shutdown()

	// A binary that cannot exist fails at spawn, so every job ends quickly.
	spec := Spec{Tool: "t", Binary: filepath.Join(t.TempDir(), "missing-binary"), Args: []string{"-x"}}
	for i := 0; i < MaxQueued; i++ {
		if _, err := q.Submit(spec); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	for i := 0; i < MaxQueued; i++ {
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("only %d of %d jobs finished", i, MaxQueued)
		}
	}

	if _, err := q.Submit(spec); err != nil {
		t.Fatalf("Submit on a queue full of finished jobs: %v", err)
	}
	if got := len(q.List()); got != MaxQueued {
		t.Errorf("queue holds %d jobs, want %d after evicting one", got, MaxQueued)
	}
}

// TestQueueProbesDeferredTotals checks that a job submitted with a ProbePath
// and no totals has its duration resolved by the worker, so the bar still
// reports a real percentage.
func TestQueueProbesDeferredTotals(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg or ffprobe is not installed")
	}

	source := filepath.Join(t.TempDir(), "source.mp4")
	prepare := newCollector(StateSucceeded)
	pq, err := NewQueue(1, prepare.listen)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	pq.Start()
	if _, err := pq.Submit(encodeSpec(bins.FFmpeg, source, 6)); err != nil {
		t.Fatalf("Submit source: %v", err)
	}
	prepare.await(t, 60*time.Second)
	pq.Shutdown()

	c := newCollector(StateSucceeded)
	q, err := NewQueue(1, c.listen)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	q.Start()
	defer q.Shutdown()

	output := filepath.Join(t.TempDir(), "out.mp4")
	args := append(ffmpeg.ProgressArgs(), "-i", source,
		"-c:v", "libx264", "-preset", "medium", "-crf", "28", "-y", output)
	id, err := q.Submit(Spec{Tool: "t", Binary: bins.FFmpeg, Args: args, ProbePaths: []string{source}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	c.await(t, 60*time.Second)

	var total float64
	for _, u := range c.snapshot() {
		if u.ID == id && u.Progress.TotalS > total {
			total = u.Progress.TotalS
		}
	}
	if total < 5 || total > 7 {
		t.Errorf("probed total = %.2fs, want about 6s", total)
	}
}

// TestSpecReportsFinishedAndCopiesArgs checks the retry lookup: it knows
// whether a job has finished, and hands back arguments the caller cannot
// use to alter the stored job.
func TestSpecReportsFinishedAndCopiesArgs(t *testing.T) {
	q, err := NewQueue(1, func(Job) {})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer q.Shutdown()

	id, err := q.Submit(Spec{Tool: "t", Binary: "ffmpeg", Args: []string{"-version"}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	spec, finished, err := q.Spec(id)
	if err != nil || finished {
		t.Fatalf("queued job: finished=%v err=%v, want unfinished", finished, err)
	}
	spec.Args[0] = "-changed"
	if again, _, _ := q.Spec(id); again.Args[0] != "-version" {
		t.Error("changing the returned arguments changed the stored job")
	}

	if err := q.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, finished, _ := q.Spec(id); !finished {
		t.Error("a cancelled job was not reported as finished")
	}
	if _, _, err := q.Spec("missing"); err == nil {
		t.Error("Spec accepted an unknown job")
	}
}

// TestProgressGateLetsANewStepThrough checks a sample starting a new download
// step or playlist entry is published at once, where the throttle used to
// drop it and leave the readout on the download's last "0:00 left" while a
// merge or GIF conversion ran
func TestProgressGateLetsANewStepThrough(t *testing.T) {
	gate := progressGate{throttle: newThrottle(10_000)}
	if !gate.allow(ffmpeg.Progress{Stage: "Downloading", Fraction: 0.1}) {
		t.Fatal("the first sample was held back")
	}
	if gate.allow(ffmpeg.Progress{Stage: "Downloading", Fraction: 0.2}) {
		t.Error("a sample inside the interval got through")
	}
	if !gate.allow(ffmpeg.Progress{Stage: "Converting", Fraction: -1}) {
		t.Error("a new step was held back")
	}
	if !gate.allow(ffmpeg.Progress{Stage: "Converting", Item: 2}) {
		t.Error("a new playlist entry was held back")
	}
	if !gate.allow(ffmpeg.Progress{Stage: "Converting", Item: 2, Done: true}) {
		t.Error("the final sample was held back")
	}
}
