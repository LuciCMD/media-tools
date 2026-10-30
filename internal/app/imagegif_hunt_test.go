package app

import (
	"fmt"
	"image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"media-tools/internal/jobs"
)

// huntGIFSources writes count copies of a large still, so each command runs
// long enough to be cancelled part way
func huntGIFSources(t *testing.T, bin string, count int) []string {
	t.Helper()
	src := t.TempDir()
	first := filepath.Join(src, "base.png")
	made, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "testsrc2=s=1920x1080", "-frames:v", "1", "-y", first).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, made)
	}
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, count)
	for i := range paths {
		paths[i] = filepath.Join(src, fmt.Sprintf("shot %d.png", i+1))
		if err := os.WriteFile(paths[i], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

// huntAwaitPass waits until job id runs command pass or later
func huntAwaitPass(t *testing.T, a *App, id string, pass int) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		for _, j := range a.queue.List() {
			if j.ID != id {
				continue
			}
			if j.State != jobs.StateQueued && j.State != jobs.StateRunning {
				return false
			}
			if j.State == jobs.StateRunning && j.Progress.Pass >= pass {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func huntLeftovers(t *testing.T, dir string, keep string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		if e.Name() != keep {
			left = append(left, e.Name())
		}
	}
	return left
}

// TestHuntImagesToGIFCancelThenRetry cancels in each command, checks nothing
// is left beside the output, then retries and checks the GIF arrives whole
func TestHuntImagesToGIFCancelThenRetry(t *testing.T) {
	for _, pass := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("pass %d", pass), func(t *testing.T) {
			a, ended := gifApp(t)
			paths := huntGIFSources(t, a.bins.FFmpeg, 60)
			out := t.TempDir()
			ids, err := a.Run(RunRequest{ToolID: "image.gif", Paths: paths, OutputDir: out,
				Options: map[string]string{"sizing": "original"}})
			if err != nil {
				t.Fatal(err)
			}
			if !huntAwaitPass(t, a, ids[0], pass) {
				t.Skipf("the job ended before command %d could be cancelled", pass)
			}
			if err := a.CancelJob(ids[0]); err != nil {
				t.Fatal(err)
			}
			j := awaitEnd(t, ended)
			if j.State != jobs.StateCanceled {
				t.Skipf("the job %s before the cancel reached it", j.State)
			}
			if left := huntLeftovers(t, out, ""); len(left) > 0 {
				t.Errorf("cancelled in command %d, these were left: %v", pass, left)
			}
			newID, err := a.RetryJob(ids[0])
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			r := awaitEnd(t, ended)
			if r.ID != newID || r.State != jobs.StateSucceeded {
				t.Fatalf("the retry %s: %s", r.State, r.Error)
			}
			name := "shot 1.gif"
			f, err := os.Open(filepath.Join(out, name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			g, err := gif.DecodeAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Image) != len(paths) {
				t.Errorf("%d frames, want %d", len(g.Image), len(paths))
			}
			for i, d := range g.Disposal {
				if d != gif.DisposalBackground {
					t.Errorf("after the retry frame %d has disposal %d", i, d)
					break
				}
			}
			if left := huntLeftovers(t, out, name); len(left) > 0 {
				t.Errorf("after the retry these were left: %v", left)
			}
		})
	}
}

// TestHuntImagesToGIFTwoJobsSameOutput runs two GIFs whose first images share
// a name in different folders into one output folder at once, so their
// working files would collide if both used the same stem
func TestHuntImagesToGIFTwoJobsSameOutput(t *testing.T) {
	a, ended := gifApp(t)
	one := huntGIFSources(t, a.bins.FFmpeg, 4)
	two := huntGIFSources(t, a.bins.FFmpeg, 6)
	out := t.TempDir()
	for _, p := range [][]string{one, two} {
		if _, err := a.Run(RunRequest{ToolID: "image.gif", Paths: p, OutputDir: out,
			Options: map[string]string{"width": "320"}}); err != nil {
			t.Fatal(err)
		}
	}
	counts := map[int]bool{}
	for i := 0; i < 2; i++ {
		if j := awaitEnd(t, ended); j.State != jobs.StateSucceeded {
			t.Fatalf("a job %s: %s", j.State, j.Error)
		}
	}
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		f, err := os.Open(filepath.Join(out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		g, err := gif.DecodeAll(f)
		f.Close()
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		counts[len(g.Image)] = true
	}
	if len(entries) != 2 || !counts[4] || !counts[6] {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the folder holds %v with frame counts %v, want two GIFs of 4 and 6", names, counts)
	}
}
