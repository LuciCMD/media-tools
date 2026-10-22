package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// huntArgs returns what follows "--" in this process's arguments, where the
// hunt tests put the helper's mode and its parameters
func huntArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

// huntRequest runs this test binary as the hunt helper in the given mode
func huntRequest(mode ...string) Request {
	args := append([]string{"-test.run=^TestHuntHelper$", "--"}, mode...)
	return Request{Binary: os.Args[0], Args: args}
}

// TestHuntHelper is the stand-in process for the hunt tests, doing nothing
// unless MEDIA_TOOLS_HUNT is set
func TestHuntHelper(t *testing.T) {
	if os.Getenv("MEDIA_TOOLS_HUNT") != "1" {
		return
	}
	args := huntArgs()
	if len(args) == 0 {
		os.Exit(3)
	}
	switch args[0] {
	case "beat":
		// beat <file> <count>
		n, _ := strconv.Atoi(args[2])
		for i := 0; i < n; i++ {
			_ = os.WriteFile(args[1], []byte(strconv.Itoa(i)), 0o600)
			time.Sleep(100 * time.Millisecond)
		}
		os.Exit(0)
	case "tree":
		// tree <file> <count>, a child beating on our own pipes
		child := exec.Command(os.Args[0], "-test.run=^TestHuntHelper$", "--", "beat", args[1], args[2])
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = child.Wait()
		os.Exit(0)
	case "orphan":
		// orphan <file>, a child on no pipes left running after a clean exit
		child := exec.Command(os.Args[0], "-test.run=^TestHuntHelper$", "--", "beat", args[1], "30")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(args[1]); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		os.Exit(0)
	case "progress":
		for i := 1; i <= 3; i++ {
			fmt.Printf("frame=%d\nout_time_us=%d\nspeed=2.0x\nprogress=continue\n", i*30, i*1_000_000)
		}
		fmt.Print("progress=end\n")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "Something specific went wrong")
		fmt.Fprintln(os.Stderr, "Conversion failed!")
		os.Exit(7)
	case "sleep":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	os.Exit(4)
}

// beatStopped reports whether a heartbeat file has stopped changing
func beatStopped(path string) (bool, string, string) {
	time.Sleep(300 * time.Millisecond)
	before, _ := os.ReadFile(path)
	time.Sleep(500 * time.Millisecond)
	after, _ := os.ReadFile(path)
	return string(before) == string(after), string(before), string(after)
}

// captureLog collects the standard logger's output for the test's duration
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestHuntProgressStillParsed checks progress still arrives through the pipe
// now the process sits in a tree
func TestHuntProgressStillParsed(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	logs := captureLog(t)
	var got []Progress
	req := huntRequest("progress")
	req.Totals = Totals{DurationS: 3}
	res, err := Run(context.Background(), req, func(p Progress) { got = append(got, p) })
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run failed: %v, exit %d\n%s", err, res.ExitCode, res.Stderr)
	}
	if len(got) != 4 {
		t.Fatalf("got %d progress samples, want 4: %+v", len(got), got)
	}
	if got[1].Fraction < 0.66 || got[1].Fraction > 0.67 || !got[3].Done {
		t.Errorf("progress samples look wrong: %+v", got)
	}
	if strings.Contains(logs.String(), "cancel can only stop") {
		t.Errorf("adopting the tree failed on a plain run: %s", logs.String())
	}
}

// TestHuntFailureStillExplained checks a failed run keeps its exit code and
// reason
func TestHuntFailureStillExplained(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	res, err := Run(context.Background(), huntRequest("fail"), nil)
	if err == nil {
		t.Fatal("a failing run reported success")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("a failing run was reported as cancelled: %v", err)
	}
	if res.ExitCode != 7 || err.Error() != "Something specific went wrong" {
		t.Errorf("got exit %d and %q, want 7 and the specific line", res.ExitCode, err.Error())
	}
}

