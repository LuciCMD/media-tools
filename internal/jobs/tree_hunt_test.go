package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestJobsHuntHelper stands in for yt-dlp in the queue's tree tests, doing
// nothing unless MEDIA_TOOLS_JOBS_HUNT is set
func TestJobsHuntHelper(t *testing.T) {
	if os.Getenv("MEDIA_TOOLS_JOBS_HUNT") != "1" {
		return
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(3)
	}
	switch args[0] {
	case "beat":
		for i := 0; i < 300; i++ {
			_ = os.WriteFile(args[1], []byte(strconv.Itoa(i)), 0o600)
			time.Sleep(100 * time.Millisecond)
		}
		os.Exit(0)
	case "gate":
		// gate <flag> <beat>, quick once flag exists, else a beating child on our pipes
		if _, err := os.Stat(args[1]); err == nil {
			os.Exit(0)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestJobsHuntHelper$", "--", "beat", args[2])
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = child.Wait()
		os.Exit(0)
	case "mark":
		_ = os.WriteFile(args[1], []byte("ran"), 0o600)
		os.Exit(0)
	}
	os.Exit(4)
}

// huntArgsFor builds the helper's argument vector for one pass
func huntArgsFor(mode ...string) []string {
	return append([]string{"-test.run=^TestJobsHuntHelper$", "--"}, mode...)
}

// stateOf returns a job's current state
func stateOf(q *Queue, id string) State {
	for _, j := range q.List() {
		if j.ID == id {
			return j.State
		}
	}
	return ""
}

// waitFor polls until cond holds or the timeout passes
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return cond()
}

// beatStill reports whether a heartbeat file is still changing
func beatStill(path string) bool {
	time.Sleep(300 * time.Millisecond)
	before, _ := os.ReadFile(path)
	time.Sleep(500 * time.Millisecond)
	after, _ := os.ReadFile(path)
	return string(before) != string(after)
}

// TestHuntQueueCancelTreeThenRetry cancels a two-pass job while its first
// pass has a child running, checks it reports Canceled, the child is gone and
// the second pass never ran, then retries the same spec to success
func TestHuntQueueCancelTreeThenRetry(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_JOBS_HUNT", "1")
	dir := t.TempDir()
	flag, beat, mark := filepath.Join(dir, "flag"), filepath.Join(dir, "beat"), filepath.Join(dir, "mark")

	q, err := NewQueue(2, func(Job) {})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Shutdown()

	spec := Spec{Tool: "t", Binary: os.Args[0], Output: mark,
		Args:   huntArgsFor("gate", flag, beat),
		Passes: [][]string{huntArgsFor("mark", mark)}}
	id, err := q.Submit(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(20*time.Second, func() bool { _, err := os.Stat(beat); return err == nil }) {
		t.Fatal("the first pass's child never started")
	}
	at := time.Now()
	if err := q.Cancel(id); err != nil {
		t.Fatal(err)
	}
	// Repeated clicks on Cancel
	_ = q.Cancel(id)
	_ = q.Cancel(id)
	if !waitFor(10*time.Second, func() bool { return stateOf(q, id) == StateCanceled }) {
		t.Fatalf("the job ended %q, want canceled", stateOf(q, id))
	}
	if took := time.Since(at); took > 2*time.Second {
		t.Errorf("the job took %v to report canceled", took.Round(time.Millisecond))
	}
	if beatStill(beat) {
		t.Error("the first pass's child outlived the cancel")
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("the second pass ran after the job was cancelled")
	}

	// Retry, the way RetryJob does it
	again, finished, err := q.Spec(id)
	if err != nil || !finished {
		t.Fatalf("Spec: %v, finished %v", err, finished)
	}
	again.ID = ""
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	id2, err := q.Submit(again)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(20*time.Second, func() bool {
		s := stateOf(q, id2)
		return s == StateSucceeded || s == StateFailed || s == StateCanceled
	}) {
		t.Fatal("the retried job never finished")
	}
	if s := stateOf(q, id2); s != StateSucceeded {
		t.Errorf("the retried job ended %q, want succeeded", s)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Error("the retried job's second pass didn't run")
	}
}

// TestHuntQueueCancelAllEightTrees runs eight jobs at once, each with a live
// child, and checks Cancel All ends every one as Canceled with no child left
func TestHuntQueueCancelAllEightTrees(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_JOBS_HUNT", "1")
	dir := t.TempDir()
	q, err := NewQueue(8, func(Job) {})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Shutdown()

	ids := make([]string, 8)
	beats := make([]string, 8)
	for i := range ids {
		beats[i] = filepath.Join(dir, "beat"+strconv.Itoa(i))
		ids[i], err = q.Submit(Spec{Tool: "t", Binary: os.Args[0], Output: beats[i],
			Args: huntArgsFor("gate", filepath.Join(dir, "never"), beats[i])})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range beats {
		if !waitFor(20*time.Second, func() bool { _, err := os.Stat(b); return err == nil }) {
			t.Fatal("a job's child never started")
		}
	}
	at := time.Now()
	q.CancelAll()
	for _, id := range ids {
		if !waitFor(10*time.Second, func() bool { return stateOf(q, id) == StateCanceled }) {
			t.Errorf("job %s ended %q, want canceled", id, stateOf(q, id))
		}
	}
	if took := time.Since(at); took > 3*time.Second {
		t.Errorf("Cancel All took %v", took.Round(time.Millisecond))
	}
	var wg sync.WaitGroup
	for i, b := range beats {
		wg.Add(1)
		go func(i int, b string) {
			defer wg.Done()
			if beatStill(b) {
				t.Errorf("job %d's child outlived Cancel All", i)
			}
		}(i, b)
	}
	wg.Wait()
}
