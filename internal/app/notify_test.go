package app

import (
	"path/filepath"
	"testing"
	"time"

	"media-tools/internal/jobs"
	"media-tools/internal/tools"
)

// job makes a queue update in the given state.
func job(state jobs.State) jobs.Job {
	return jobs.Job{State: state}
}

// TestNotifierAnnouncesLongBatchesOnce checks one message covers a whole
// batch, sent when the last job ends, and only if the batch ran long enough.
func TestNotifierAnnouncesLongBatchesOnce(t *testing.T) {
	n := notifier{ready: true}
	start := time.Now()

	n.observe(job(jobs.StateQueued), 2, start)
	n.observe(job(jobs.StateRunning), 2, start)
	if _, send := n.observe(job(jobs.StateSucceeded), 1, start.Add(time.Minute)); send {
		t.Error("a notification was sent while a job was still active")
	}
	message, send := n.observe(job(jobs.StateFailed), 0, start.Add(2*time.Minute))
	if !send || message != "1 job finished and 1 failed." {
		t.Errorf("end of batch = %q, %v; want one message about both jobs", message, send)
	}

	// The next batch starts afresh and, being quick, stays quiet.
	later := start.Add(time.Hour)
	n.observe(job(jobs.StateRunning), 1, later)
	if _, send := n.observe(job(jobs.StateSucceeded), 0, later.Add(time.Second)); send {
		t.Error("a batch shorter than notifyAfter was announced")
	}
}

// TestNotifierStaysQuietUntilReady checks nothing is sent before
// notifications were set up, and that cancelled jobs aren't news.
func TestNotifierStaysQuietUntilReady(t *testing.T) {
	n := notifier{}
	start := time.Now()
	n.observe(job(jobs.StateRunning), 1, start)
	if _, send := n.observe(job(jobs.StateSucceeded), 0, start.Add(time.Minute)); send {
		t.Error("a notification was sent before they were set up")
	}

	n.ready = true
	n.observe(job(jobs.StateRunning), 1, start)
	if _, send := n.observe(job(jobs.StateCanceled), 0, start.Add(time.Minute)); send {
		t.Error("a batch that was only cancelled was announced")
	}
}

// TestBatchMessageReadsNaturally checks every mix of outcomes gives a
// sentence.
func TestBatchMessageReadsNaturally(t *testing.T) {
	cases := []struct {
		finished, failed int
		want             string
	}{
		{0, 0, ""},
		{1, 0, "Your job has finished."},
		{3, 0, "All 3 jobs have finished."},
		{1, 1, "Your job failed. Open Media Tools to see why."},
		{2, 2, "All 2 jobs failed. Open Media Tools to see why."},
		{5, 2, "3 jobs finished and 2 failed."},
	}
	for _, c := range cases {
		if got := batchMessage(c.finished, c.failed); got != c.want {
			t.Errorf("batchMessage(%d, %d) = %q, want %q", c.finished, c.failed, got, c.want)
		}
	}
}

// TestOutputNameNeverOverwritesTheInput checks a conversion that keeps the
// name, folder and type gets a different file.
func TestOutputNameNeverOverwritesTheInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "Clip.MP4")
	if got := outputName(dir, "clip", "mp4", input); got == input || filepath.Base(got) != "clip (converted).mp4" {
		t.Errorf("same-name output = %s, want clip (converted).mp4", filepath.Base(got))
	}
	if got := outputName(dir, "clip", "mkv", input); filepath.Base(got) != "clip.mkv" {
		t.Errorf("a different type = %s, want clip.mkv", filepath.Base(got))
	}
}

// TestJobLabelNamesTheFiles checks a job over several files says how many.
func TestJobLabelNamesTheFiles(t *testing.T) {
	tool := tools.Tool{Name: "Join Clips"}
	if got := jobLabel(tool, []string{"a.mp4"}); got != "Join Clips - a.mp4" {
		t.Errorf("one file = %q", got)
	}
	if got := jobLabel(tool, []string{"a.mp4", "b.mp4", "c.mp4"}); got != "Join Clips - a.mp4 and 2 more" {
		t.Errorf("three files = %q", got)
	}
}
