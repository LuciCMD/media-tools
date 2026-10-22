//go:build windows

package ffmpeg

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// handleCount is how many kernel handles this process holds
func handleCount(t *testing.T) uint32 {
	t.Helper()
	var n uint32
	r, _, err := procGetProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		t.Fatalf("GetProcessHandleCount: %v", err)
	}
	return n
}

var procGetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// TestHuntNoHandleLeak runs many plain, failed and cancelled runs, eight at a
// time like Jobs at Once at its top, and checks the handle count settles
// back, so no job object or process handle is left open per run
func TestHuntNoHandleLeak(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	_, _ = Run(context.Background(), huntRequest("progress"), nil)
	time.Sleep(200 * time.Millisecond)
	base := handleCount(t)

	const rounds, width = 8, 8
	for r := 0; r < rounds; r++ {
		var wg sync.WaitGroup
		for w := 0; w < width; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				switch w % 3 {
				case 0:
					_, _ = Run(context.Background(), huntRequest("progress"), nil)
				case 1:
					_, _ = Run(context.Background(), huntRequest("fail"), nil)
				default:
					ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
					_, _ = Run(ctx, huntRequest("sleep"), nil)
					cancel()
				}
			}(w)
		}
		wg.Wait()
	}
	time.Sleep(300 * time.Millisecond)
	after := handleCount(t)
	// 64 runs, so a leak of even one handle per run would show as 64
	if after > base+16 {
		t.Errorf("handles went from %d to %d over %d runs", base, after, rounds*width)
	}
	t.Logf("handles before %d, after %d", base, after)
}

// TestHuntAdoptAfterKill is the cancel that lands between Start and
// adoptTree, Process.Kill having already run, so adopting must fail cleanly
// or give a tree whose kill and release still work
func TestHuntAdoptAfterKill(t *testing.T) {
	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	cmd := exec.Command(os.Args[0], huntRequest("sleep").Args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	tree, err := adoptTree(cmd)
	t.Logf("adoptTree on a killed, unreaped process: tree %v, err %v", tree != nil, err)
	if err == nil {
		if kerr := tree.kill(); kerr != nil {
			t.Logf("kill on that tree: %v, Run would fall back to Process.Kill", kerr)
		}
		tree.release()
	}
	if werr := cmd.Wait(); werr == nil {
		t.Error("a killed process reported a clean exit")
	}
}

// TestHuntEscapeHelper is the stand-in for an escaped child: as "holder" it
// only waits, and as "escaper" it hands its own output pipes to the holder,
// which sits outside the run's job, then waits to be killed
func TestHuntEscapeHelper(t *testing.T) {
	switch os.Getenv("MEDIA_TOOLS_ESCAPE") {
	case "holder":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "escaper":
		pid, _ := strconv.Atoi(os.Getenv("MEDIA_TOOLS_HOLDER"))
		target, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE, false, uint32(pid))
		if err != nil {
			os.Exit(2)
		}
		for _, f := range []*os.File{os.Stdout, os.Stderr} {
			var dup windows.Handle
			if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(f.Fd()), target, &dup,
				0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
				os.Exit(3)
			}
		}
		_ = os.WriteFile(os.Getenv("MEDIA_TOOLS_READY"), []byte("1"), 0o600)
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
}

// TestHuntCancelWithEscapedPipe checks Run still returns, within the grace,
// when something outside the job holds the run's pipes open, which is the
// case closeAfterCancel exists for
func TestHuntCancelWithEscapedPipe(t *testing.T) {
	holder := exec.Command(os.Args[0], "-test.run=^TestHuntEscapeHelper$")
	holder.Env = append(os.Environ(), "MEDIA_TOOLS_ESCAPE=holder")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = holder.Process.Kill()
		_ = holder.Wait()
	})

	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("MEDIA_TOOLS_ESCAPE", "escaper")
	t.Setenv("MEDIA_TOOLS_HOLDER", strconv.Itoa(holder.Process.Pid))
	t.Setenv("MEDIA_TOOLS_READY", ready)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var at time.Time
	var atMu sync.Mutex
	go func() {
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		atMu.Lock()
		at = time.Now()
		atMu.Unlock()
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Request{Binary: os.Args[0], Args: []string{"-test.run=^TestHuntEscapeHelper$"}}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		atMu.Lock()
		took := time.Since(at)
		atMu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want context.Canceled", err)
		}
		if took > pipeGrace+2*time.Second {
			t.Errorf("Run returned %v after the cancel, want about the %v grace", took.Round(time.Millisecond), pipeGrace)
		}
		t.Logf("Run returned %v after the cancel", took.Round(time.Millisecond))
	case <-time.After(pipeGrace + 15*time.Second):
		t.Fatal("Run never returned with its pipes held outside the job")
	}
}
