// Package jobs runs media operations on a bounded worker pool and reports
// their progress to a listener.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"media-tools/internal/ffmpeg"
)

// Bounds on the queue (Power of Ten rules 2 and 3).
const (
	MaxQueued      = 512
	MaxWorkers     = 8
	MinWorkers     = 1
	emitIntervalMs = 120
	// MaxCommands bounds the commands in one job, a GIF of 500 images needing 502
	MaxCommands   = 512
	maxPasses     = MaxCommands
	maxProbePaths = 128
	maxJobFiles   = 8
)

// State is the lifecycle position of a job.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

// Spec is the immutable description of work to perform.
type Spec struct {
	ID     string        `json:"id"`
	Tool   string        `json:"tool"`
	Label  string        `json:"label"`
	Input  string        `json:"input"`
	Output string        `json:"output"`
	Binary string        `json:"-"`
	Args   []string      `json:"-"`
	Totals ffmpeg.Totals `json:"-"`

	// Passes are commands run after Args, in order, as one job with one
	// progress bar: the second pass of a two-pass encode.
	Passes [][]string `json:"-"`
	// Files are written before the first command and removed after the last.
	Files []File `json:"-"`
	// Cleanup names files the commands leave behind, removed after the last.
	Cleanup []string `json:"-"`
	// Steps names each command for the job board, in order
	Steps []string `json:"-"`

	// ProbePaths, when Totals is empty, are probed by the worker just before
	// the job runs and their durations added up. Probing at submission would
	// make a large batch wait on one ffprobe per file before starting.
	ProbePaths []string `json:"-"`
	// Prepare, when set, builds the commands once the worker knows the
	// source's length, for work whose command depends on it, such as a
	// bitrate computed to hit a file size. It replaces Args, Passes, Files
	// and Cleanup.
	Prepare func(context.Context, ffmpeg.Totals) (Commands, error) `json:"-"`

	// Then, when set, builds the rest of the job once the commands above have
	// succeeded, run by a program of its own, such as ffmpeg making a GIF from
	// what yt-dlp downloaded, and ThenCount is how many commands it returns
	// so one bar covers the whole job from the start
	Then      func(context.Context) (Stage, error) `json:"-"`
	ThenCount int                                  `json:"-"`
	// WorkDir is a folder made for this job alone, removed with all it holds
	// once the job ends however it ends
	WorkDir string `json:"-"`
}

// Commands is the executable part of a Spec, as Prepare produces it.
type Commands struct {
	Args    []string
	Passes  [][]string
	Files   []File
	Cleanup []string
	// Steps names each command for the job board, in order
	Steps []string
	// Fixup changes the result once every command has succeeded, before Keep
	// moves it, for what the program can't be told to do itself
	Fixup func() error
	// Keep moves a file into place once every command has succeeded, so a
	// failed or cancelled job leaves nothing half made at its destination
	Keep *Move
}

// Stage is the rest of a job that Then builds, with the program that runs it
// and the extent of its work for the bar
type Stage struct {
	Binary string
	Totals ffmpeg.Totals
	Commands
}

// Move is a rename from a job's own folder to where its result belongs
type Move struct {
	From string
	To   string
}

// File is a small text file a job needs on disk while it runs, such as the
// list the concat demuxer reads.
type File struct {
	Path    string
	Content string
}

// Job is a Spec plus its mutable execution state.
type Job struct {
	Spec
	State     State           `json:"state"`
	Progress  ffmpeg.Progress `json:"progress"`
	Error     string          `json:"error"`
	StartedAt int64           `json:"startedAt"`
	EndedAt   int64           `json:"endedAt"`
}

// Listener receives job state changes for forwarding to the UI.
type Listener func(Job)

// Queue executes jobs on a fixed pool of workers.
type Queue struct {
	mu       sync.RWMutex
	jobs     map[string]*Job
	order    []string
	cancels  map[string]context.CancelFunc
	pending  chan string
	listener Listener
	workers  int
	started  bool
	seq      atomic.Uint64
	wg       sync.WaitGroup
	rootCtx  context.Context
	stop     context.CancelFunc
}

