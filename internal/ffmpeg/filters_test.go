package ffmpeg

import (
	"context"
	"testing"
)

// TestHasFilterReadsTheBuild checks a filter every build has is found, a
// made-up one isn't, and a missing ffmpeg has none.
func TestHasFilterReadsTheBuild(t *testing.T) {
	bins := Locate()
	if bins.FFmpeg == "" {
		t.Skip("ffmpeg is not installed")
	}
	ctx := context.Background()
	if !HasFilter(ctx, bins.FFmpeg, "volume") {
		t.Error("the volume filter wasn't found")
	}
	if HasFilter(ctx, bins.FFmpeg, "no-such-filter") {
		t.Error("a made-up filter was reported present")
	}
	if HasFilter(ctx, "", "volume") {
		t.Error("with no ffmpeg, no filter can be present")
	}
}
