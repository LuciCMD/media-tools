package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Bounds on a supervised run (Power of Ten rule 2).
const (
	maxStderrLines = 400
	maxArgs        = 128
)

// Request describes one external command to supervise.
type Request struct {
	Binary string   // absolute path to ffmpeg or yt-dlp
	Args   []string // fully-formed argument vector, never shell-interpreted
	Totals Totals   // known extent of the work, for percentage derivation
}

// Result reports how a supervised run finished.
type Result struct {
	ExitCode int
	Stderr   string
}

// Run executes req, streaming progress to onProgress until the process exits.
//
// The argument vector is passed directly to the OS with no shell involved, so
// paths containing spaces or shell metacharacters cannot alter the command.
func Run(ctx context.Context, req Request, onProgress func(Progress)) (Result, error) {
	if err := validateRequest(req); err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, req.Binary, req.Args...)
	hideWindow(cmd)
	prepareTree(cmd)

	// A cancel ends the command's whole tree, since yt-dlp's own child and
	// the ffmpeg it runs would otherwise keep going and hold the pipes open
	var treeMu sync.Mutex
	var tree *processTree
	cmd.Cancel = func() error {
		treeMu.Lock()
		t := tree
		treeMu.Unlock()
		if t != nil {
			err := t.kill()
			if err == nil {
				return nil
			}
			log.Printf("run: %s: couldn't stop what it started, stopping only the process itself: %v", req.Binary, err)
		}
		return cmd.Process.Kill()
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("run: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("run: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("run: start %s: %w", req.Binary, err)
	}
	// A cancel before this point has already ended the process, so the
	// tree can't be adopted and there's nothing left to warn about
	if t, err := adoptTree(cmd); err != nil && ctx.Err() == nil {
		log.Printf("run: %s: cancel can only stop the process itself, not what it starts: %v", req.Binary, err)
	} else if err == nil {
		treeMu.Lock()
		tree = t
		treeMu.Unlock()
		defer t.release()
	}

	var wg sync.WaitGroup
	var tail string

	wg.Add(2)
	go func() {
		defer wg.Done()
		// Progress parse failures must not abort an otherwise healthy encode.
		_ = ParseProgress(stdout, req.Totals, safeEmit(onProgress))
	}()
	go func() {
		defer wg.Done()
		tail = collectTail(stderr)
	}()
	readersDone := make(chan struct{})
	go closeAfterCancel(ctx, readersDone, stdout, stderr)
	wg.Wait()
	close(readersDone)

	return finishRun(ctx, cmd, tail)
}

// pipeGrace is how long a cancelled run waits for its pipes to close by
// themselves before closing them, should a child have escaped the kill
// A variable only so a test can shorten it
var pipeGrace = 3 * time.Second

// closeAfterCancel closes the pipes once ctx is cancelled and pipeGrace has
// passed with the readers still waiting, so Run can't hang on a stray child
func closeAfterCancel(ctx context.Context, readersDone <-chan struct{}, pipes ...io.Closer) {
	select {
	case <-readersDone:
		return
	case <-ctx.Done():
	}
	timer := time.NewTimer(pipeGrace)
	defer timer.Stop()
	select {
	case <-readersDone:
	case <-timer.C:
		for _, p := range pipes {
			_ = p.Close()
		}
	}
}

// finishRun waits on the process and maps its exit into a Result.
func finishRun(ctx context.Context, cmd *exec.Cmd, tail string) (Result, error) {
	err := cmd.Wait()
	if err == nil {
		return Result{ExitCode: 0, Stderr: tail}, nil
	}
	if ctx.Err() != nil {
		return Result{ExitCode: -1, Stderr: tail}, ctx.Err()
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return Result{ExitCode: exitErr.ExitCode(), Stderr: tail},
			errors.New(failureReason(tail))
	}
	return Result{ExitCode: -1, Stderr: tail}, err
}

// validateRequest checks the request before any process is spawned.
func validateRequest(req Request) error {
	if strings.TrimSpace(req.Binary) == "" {
		return errors.New("run: no binary specified")
	}
	if len(req.Args) == 0 {
		return errors.New("run: empty argument vector")
	}
	if len(req.Args) > maxArgs {
		return fmt.Errorf("run: argument vector exceeds %d entries", maxArgs)
	}
	return nil
}

// safeEmit guards against a nil progress callback.
func safeEmit(fn func(Progress)) func(Progress) {
	if fn != nil {
		return fn
	}
	return func(Progress) {}
}

// collectTail keeps the last maxStderrLines of stderr for diagnostics.
func collectTail(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), maxLineBytes)

	ring := make([]string, 0, maxStderrLines)
	for scanner.Scan() {
		line := scanner.Text()
		if len(ring) == maxStderrLines {
			copy(ring, ring[1:])
			ring = ring[:maxStderrLines-1]
		}
		ring = append(ring, line)
	}
	return strings.Join(ring, "\n")
}

// ProgressArgs returns the flags that make ffmpeg emit machine-readable
// progress on stdout, leaving stderr free for diagnostics.
func ProgressArgs() []string {
	return []string{"-progress", "pipe:1", "-nostats", "-nostdin", "-hide_banner", "-loglevel", "error"}
}
