package app

import (
	"fmt"
	"image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
	"media-tools/internal/jobs"
	"media-tools/internal/settings"
	"media-tools/internal/tools"
)

// gifApp is an App with its settings in a temporary folder, ffmpeg found
// without detection, and a queue whose ended jobs arrive on the channel
func gifApp(t *testing.T) (*App, chan jobs.Job) {
	t.Helper()
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg is not installed")
	}
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	store, err := settings.NewStore(appName)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store, claimed: make(map[string]bool, 8)}
	a.probed.Do(func() { a.bins = bins })
	ended := make(chan jobs.Job, 8)
	q, err := jobs.NewQueue(1, func(j jobs.Job) {
		a.emitJob(j)
		if j.State != jobs.StateQueued && j.State != jobs.StateRunning {
			ended <- j
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	a.queue = q
	q.Start()
	t.Cleanup(q.Shutdown)
	return a, ended
}

func awaitEnd(t *testing.T, ended chan jobs.Job) jobs.Job {
	t.Helper()
	select {
	case j := <-ended:
		return j
	case <-time.After(3 * time.Minute):
		t.Fatal("the job never ended")
	}
	return jobs.Job{}
}

// TestImagesToGIFThroughRun makes a GIF from more images than the worker
// would probe, through Run as the page calls it, and checks every frame is
// marked to clear and nothing is left beside it
func TestImagesToGIFThroughRun(t *testing.T) {
	a, ended := gifApp(t)
	src, out := t.TempDir(), t.TempDir()
	wide, narrow := filepath.Join(src, "wide.png"), filepath.Join(src, "narrow.png")
	for path, spec := range map[string]string{wide: "color=c=red:s=64x48", narrow: "color=c=green:s=24x48"} {
		made, err := exec.Command(a.bins.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
			"-i", spec, "-frames:v", "1", "-y", path).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, made)
		}
	}
	const count = 130
	paths := make([]string, count)
	for i := range paths {
		from := wide
		if i%2 == 1 {
			from = narrow
		}
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		paths[i] = filepath.Join(src, fmt.Sprintf("f%d.png", i+1))
		if err := os.WriteFile(paths[i], data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := a.Run(RunRequest{ToolID: "image.gif", Paths: paths, OutputDir: out,
		Options: map[string]string{"sizing": "original", "fps": "25"}}); err != nil {
		t.Fatal(err)
	}
	if j := awaitEnd(t, ended); j.State != jobs.StateSucceeded {
		t.Fatalf("the job %s: %s", j.State, j.Error)
	}

	f, err := os.Open(filepath.Join(out, "f1.gif"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != count || g.Config.Width != 64 || g.Config.Height != 48 {
		t.Errorf("%d frames at %d×%d, want %d at 64×48", len(g.Image), g.Config.Width, g.Config.Height, count)
	}
	for i, d := range g.Disposal {
		if d != gif.DisposalBackground {
			t.Fatalf("frame %d has disposal %d, want it cleared", i, d)
		}
	}
	left, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range left {
		if e.Name() != "f1.gif" {
			t.Errorf("%s was left beside the GIF", e.Name())
		}
	}
}

// TestImagesToGIFFailsOnAnUnreadableFirstImage checks the job names the image
// whose size it couldn't read rather than guessing a shape
func TestImagesToGIFFailsOnAnUnreadableFirstImage(t *testing.T) {
	a, ended := gifApp(t)
	src, out := t.TempDir(), t.TempDir()
	junk, other := filepath.Join(src, "junk.png"), filepath.Join(src, "other.png")
	for _, p := range []string{junk, other} {
		if err := os.WriteFile(p, []byte("not an image"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Run(RunRequest{ToolID: "image.gif", Paths: []string{junk, other}, OutputDir: out}); err != nil {
		t.Fatal(err)
	}
	j := awaitEnd(t, ended)
	if j.State != jobs.StateFailed || !strings.Contains(j.Error, "junk.png") {
		t.Errorf("the job %s with %q, want it failed naming junk.png", j.State, j.Error)
	}
}

// TestCommandsOfFixesOnlyAStagedGIF checks the disposal is written only where
// the result waits to be moved
func TestCommandsOfFixesOnlyAStagedGIF(t *testing.T) {
	if commandsOf(tools.Plan{Args: []string{"-i", "x"}, Disposal: tools.DisposeClear}).Fixup != nil {
		t.Error("a fix-up was set with nothing staged")
	}
	if commandsOf(tools.Plan{Args: []string{"-i", "x"}, Staged: "s.gif", OutputPath: "o.gif"}).Fixup != nil {
		t.Error("a fix-up was set with no disposal")
	}
	if commandsOf(tools.Plan{Args: []string{"-i", "x"}, Staged: "s.gif", OutputPath: "o.gif",
		Disposal: tools.DisposeClear}).Fixup == nil {
		t.Error("no fix-up for a staged GIF with a disposal")
	}
}