// NewQueue creates a queue with a clamped worker count.
func NewQueue(workers int, listener Listener) (*Queue, error) {
	if listener == nil {
		return nil, errors.New("jobs: listener must not be nil")
	}
	if workers < MinWorkers {
		workers = MinWorkers
	}
	if workers > MaxWorkers {
		workers = MaxWorkers
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{
		jobs:     make(map[string]*Job, MaxQueued),
		order:    make([]string, 0, MaxQueued),
		cancels:  make(map[string]context.CancelFunc, MaxWorkers),
		pending:  make(chan string, MaxQueued),
		listener: listener,
		workers:  workers,
		rootCtx:  ctx,
		stop:     cancel,
	}, nil
}

// Start launches the worker pool. It is safe to call more than once.
func (q *Queue) Start() {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return
	}
	q.started = true
	count := q.workers
	q.mu.Unlock()

	for i := 0; i < count; i++ {
		q.wg.Add(1)
		go q.worker()
	}
}

// Submit enqueues a job and returns its identifier.
func (q *Queue) Submit(spec Spec) (string, error) {
	if err := validateSpec(spec); err != nil {
		return "", err
	}

	q.mu.Lock()
	// Finished jobs count towards the limit until they are cleared, so a long
	// session would otherwise start refusing work. The oldest finished job
	// makes room instead; only live work can fill the queue.
	if len(q.order) >= MaxQueued && !q.evictOldestFinishedLocked() {
		q.mu.Unlock()
		return "", fmt.Errorf("jobs: queue is full (limit %d)", MaxQueued)
	}
	if spec.ID == "" {
		spec.ID = fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), q.seq.Add(1))
	}
	job := &Job{Spec: spec, State: StateQueued, Progress: ffmpeg.Progress{Fraction: -1, ETASeconds: -1}}
	q.jobs[spec.ID] = job
	q.order = append(q.order, spec.ID)
	snapshot := *job
	q.mu.Unlock()

	q.listener(snapshot)

	select {
	case q.pending <- spec.ID:
		return spec.ID, nil
	default:
		q.finish(spec.ID, StateFailed, "queue backlog is full")
		return spec.ID, errors.New("jobs: queue backlog is full")
	}
}

// validateSpec rejects a malformed job before it reaches a worker.
func validateSpec(spec Spec) error {
	if spec.Binary == "" {
		return errors.New("jobs: spec has no binary")
	}
	if len(spec.Args) == 0 && spec.Prepare == nil {
		return errors.New("jobs: spec has no arguments")
	}
	if len(spec.Passes)+1+spec.ThenCount > maxPasses {
		return fmt.Errorf("jobs: spec has more than %d commands", maxPasses)
	}
	if (spec.Then == nil) != (spec.ThenCount == 0) || spec.ThenCount < 0 {
		return errors.New("jobs: spec's Then and ThenCount don't match")
	}
	if len(spec.ProbePaths) > maxProbePaths || len(spec.Files) > maxJobFiles {
		return errors.New("jobs: spec probes or writes too many files")
	}
	if spec.Tool == "" {
		return errors.New("jobs: spec has no tool name")
	}
	return nil
}

// worker consumes job IDs until the queue is shut down.
func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.rootCtx.Done():
			return
		case id, ok := <-q.pending:
			if !ok {
				return
			}
			q.execute(id)
		}
	}
}

// execute runs one job to completion, reporting progress as it goes.
func (q *Queue) execute(id string) {
	ctx, cancel := context.WithCancel(q.rootCtx)
	defer func() {
		cancel()
		q.mu.Lock()
		delete(q.cancels, id)
		q.mu.Unlock()
	}()

	// The canceller is registered by begin, under the same lock that marks the
	// job running. Registering it afterwards would leave a window in which the
	// listener has already reported "running" but Cancel could not find a
	// canceller, so a click the instant a job starts would be rejected.
	spec, ok := q.begin(id, cancel)
	if !ok {
		return
	}

	totals := resolveTotals(ctx, spec)
	cmds, err := commandsFor(ctx, spec, totals)
	if err != nil {
		q.settle(ctx, id, err)
		return
	}
	count := len(cmds.Passes) + 1 + spec.ThenCount
	err = writeFiles(cmds.Files)
	if err == nil {
		err = q.runPasses(ctx, id, spec.Binary, cmds, totals, 0, count)
	}
	if err == nil && cmds.Fixup != nil {
		err = cmds.Fixup()
	}
	if err == nil {
		err = keep(cmds.Keep)
	}
	if err == nil && spec.Then != nil {
		err = q.runThen(ctx, id, spec, len(cmds.Passes)+1, count)
	}
	// Temporary files go whether the job succeeds, fails or is cancelled, and
	// before the outcome is announced, so none is still there once it ends.
	removeFiles(cmds)
	if spec.WorkDir != "" {
		_ = os.RemoveAll(spec.WorkDir)
	}
	q.settle(ctx, id, err)
}

