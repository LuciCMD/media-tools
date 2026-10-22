package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRunReportsRealProgress drives ffmpeg end to end against a synthetic clip
// and asserts that progress advances monotonically to completion.
//
// It is skipped when ffmpeg is not installed, so the suite still passes on a
// machine without it.
func TestRunReportsRealProgress(t *testing.T) {
	bins := Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "converted.mp4")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	makeTestClip(ctx, t, bins.FFmpeg, source)

	info, err := Probe(ctx, source)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.DurationS <= 0 {
		t.Fatalf("probed duration = %v, want a positive value", info.DurationS)
	}

	samples := runConversion(ctx, t, bins.FFmpeg, source, output, info)
	assertProgress(t, samples)

	if _, err := os.Stat(output); err != nil {
		t.Fatalf("expected an output file: %v", err)
	}
}

// makeTestClip renders a short synthetic video to path.
func makeTestClip(ctx context.Context, t *testing.T, ffmpegPath, path string) {
	t.Helper()
	_, err := Run(ctx, Request{
		Binary: ffmpegPath,
		Args: []string{
			"-hide_banner", "-loglevel", "error", "-nostdin",
			"-f", "lavfi", "-i", "testsrc=size=1280x720:rate=30:duration=20",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
			"-y", path,
		},
	}, nil)
	if err != nil {
		t.Fatalf("could not create the test clip: %v", err)
	}
}

// runConversion re-encodes source and collects every progress sample.
// The clip is deliberately long enough that ffmpeg emits several periodic
// progress blocks rather than only the terminal one.
func runConversion(ctx context.Context, t *testing.T, ffmpegPath, source, output string,
	info MediaInfo) []Progress {
	t.Helper()

	args := append(ProgressArgs(),
		"-i", source,
		"-c:v", "libx264", "-preset", "medium", "-crf", "23",
		"-y", output,
	)

	var samples []Progress
	_, err := Run(ctx, Request{
		Binary: ffmpegPath,
		Args:   args,
		Totals: Totals{DurationS: info.DurationS, Frames: info.TotalFrame},
	}, func(p Progress) {
		samples = append(samples, p)
	})
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	return samples
}

// assertProgress checks the samples form a sane, monotonic sequence ending at 1.
func assertProgress(t *testing.T, samples []Progress) {
	t.Helper()
	if len(samples) < 2 {
		t.Fatalf("expected several progress samples, got %d", len(samples))
	}

	previous := -1.0
	for i, s := range samples {
		if s.Fraction < 0 {
			t.Errorf("sample %d has an indeterminate fraction despite a known duration", i)
		}
		if s.Fraction < previous {
			t.Errorf("sample %d went backwards: %v after %v", i, s.Fraction, previous)
		}
		previous = s.Fraction
	}

	last := samples[len(samples)-1]
	if !last.Done {
		t.Error("the final sample is not marked done")
	}
	if last.Fraction != 1 {
		t.Errorf("final fraction = %v, want 1", last.Fraction)
	}
}

// TestRunCancels verifies that cancelling the context stops ffmpeg promptly.
func TestRunCancels(t *testing.T) {
	bins := Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	output := filepath.Join(t.TempDir(), "cancelled.mp4")

	go func() {
		time.Sleep(700 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := Run(ctx, Request{
		Binary: bins.FFmpeg,
		Args: append(ProgressArgs(),
			"-f", "lavfi", "-i", "testsrc=size=1920x1080:rate=30:duration=600",
			"-c:v", "libx264", "-preset", "veryslow", "-y", output),
		Totals: Totals{DurationS: 600},
	}, nil)

	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("cancellation took %v, which is too slow", elapsed)
	}
}
