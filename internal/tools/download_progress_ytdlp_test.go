package tools

import (
	"context"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// TestYtdlpDownloadReportsProgress checks a real download, run the way the
// queue runs it, reports a rising fraction with sizes and speed, then the
// step after it, where it used to go from 0% to done in one jump
func TestYtdlpDownloadReportsProgress(t *testing.T) {
	link := localVideoServer(t)
	tool, _ := Get("download.video")
	opts, err := tool.Validate(map[string]string{"container": "mkv"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := tool.Build(Inputs{Path: link, Output: t.TempDir(), Options: opts}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	// Slowed so the clip takes a few seconds and reports along the way
	args := append(ffmpeg.DownloadProgressArgs(), "--ignore-config", "--cache-dir", t.TempDir(), "--limit-rate", "400K")
	args = append(args, plan.Args...)

	var samples []ffmpeg.Progress
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := ffmpeg.Run(ctx, ffmpeg.Request{Binary: "yt-dlp", Args: args}, func(p ffmpeg.Progress) {
		samples = append(samples, p)
	})
	if err != nil {
		t.Fatalf("yt-dlp failed: %v\n%s", err, res.Stderr)
	}

	var partway, remux bool
	last := -1.0
	for _, p := range samples {
		if p.Fraction > 0.05 && p.Fraction < 0.95 && p.TotalBytes > 0 && p.RateBps > 0 && p.ETASeconds >= 0 {
			partway = true
		}
		if p.Stage == "Changing container" {
			remux = true
		}
		if p.Fraction >= 0 && p.Stage == "Downloading" && p.Fraction < last {
			t.Errorf("the fraction went back from %.3f to %.3f", last, p.Fraction)
		}
		if p.Fraction >= 0 {
			last = p.Fraction
		}
	}
	t.Logf("%d samples", len(samples))
	if !partway {
		t.Errorf("no sample partway with a size, speed and time left: %+v", samples)
	}
	if !remux {
		t.Errorf("the remux step was never named")
	}
}
