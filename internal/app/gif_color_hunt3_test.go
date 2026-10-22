package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"media-tools/internal/ffmpeg"
	"media-tools/internal/jobs"
	"media-tools/internal/tools"
)

// hunt3Garbage writes a file no probe can read
func hunt3Garbage(t *testing.T, path string) {
	t.Helper()
	junk := []byte(strings.Repeat("not a video, just text <script>'\"; rm -rf\n", 200))
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestGIFColorHunt3UnreadableDownloadIsNotTagged checks a download ffprobe
// can't read still gets its GIF commands, untagged, with no length
func TestGIFColorHunt3UnreadableDownloadIsNotTagged(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg is not installed")
	}
	out := t.TempDir()
	_, plan, work := gifPlan(t, "https://example.com/v", out, map[string]string{"container": "gif"})
	hunt3Garbage(t, filepath.Join(work, "Clip [c].mp4"))
	stage, err := finishStage(context.Background(), plan, work, bins.FFmpeg)
	if err != nil {
		t.Fatalf("finishStage: %v", err)
	}
	all := strings.Join(append(append([]string{}, stage.Args...), stage.Passes[0]...), " ")
	if strings.Contains(all, "setparams") {
		t.Errorf("an unprobed download was tagged: %q", all)
	}
	if stage.Totals.DurationS != 0 || stage.Totals.Frames != 0 {
		t.Errorf("totals %+v for an unreadable file", stage.Totals)
	}
}

// TestGIFColorHunt3TrackToolsStillNeedTheProbe checks only colours are
// optional, so a tool that needs the tracks still fails on an unreadable file
func TestGIFColorHunt3TrackToolsStillNeedTheProbe(t *testing.T) {
	if b := ffmpeg.Locate(); b.FFprobe == "" {
		t.Skip("ffprobe is not installed")
	}
	src := filepath.Join(t.TempDir(), "bad.mp4")
	hunt3Garbage(t, src)
	checked := 0
	for _, tool := range tools.All() {
		if !tool.NeedsStreams {
			continue
		}
		checked++
		if _, err := probedInputs(context.Background(), tool, tools.Inputs{Path: src}, ffmpeg.Totals{}); err == nil {
			t.Errorf("%s ran on a file whose tracks couldn't be read", tool.ID)
		}
	}
	if checked == 0 {
		t.Fatal("no tool needs the tracks")
	}
}

// TestGIFColorHunt3VideoColorSkipsOtherStreams checks the colours come from
// the first video stream, whatever comes before it
func TestGIFColorHunt3VideoColorSkipsOtherStreams(t *testing.T) {
	info := ffmpeg.MediaInfo{Streams: []ffmpeg.Stream{
		{Type: "audio"}, {Type: "subtitle"},
		{Type: "video", Width: 1920, Height: 1080, ColorSpace: "unknown"},
		{Type: "video", Width: 320, Height: 240, ColorSpace: "bt470bg"},
	}}
	if got := videoColor(info); got != (tools.SourceColor{Space: "unknown", Width: 1920, Height: 1080}) {
		t.Errorf("videoColor %+v", got)
	}
	if got := videoColor(ffmpeg.MediaInfo{Streams: []ffmpeg.Stream{{Type: "audio"}}}); got != (tools.SourceColor{}) {
		t.Errorf("audio only gave %+v", got)
	}
}

// TestGIFColorHunt3ConvertUnreadableFails runs Convert to GIF on a file
// ffprobe can't read through a real queue, built as submitJob builds it, and
// checks the job fails with ffmpeg's reason and leaves no GIF or palette behind
func TestGIFColorHunt3ConvertUnreadableFails(t *testing.T) {
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "My Clip ü.mp4")
	hunt3Garbage(t, src)
	tool, err := tools.Get("video.gif")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := tool.Validate(nil)
	if err != nil {
		t.Fatal(err)
	}
	gif := filepath.Join(dir, "My Clip ü.gif")
	in := tools.Inputs{Path: src, Paths: []string{src}, Output: gif, Options: opts, SubtitleTracks: tools.UnknownCount}
	enc := tools.Encode{Encoder: "libx264", Quality: 23, Preset: "medium", AudioKbps: 192}
	if _, err := tool.Build(in, enc); err != nil {
		t.Fatalf("the submit-time check refused it: %v", err)
	}
	spec := jobs.Spec{Tool: tool.ID, Label: "Convert to GIF - x", Input: src, Output: gif, Binary: bins.FFmpeg,
		ProbePaths: []string{src},
		Prepare: func(ctx context.Context, totals ffmpeg.Totals) (jobs.Commands, error) {
			probed, err := probedInputs(ctx, tool, in, totals)
			if err != nil {
				return jobs.Commands{}, err
			}
			p, err := tool.Build(probed, enc)
			if err != nil {
				return jobs.Commands{}, err
			}
			return commandsOf(p), nil
		}}
	// A failed probe only leaves the colours unchecked, so ffmpeg gets to say what's wrong
	if probed, err := probedInputs(context.Background(), tool, in, ffmpeg.Totals{}); err != nil ||
		probed.Color != (tools.SourceColor{}) {
		t.Errorf("unreadable file gave %+v, %v, want zero colours and no error", probed.Color, err)
	}
	job, _ := gifJobRun(t, spec, "")
	if !strings.Contains(job.Error, "Invalid data") {
		t.Errorf("the reason shown doesn't say the file is unreadable: %q", job.Error)
	}
	if job.State != jobs.StateFailed || job.Error == "" {
		t.Fatalf("job ended %s with %q, want failed with a reason", job.State, job.Error)
	}
	t.Logf("shown: %q", job.Error)
	for _, left := range []string{gif, gif + ".mediatools-palette.png"} {
		if _, err := os.Stat(left); err == nil {
			t.Errorf("%s was left behind", filepath.Base(left))
		}
	}
}
