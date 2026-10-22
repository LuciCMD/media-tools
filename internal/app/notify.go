package app

import (
	"fmt"
	"sync"
	"time"

	"media-tools/internal/jobs"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// notifyAfter is how long a batch must run before its end is worth a
// notification. Anything quicker finishes while the window is still in view.
const notifyAfter = 15 * time.Second

// notifier tracks the current batch, from the queue leaving idle until it is
// idle again, so one notification covers every job in it.
type notifier struct {
	mu       sync.Mutex
	ready    bool // notifications were set up successfully
	started  time.Time
	finished int
	failed   int
}

// enableNotifications sets up desktop notifications. On Windows this
// registers the app under HKCU\Software\Classes so its notifications can be
// shown, which is why it happens only once the user turns them on.
func (a *App) enableNotifications() {
	if a.ctx == nil {
		return
	}
	if err := runtime.InitializeNotifications(a.ctx); err != nil {
		runtime.LogWarning(a.ctx, "notifications unavailable: "+err.Error())
		return
	}
	// macOS asks the user once; Windows and Linux always allow it.
	if ok, err := runtime.RequestNotificationAuthorization(a.ctx); err != nil || !ok {
		return
	}
	a.notes.mu.Lock()
	a.notes.ready = true
	a.notes.mu.Unlock()
}

// noteJob follows each job's state, and when the queue empties after a batch
// long enough to matter, says how it went.
func (a *App) noteJob(job jobs.Job) {
	// Counting the queue is only needed when a job ends, not on every one of
	// the several progress updates a second each running job sends.
	active := 0
	if job.State != jobs.StateQueued && job.State != jobs.StateRunning {
		active = a.activeJobs()
	}
	message, send := a.notes.observe(job, active, time.Now())
	if !send || !a.store.Get().Notifications || a.ctx == nil {
		return
	}
	// A notification that can't be shown is not worth interrupting anyone for.
	_ = runtime.SendNotification(a.ctx, runtime.NotificationOptions{
		ID:    fmt.Sprintf("batch-%d", time.Now().UnixNano()),
		Title: "Media Tools",
		Body:  message,
	})
}

// activeJobs counts queued and running jobs.
func (a *App) activeJobs() int {
	if a.queue == nil {
		return 0
	}
	return a.queue.ActiveCount()
}

// observe records one job update and returns the message to send, if this
// update ends a batch that ran for at least notifyAfter.
func (n *notifier) observe(job jobs.Job, active int, now time.Time) (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	switch job.State {
	case jobs.StateQueued, jobs.StateRunning:
		if n.started.IsZero() {
			n.started = now
		}
		return "", false
	case jobs.StateSucceeded:
		n.finished++
	case jobs.StateFailed:
		n.finished++
		n.failed++
	default:
		// A cancelled job was stopped on purpose; it isn't news.
	}
	if active > 0 || n.started.IsZero() {
		return "", false
	}

	long := now.Sub(n.started) >= notifyAfter
	message := batchMessage(n.finished, n.failed)
	n.started, n.finished, n.failed = time.Time{}, 0, 0
	return message, long && n.ready && message != ""
}

// batchMessage says how a batch went in one sentence.
func batchMessage(finished, failed int) string {
	done := finished - failed
	switch {
	case finished == 0:
		return ""
	case failed == 0 && done == 1:
		return "Your job has finished."
	case failed == 0:
		return fmt.Sprintf("All %d jobs have finished.", done)
	case done == 0 && failed == 1:
		return "Your job failed. Open Media Tools to see why."
	case done == 0:
		return fmt.Sprintf("All %d jobs failed. Open Media Tools to see why.", failed)
	}
	if done == 1 {
		return fmt.Sprintf("1 job finished and %d failed.", failed)
	}
	return fmt.Sprintf("%d jobs finished and %d failed.", done, failed)
}