// runThen builds and runs the rest of a job once its first commands have
// succeeded, carrying on the same bar from command first of count
func (q *Queue) runThen(ctx context.Context, id string, spec Spec, first, count int) error {
	stage, err := spec.Then(ctx)
	if err != nil {
		return err
	}
	if len(stage.Passes)+1 != spec.ThenCount || len(stage.Args) == 0 || stage.Binary == "" ||
		len(stage.Files) > maxJobFiles {
		return errors.New("jobs: the job's next step isn't what it said it would be")
	}
	err = writeFiles(stage.Files)
	if err == nil {
		err = q.runPasses(ctx, id, stage.Binary, stage.Commands, stage.Totals, first, count)
	}
	if err == nil && stage.Fixup != nil {
		err = stage.Fixup()
	}
	if err == nil {
		err = keep(stage.Keep)
	}
	removeFiles(stage.Commands)
	return err
}

// keep moves a finished result into place
func keep(m *Move) error {
	if m == nil {
		return nil
	}
	if err := os.Rename(m.From, m.To); err != nil {
		return fmt.Errorf("couldn't move the result into place: %w", err)
	}
	return nil
}

// runPasses runs each command in turn, reporting progress across all of them
// as one bar, pass one of two filling the first half, these being the job's
// commands from first on, of count in all
func (q *Queue) runPasses(ctx context.Context, id, binary string, cmds Commands, totals ffmpeg.Totals,
	first, count int) error {
	all := make([][]string, 0, 1+len(cmds.Passes))
	all = append(all, cmds.Args)
	all = append(all, cmds.Passes...)
	if len(all) > maxPasses || first+len(all) > count {
		return fmt.Errorf("jobs: more than %d commands", maxPasses)
	}

	for i, args := range all {
		pass := first + i
		step := ""
		if i < len(cmds.Steps) {
			step = cmds.Steps[i]
		}
		// A named step shows as it starts, since some, such as palettegen,
		// report nothing until they end
		if step != "" {
			start := ffmpeg.Progress{Stage: step, ETASeconds: -1}
			// Without a length the samples that follow are unknown too, so no set point first
			if totals.DurationS <= 0 && totals.Frames <= 0 {
				start.Fraction = -1
			}
			q.publishProgress(id, spreadOverPasses(start, pass, count, totals))
		}
		gate := progressGate{throttle: newThrottle(emitIntervalMs)}
		_, err := ffmpeg.Run(ctx, ffmpeg.Request{Binary: binary, Args: args, Totals: totals},
			func(p ffmpeg.Progress) {
				// A program that names its own steps, as yt-dlp does, keeps them
				if p.Stage == "" {
					p.Stage = step
				}
				p = spreadOverPasses(p, pass, count, totals)
				if gate.allow(p) {
					q.publishProgress(id, p)
				}
			})
		if err != nil {
			return err
		}
	}
	return nil
}

// spreadOverPasses maps one pass's progress onto the whole job. The time
// left adds a full pass for each one still to come, at the current speed.
func spreadOverPasses(p ffmpeg.Progress, pass, count int, totals ffmpeg.Totals) ffmpeg.Progress {
	p.Pass, p.Passes = pass+1, count
	if count <= 1 {
		return p
	}
	p.Done = p.Done && pass == count-1
	if p.Fraction >= 0 {
		p.Fraction = (float64(pass) + p.Fraction) / float64(count)
	}
	if p.ETASeconds >= 0 && p.Speed > 0 && totals.DurationS > 0 {
		p.ETASeconds += float64(count-1-pass) * totals.DurationS / p.Speed
	}
	return p
}