// TestHuntPreCancelledContext checks a context already cancelled before the
// run starts reports as a cancel and starts nothing
func TestHuntPreCancelledContext(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := Run(ctx, huntRequest("sleep"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("a pre-cancelled run took %v", took)
	}
}

// TestHuntCancelAtEveryMoment cancels runs at delays from 0 to 60 ms, which
// spans before Start, between Start and adoptTree, and after, from several
// goroutines at once, and checks every run ends promptly with no child left
func TestHuntCancelAtEveryMoment(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	logs := captureLog(t)
	dir := t.TempDir()
	const runs = 30
	var wg sync.WaitGroup
	errs := make([]error, runs)
	took := make([]time.Duration, runs)
	beats := make([]string, runs)
	for i := 0; i < runs; i++ {
		beats[i] = filepath.Join(dir, "beat"+strconv.Itoa(i))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			delay := time.Duration(i*2) * time.Millisecond
			var at time.Time
			var atMu sync.Mutex
			go func() {
				time.Sleep(delay)
				atMu.Lock()
				at = time.Now()
				atMu.Unlock()
				// Repeated cancels from several goroutines
				var cw sync.WaitGroup
				for k := 0; k < 4; k++ {
					cw.Add(1)
					go func() { defer cw.Done(); cancel() }()
				}
				cw.Wait()
			}()
			_, errs[i] = Run(ctx, huntRequest("tree", beats[i], "100"), nil)
			atMu.Lock()
			took[i] = time.Since(at)
			atMu.Unlock()
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runs cancelled at once didn't all return within 30 s")
	}
	for i := 0; i < runs; i++ {
		if errs[i] == nil {
			t.Errorf("run %d reported success after a cancel", i)
		}
		if took[i] > 2*time.Second {
			t.Errorf("run %d returned %v after its cancel", i, took[i].Round(time.Millisecond))
		}
	}
	// Any child that escaped would still be beating
	time.Sleep(300 * time.Millisecond)
	snap := map[int]string{}
	for i := range beats {
		b, _ := os.ReadFile(beats[i])
		snap[i] = string(b)
	}
	time.Sleep(600 * time.Millisecond)
	for i := range beats {
		b, _ := os.ReadFile(beats[i])
		if string(b) != snap[i] {
			t.Errorf("run %d left its child running (beat %q then %q)", i, snap[i], b)
		}
	}
	t.Logf("log from the runs:\n%s", logs.String())
}

// TestHuntConcurrentCancelOfEightTrees mirrors Jobs at Once at 8, every run
// with a live child, all cancelled together
func TestHuntConcurrentCancelOfEightTrees(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	dir := t.TempDir()
	const runs = 8
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	beats := make([]string, runs)
	results := make(chan error, runs)
	for i := 0; i < runs; i++ {
		beats[i] = filepath.Join(dir, "beat"+strconv.Itoa(i))
		go func(b string) {
			_, err := Run(ctx, huntRequest("tree", b, "300"), nil)
			results <- err
		}(beats[i])
	}
	deadline := time.Now().Add(20 * time.Second)
	for _, b := range beats {
		for time.Now().Before(deadline) {
			if _, err := os.Stat(b); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	at := time.Now()
	cancel()
	for i := 0; i < runs; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("a run ended with %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("not every run returned after the cancel")
		}
	}
	if took := time.Since(at); took > 2*time.Second {
		t.Errorf("the runs took %v to return after the cancel", took.Round(time.Millisecond))
	}
	for i, b := range beats {
		if ok, before, after := beatStopped(b); !ok {
			t.Errorf("run %d's child still running (beat %s then %s)", i, before, after)
		}
	}
}

// TestHuntNoGoroutineLeak checks Run leaves no goroutine behind, after plain
// runs, failed runs and cancelled ones
func TestHuntNoGoroutineLeak(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	// Warm up anything lazily started
	_, _ = Run(context.Background(), huntRequest("progress"), nil)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	base := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		_, _ = Run(ctx, huntRequest("progress"), nil)
		_, _ = Run(ctx, huntRequest("fail"), nil)
		go func() { time.Sleep(150 * time.Millisecond); cancel() }()
		_, _ = Run(ctx, huntRequest("sleep"), nil)
		cancel()
	}
	// A context cancelled after a run returns must not wake anything either
	ctx, cancel := context.WithCancel(context.Background())
	_, _ = Run(ctx, huntRequest("progress"), nil)
	cancel()

	var now int
	for i := 0; i < 40; i++ {
		now = runtime.NumGoroutine()
		if now <= base {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	n := runtime.Stack(buf, true)
	t.Errorf("goroutines went from %d to %d\n%s", base, now, buf[:n])
}

// TestHuntCloseAfterCancelReturnsOnBothPaths checks the pipe closer exits
// without a cancel and closes the pipes only once the grace has run out
func TestHuntCloseAfterCancelReturnsOnBothPaths(t *testing.T) {
	// No cancel, readers finish
	done := make(chan struct{})
	exited := make(chan struct{})
	c := &countCloser{}
	go func() { closeAfterCancel(context.Background(), done, c); close(exited) }()
	close(done)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("closeAfterCancel didn't return once the readers were done")
	}
	if c.n() != 0 {
		t.Error("the pipes were closed without a cancel")
	}

	// Cancel, readers stuck
	ctx, cancel := context.WithCancel(context.Background())
	stuck := make(chan struct{})
	exited = make(chan struct{})
	c = &countCloser{}
	start := time.Now()
	go func() { closeAfterCancel(ctx, stuck, c, c); close(exited) }()
	cancel()
	select {
	case <-exited:
	case <-time.After(pipeGrace + 2*time.Second):
		t.Fatal("closeAfterCancel didn't give up on stuck readers")
	}
	if took := time.Since(start); took < pipeGrace-100*time.Millisecond {
		t.Errorf("the pipes were closed after %v, before the grace", took)
	}
	if c.n() != 2 {
		t.Errorf("closed %d pipes, want 2", c.n())
	}
}

type countCloser struct {
	mu    sync.Mutex
	count int
}

func (c *countCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	return nil
}

func (c *countCloser) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// TestHuntOrphanAfterCleanExit records what happens to a child left running
// by a command that exits cleanly: the job's close ends it at release, where
// before this change it carried on
func TestHuntOrphanAfterCleanExit(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	beat := filepath.Join(t.TempDir(), "beat")
	res, err := Run(context.Background(), huntRequest("orphan", beat), nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("the run failed: %v, exit %d", err, res.ExitCode)
	}
	stopped, before, after := beatStopped(beat)
	t.Logf("orphaned child stopped at release: %v (beat %s then %s, OS %s)", stopped, before, after, runtime.GOOS)
	if !stopped {
		// Only the process group path leaves it, nothing on Windows should
		if runtime.GOOS == "windows" {
			t.Error("the orphan outlived the job's release on Windows")
		}
		// Wait for it to finish its 30 beats, so it doesn't outlive the test dir
		time.Sleep(3 * time.Second)
	}
}
