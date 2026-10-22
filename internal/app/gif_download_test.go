package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
	"media-tools/internal/jobs"
	"media-tools/internal/tools"
)

// gifPlan builds Download Video as a GIF for opts into out, with its work
// folder made inside out as submitDownload makes it
func gifPlan(t *testing.T, link, out string, opts map[string]string) (tools.Tool, tools.Plan, string) {
	t.Helper()
	tool, err := tools.Get("download.video")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(out, ".mediatools-")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := tool.Build(tools.Inputs{Path: link, Output: out, WorkDir: work, Options: valid},
		tools.Encode{Encoder: "libx264", Quality: 23, Preset: "medium", AudioKbps: 192})
	if err != nil {
		t.Fatal(err)
	}
	return tool, plan, work
}

// TestDownloadSpecFinishesAGIF checks a GIF download's job hands what lands
// in its work folder to ffmpeg, measured for the bar, with the GIF moved
// out once whole, while an MP4's job has no next stage or work folder
func TestDownloadSpecFinishesAGIF(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg is not installed")
	}
	bins.YTDLP = "yt-dlp-stand-in"
	out := t.TempDir()
	tool, plan, work := gifPlan(t, "https://example.com/v", out, map[string]string{"container": "gif"})
	spec := downloadSpec(tool, "https://example.com/v", out, work, plan, bins)
	if spec.Binary != bins.YTDLP || spec.WorkDir != work || spec.ThenCount != 2 || spec.Then == nil {
		t.Fatalf("spec binary %q, work %q, then %d", spec.Binary, spec.WorkDir, spec.ThenCount)
	}
	if !slices.Contains(spec.Args, "--progress-template") {
		t.Errorf("the download doesn't report its progress: %q", spec.Args)
	}

	src := filepath.Join(work, "Clip [c].mp4")
	out1, err := exec.Command(bins.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "testsrc2=s=320x180:r=15:d=3", "-c:v", "libx264", "-preset", "ultrafast", "-y", src).CombinedOutput()
	if err != nil {
		t.Fatalf("making the clip: %v\n%s", err, out1)
	}
	if err := os.WriteFile(filepath.Join(work, "Clip [c].f1.mp4.part"), make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	stage, err := spec.Then(context.Background())
	if err != nil {
		t.Fatalf("Then: %v", err)
	}
	if stage.Binary != bins.FFmpeg || stage.Totals.DurationS < 2.5 || stage.Totals.DurationS > 3.5 {
		t.Errorf("stage runs %q over %.2f s", stage.Binary, stage.Totals.DurationS)
	}
	progress := ffmpeg.ProgressArgs()
	for _, cmd := range append([][]string{stage.Args}, stage.Passes...) {
		if !slices.Equal(cmd[:len(progress)], progress) || !slices.Contains(cmd, src) {
			t.Errorf("a command doesn't report progress or read the clip: %q", cmd)
		}
	}
	want := jobs.Move{From: filepath.Join(work, "result.gif"), To: filepath.Join(out, "Clip [c].gif")}
	if stage.Keep == nil || *stage.Keep != want {
		t.Errorf("keep %+v, want %+v", stage.Keep, want)
	}
	if len(stage.Steps) != 2 {
		t.Errorf("steps %q", stage.Steps)
	}

	mp4Tool, mp4, mp4Work := gifPlan(t, "https://example.com/v", out, map[string]string{"container": "mp4"})
	if s := downloadSpec(mp4Tool, "https://example.com/v", out, mp4Work, mp4, bins); s.Then != nil || s.WorkDir != "" {
		t.Errorf("an MP4 has a next stage %v or work folder %q", s.Then != nil, s.WorkDir)
	}
}