// commandsFor returns the job's commands, building them now if they depend
// on the source's length.
func commandsFor(ctx context.Context, spec Spec, totals ffmpeg.Totals) (Commands, error) {
	if spec.Prepare == nil {
		return Commands{Args: spec.Args, Passes: spec.Passes, Files: spec.Files, Cleanup: spec.Cleanup,
			Steps: spec.Steps}, nil
	}
	cmds, err := spec.Prepare(ctx, totals)
	if err != nil {
		return Commands{}, err
	}
	if len(cmds.Args) == 0 || len(cmds.Passes)+1 > maxPasses || len(cmds.Files) > maxJobFiles {
		return Commands{}, errors.New("jobs: prepared commands are empty or too many")
	}
	return cmds, nil
}

// writeFiles writes a job's temporary files.
func writeFiles(files []File) error {
	for _, f := range files {
		if err := os.WriteFile(f.Path, []byte(f.Content), 0o600); err != nil {
			return fmt.Errorf("couldn't write %s: %w", f.Path, err)
		}
	}
	return nil
}

// removeFiles deletes a job's temporary files and leftovers.
func removeFiles(cmds Commands) {
	for _, f := range cmds.Files {
		// A file that was never written, or is already gone, is not an error.
		_ = os.Remove(f.Path)
	}
	for _, path := range cmds.Cleanup {
		_ = os.Remove(path)
	}
}

// resolveTotals returns the extent of the work, probing the sources when the
// submitter deferred that to the worker. A failed probe costs only the
// percentage: the job still runs, with an indeterminate bar.
func resolveTotals(ctx context.Context, spec Spec) ffmpeg.Totals {
	if len(spec.ProbePaths) == 0 || spec.Totals.DurationS > 0 || spec.Totals.Frames > 0 {
		return spec.Totals
	}
	var total ffmpeg.Totals
	for i := 0; i < len(spec.ProbePaths) && i < maxProbePaths; i++ {
		info, err := ffmpeg.Probe(ctx, spec.ProbePaths[i])
		if err != nil {
			return spec.Totals
		}
		total.DurationS += info.DurationS
		total.Frames += info.TotalFrame
	}
	return total
}

// settle maps a run outcome onto the job's terminal state.
func (q *Queue) settle(ctx context.Context, id string, err error) {
	switch {
	case err == nil:
		q.publishProgress(id, ffmpeg.Progress{Fraction: 1, ETASeconds: 0, Done: true})
		q.finish(id, StateSucceeded, "")
	case ctx.Err() != nil:
		q.finish(id, StateCanceled, "canceled")
	default:
		q.finish(id, StateFailed, err.Error())
	}
}

// begin marks a job running, registers its canceller and returns its spec.
func (q *Queue) begin(id string, cancel context.CancelFunc) (Spec, bool) {
	q.mu.Lock()
	job, ok := q.jobs[id]
	if !ok || job.State != StateQueued {
		q.mu.Unlock()
		return Spec{}, false
	}
	job.State = StateRunning
	job.StartedAt = time.Now().UnixMilli()
	q.cancels[id] = cancel
	snapshot := *job
	q.mu.Unlock()

	q.listener(snapshot)
	return snapshot.Spec, true
}

// publishProgress records and forwards a progress sample.
func (q *Queue) publishProgress(id string, p ffmpeg.Progress) {
	q.mu.Lock()
	job, ok := q.jobs[id]
	if !ok || job.State != StateRunning {
		q.mu.Unlock()
		return
	}
	job.Progress = p
	snapshot := *job
	q.mu.Unlock()

	q.listener(snapshot)
}

// finish moves a job to a terminal state and notifies the listener.
func (q *Queue) finish(id string, state State, message string) {
	q.mu.Lock()
	job, ok := q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return
	}
	job.State = state
	job.Error = message
	job.EndedAt = time.Now().UnixMilli()
	if state == StateSucceeded {
		job.Progress.Fraction = 1
		job.Progress.ETASeconds = 0
		job.Progress.Done = true
	}
	snapshot := *job
	q.mu.Unlock()

	q.listener(snapshot)
}

