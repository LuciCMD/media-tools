//go:build !windows

package ffmpeg

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHuntSetsidHelper starts a child in a session of its own, so it leaves
// the run's process group, sharing the run's pipes, then waits to be killed
func TestHuntSetsidHelper(t *testing.T) {
	pidFile := os.Getenv("MEDIA_TOOLS_SETSID")
	if pidFile == "" {
		return
	}
	child := exec.Command("sleep", "30")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

// TestHuntCancelWithChildOutsideGroup checks Run returns within the grace
// when a child has left the process group but still holds the pipes
func TestHuntCancelWithChildOutsideGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("MEDIA_TOOLS_SETSID", pidFile)
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(pidFile); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		cancel()
	}()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := Run(ctx, Request{Binary: os.Args[0], Args: []string{"-test.run=^TestHuntSetsidHelper$"}}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want context.Canceled", err)
		}
		t.Logf("Run returned after %v", time.Since(start).Round(time.Millisecond))
	case <-time.After(pipeGrace + 15*time.Second):
		t.Fatal("Run never returned with a child outside the group holding its pipes")
	}
}