// TestGIFsLearnTheSourcesColours checks the app passes what the probe found
// to both GIF paths, so an HD video that names no colour standard is tagged
// with HD's before it becomes a GIF, where ffmpeg assumed SD's
func TestGIFsLearnTheSourcesColours(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg is not installed")
	}
	out := t.TempDir()
	tool, plan, work := gifPlan(t, "https://example.com/v", out, map[string]string{"container": "gif"})
	src := filepath.Join(work, "HD [h].mp4")
	made, err := exec.Command(bins.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "testsrc2=s=1280x720:r=10:d=1", "-vf", "setparams=colorspace=unknown",
		"-c:v", "libx264", "-preset", "ultrafast", "-y", src).CombinedOutput()
	if err != nil {
		t.Fatalf("making the clip: %v\n%s", err, made)
	}
	stage, err := downloadSpec(tool, "https://example.com/v", out, work, plan, bins).Then(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(stage.Args, " "), "setparams=colorspace=bt709,") {
		t.Errorf("an untagged HD download isn't tagged: %q", stage.Args)
	}

	convert, err := tools.Get("video.gif")
	if err != nil {
		t.Fatal(err)
	}
	in, err := probedInputs(context.Background(), convert, tools.Inputs{Path: src}, ffmpeg.Totals{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Color.Width != 1280 || in.Color.Height != 720 || (in.Color.Space != "" && in.Color.Space != "unknown") {
		t.Errorf("Convert to GIF learned %+v", in.Color)
	}

	// A file ffprobe can't read only loses the colour check for a GIF, as a
	// download does, where it failed the job over "tracks"
	junk := filepath.Join(t.TempDir(), "junk.mp4")
	if err := os.WriteFile(junk, []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if in, err := probedInputs(context.Background(), convert, tools.Inputs{Path: junk}, ffmpeg.Totals{}); err != nil ||
		in.Color != (tools.SourceColor{}) {
		t.Errorf("an unreadable file gave %+v, %v", in.Color, err)
	}
	burn, err := tools.Get("video.burnsubs")
	if err != nil {
		t.Skip("no Burn In Subtitles tool to compare")
	}
	if _, err := probedInputs(context.Background(), burn, tools.Inputs{Path: junk}, ffmpeg.Totals{}); err == nil {
		t.Error("Burn In Subtitles, which needs the tracks, ran on an unreadable file")
	}
}

// localClip serves seconds of 640x360 video with sound over loopback, for
// the opt-in runs of the real yt-dlp
func localClip(t *testing.T, seconds string) (string, ffmpeg.Binaries) {
	t.Helper()
	if os.Getenv("MEDIA_TOOLS_YTDLP") != "1" {
		t.Skip("set MEDIA_TOOLS_YTDLP=1 to run yt-dlp against a local clip")
	}
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" || bins.YTDLP == "" {
		t.Skip("ffmpeg or yt-dlp is not installed")
	}
	dir := t.TempDir()
	out, err := exec.Command(bins.FFmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d="+seconds, "-f", "lavfi", "-i", "sine=f=440:d="+seconds,
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-y", filepath.Join(dir, "clip.mp4")).CombinedOutput()
	if err != nil {
		t.Fatalf("making the clip: %v\n%s", err, out)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return srv.URL + "/clip.mp4", bins
}

// gifJobRun submits spec to a real queue and returns how it ended and every
// update it gave, cancelling once cancelAt is the step shown, if given
func gifJobRun(t *testing.T, spec jobs.Spec, cancelAt string) (jobs.Job, []jobs.Job) {
	t.Helper()
	// The author's yt-dlp config and cache stay out of it
	spec.Args = append([]string{"--ignore-config", "--cache-dir", t.TempDir()}, spec.Args...)
	var mu sync.Mutex
	var seen []jobs.Job
	q, err := jobs.NewQueue(1, func(j jobs.Job) {
		mu.Lock()
		seen = append(seen, j)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Shutdown()
	id, err := q.Submit(spec)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := false
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		for _, j := range q.List() {
			if j.ID != id {
				continue
			}
			if j.State == jobs.StateSucceeded || j.State == jobs.StateFailed || j.State == jobs.StateCanceled {
				mu.Lock()
				defer mu.Unlock()
				return j, slices.Clone(seen)
			}
			if cancelAt != "" && !cancelled && j.Progress.Stage == cancelAt {
				if err := q.Cancel(id); err != nil {
					t.Fatal(err)
				}
				cancelled = true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the job never ended")
	return jobs.Job{}, nil
}

// outFiles lists what's in out
func outFiles(t *testing.T, out string) []string {
	t.Helper()
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestYtdlpGIFJobEndToEnd runs a GIF download as the app does, through the
// real queue: the clip downloads, the palette is made, the GIF drawn and
// moved out, and only the GIF is left, with the bar moving forward through
// three named steps
func TestYtdlpGIFJobEndToEnd(t *testing.T) {
	link, bins := localClip(t, "20")
	out := t.TempDir()
	tool, plan, work := gifPlan(t, link, out, map[string]string{"container": "gif", "start": "00:00:02", "end": "00:00:06"})
	end, seen := gifJobRun(t, downloadSpec(tool, link, out, work, plan, bins), "")
	if end.State != jobs.StateSucceeded {
		t.Fatalf("the job ended %s: %s", end.State, end.Error)
	}
	names := outFiles(t, out)
	if len(names) != 1 || names[0] != "clip [clip].gif" {
		t.Fatalf("out holds %q, want only the GIF", names)
	}
	gif := filepath.Join(out, names[0])
	info, err := exec.Command(bins.FFprobe, "-v", "error", "-show_entries", "stream=codec_name,width:format=duration",
		"-of", "default=nw=1", gif).Output()
	if err != nil || !strings.Contains(string(info), "codec_name=gif") || !strings.Contains(string(info), "width=480") {
		t.Errorf("the result isn't a 480 px GIF: %v\n%s", err, info)
	}
	steps := map[string]bool{}
	last := -1.0
	for _, j := range seen {
		p := j.Progress
		if j.State != jobs.StateRunning || p.Passes == 0 {
			continue
		}
		steps[p.Stage] = true
		if p.Passes != 3 {
			t.Errorf("an update counts %d commands, want 3", p.Passes)
		}
		if p.Fraction >= 0 {
			if p.Fraction < last-0.001 {
				t.Errorf("the bar went back from %.3f to %.3f at %q", last, p.Fraction, p.Stage)
			}
			last = p.Fraction
		}
	}
	if !steps["Making the palette"] || !steps["Drawing the GIF"] {
		t.Errorf("steps seen %v", steps)
	}
}

// TestYtdlpGIFJobCancelThenRetry cancels a whole-video GIF while it's being
// drawn, which must leave nothing in the folder, then runs it again the way
// Retry does, which must leave just the GIF
func TestYtdlpGIFJobCancelThenRetry(t *testing.T) {
	link, bins := localClip(t, "60")
	out := t.TempDir()
	tool, plan, work := gifPlan(t, link, out, map[string]string{"container": "gif", "span": "original",
		"sizing": "original", "pace": "original"})
	spec := downloadSpec(tool, link, out, work, plan, bins)
	end, _ := gifJobRun(t, spec, "Drawing the GIF")
	if end.State != jobs.StateCanceled {
		t.Fatalf("the job ended %s: %s", end.State, end.Error)
	}
	if names := outFiles(t, out); len(names) != 0 {
		t.Errorf("a cancelled GIF left %q", names)
	}

	spec.Args = retryDownloadArgs(spec.Args)
	end, _ = gifJobRun(t, spec, "")
	if end.State != jobs.StateSucceeded {
		t.Fatalf("the retry ended %s: %s", end.State, end.Error)
	}
	if names := outFiles(t, out); len(names) != 1 || !strings.HasSuffix(names[0], ".gif") {
		t.Errorf("the retry left %q, want only the GIF", names)
	}
}