// Cancel stops a running job, or drops it if it has not started.
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	job, ok := q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("jobs: unknown job %q", id)
	}
	if cancel, running := q.cancels[id]; running {
		q.mu.Unlock()
		cancel()
		return nil
	}
	if job.State != StateQueued {
		q.mu.Unlock()
		return fmt.Errorf("jobs: job %q is not cancellable", id)
	}
	// Marked under the lock begin checks, so a worker can't start it meanwhile
	job.State = StateCanceled
	job.Error = "canceled before starting"
	job.EndedAt = time.Now().UnixMilli()
	snapshot := *job
	q.mu.Unlock()

	q.listener(snapshot)
	// It never ran, so nothing else will remove the folder made for it
	if snapshot.WorkDir != "" {
		_ = os.RemoveAll(snapshot.WorkDir)
	}
	return nil
}

// CancelAll stops every queued and running job.
func (q *Queue) CancelAll() {
	q.mu.RLock()
	ids := make([]string, 0, len(q.order))
	for _, id := range q.order {
		if job, ok := q.jobs[id]; ok && (job.State == StateQueued || job.State == StateRunning) {
			ids = append(ids, id)
		}
	}
	q.mu.RUnlock()

	for _, id := range ids {
		// A job that finished between the snapshot and here is not an error.
		_ = q.Cancel(id)
	}
}

// Spec returns the specification a job was submitted with, so a finished job
// can be run again, and whether the job has finished.
func (q *Queue) Spec(id string) (Spec, bool, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	job, ok := q.jobs[id]
	if !ok {
		return Spec{}, false, fmt.Errorf("jobs: unknown job %q", id)
	}
	spec := job.Spec
	spec.Args = append([]string(nil), job.Spec.Args...)
	finished := job.State != StateQueued && job.State != StateRunning
	return spec, finished, nil
}

// ActiveCount returns how many jobs are queued or running.
func (q *Queue) ActiveCount() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	n := 0
	for _, job := range q.jobs {
		if job.State == StateQueued || job.State == StateRunning {
			n++
		}
	}
	return n
}

// List returns every job in submission order.
func (q *Queue) List() []Job {
	q.mu.RLock()
	defer q.mu.RUnlock()

	out := make([]Job, 0, len(q.order))
	for _, id := range q.order {
		if job, ok := q.jobs[id]; ok {
			out = append(out, *job)
		}
	}
	return out
}

// ClearFinished drops every job that has reached a terminal state.
func (q *Queue) ClearFinished() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	kept := make([]string, 0, len(q.order))
	removed := 0
	for _, id := range q.order {
		job, ok := q.jobs[id]
		if !ok {
			continue
		}
		if job.State == StateQueued || job.State == StateRunning {
			kept = append(kept, id)
			continue
		}
		delete(q.jobs, id)
		removed++
	}
	q.order = kept
	return removed
}

// evictOldestFinishedLocked drops the oldest job in a terminal state and
// reports whether one was found. The caller must hold q.mu for writing.
func (q *Queue) evictOldestFinishedLocked() bool {
	for i, id := range q.order {
		job, ok := q.jobs[id]
		if ok && (job.State == StateQueued || job.State == StateRunning) {
			continue
		}
		delete(q.jobs, id)
		q.order = append(q.order[:i], q.order[i+1:]...)
		return true
	}
	return false
}

// Shutdown cancels outstanding work and waits for the workers to exit.
func (q *Queue) Shutdown() {
	q.CancelAll()
	q.stop()
	q.wg.Wait()
}

// progressGate passes samples no more often than its throttle allows, but
// always a final one, and one that starts a new download step or playlist
// entry, since yt-dlp prints those within milliseconds of the last sample
// and the step can then run for minutes
type progressGate struct {
	throttle *throttle
	stage    string
	item     int
}

// allow reports whether p should be published now
func (g *progressGate) allow(p ffmpeg.Progress) bool {
	changed := p.Stage != g.stage || p.Item != g.item
	if !g.throttle.allow(p.Done || changed) {
		return false
	}
	g.stage, g.item = p.Stage, p.Item
	return true
}

// throttle limits how often progress samples reach the UI.
type throttle struct {
	interval time.Duration
	last     time.Time
}

func newThrottle(ms int) *throttle {
	return &throttle{interval: time.Duration(ms) * time.Millisecond}
}

// allow reports whether a sample should be emitted now; terminal samples always pass.
func (t *throttle) allow(force bool) bool {
	now := time.Now()
	if force || now.Sub(t.last) >= t.interval {
		t.last = now
		return true
	}
	return false
}
