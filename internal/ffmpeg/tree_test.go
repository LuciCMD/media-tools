package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestHelperProcess stands in for yt-dlp in the tree tests: as "parent" it
// starts a "child" sharing its output, as yt-dlp starts ffmpeg, and the
// child writes a heartbeat file until it's killed
// Run normally, with neither set, it does nothing
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("MEDIA_TOOLS_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		child.Env = append(os.Environ(), "MEDIA_TOOLS_HELPER=child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = child.Wait()
		os.Exit(0)
	case "child":
		beat := os.Getenv("MEDIA_TOOLS_BEAT")
		beats, err := strconv.Atoi(os.Getenv("MEDIA_TOOLS_BEATS"))
		if err != nil {
			beats = 200
		}
		for i := 0; i < beats; i++ {
			_ = os.WriteFile(beat, []byte(strconv.Itoa(i)), 0o600)
			time.Sleep(100 * time.Millisecond)
		}
		os.Exit(0)
	}
}

// TestCancelEndsTheWholeTree checks a cancel ends a command's child too and
// Run returns promptly, where it used to wait for the child to finish
func TestCancelEndsTheWholeTree(t *testing.T) {
	beat := filepath.Join(t.TempDir(), "beat")
	t.Setenv("MEDIA_TOOLS_HELPER", "parent")
	t.Setenv("MEDIA_TOOLS_BEAT", beat)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan time.Time, 1)
	go func() {
		// Cancel once the child is beating, so it really exists
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(beat); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		cancelled <- time.Now()
		cancel()
	}()

	_, err := Run(ctx, Request{Binary: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$"}}, nil)
	if err == nil {
		t.Fatal("a cancelled run reported success")
	}
	if took := time.Since(<-cancelled); took > 2*time.Second {
		t.Errorf("Run returned %v after the cancel, want under 2 s", took.Round(time.Millisecond))
	}

	// A live child rewrites the heartbeat every 100 ms
	time.Sleep(300 * time.Millisecond)
	before, _ := os.ReadFile(beat)
	time.Sleep(500 * time.Millisecond)
	after, _ := os.ReadFile(beat)
	if string(before) != string(after) {
		t.Errorf("the child is still running after the cancel (heartbeat %s then %s)", before, after)
	}
}

// TestRunWithoutCancelStillWaitsForTheTree checks a run left alone still
// lets its child finish, now that both sit in one tree
func TestRunWithoutCancelStillWaitsForTheTree(t *testing.T) {
	beat := filepath.Join(t.TempDir(), "beat")
	t.Setenv("MEDIA_TOOLS_HELPER", "parent")
	t.Setenv("MEDIA_TOOLS_BEAT", beat)
	t.Setenv("MEDIA_TOOLS_BEATS", "5")
	res, err := Run(context.Background(), Request{Binary: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$"}}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("a plain run failed: %v, exit %d\n%s", err, res.ExitCode, res.Stderr)
	}
	if got, _ := os.ReadFile(beat); string(got) != "4" {
		t.Errorf("the child stopped at heartbeat %q, want all 5 (0 to 4)", got)
	}
}

// TestCancelClosesPipesAStrayChildHolds checks a reader blocked on a pipe
// some escaped child still holds open is released once the grace has passed
func TestCancelClosesPipesAStrayChildHolds(t *testing.T) {
	old := pipeGrace
	pipeGrace = 200 * time.Millisecond
	defer func() { pipeGrace = old }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() // the stray child's end, never closed while the test waits

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	readersDone := make(chan struct{})
	read := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 1))
		read <- err
	}()
	go closeAfterCancel(ctx, readersDone, r)

	select {
	case err := <-read:
		if err == nil {
			t.Error("the blocked read returned data instead of ending")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the blocked read was never released")
	}
	close(readersDone)
}
