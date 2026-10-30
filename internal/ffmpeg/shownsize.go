package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
)

// ShownSize is the size of path's first picture as a viewer shows it, turned
// by its orientation, where a phone photo is often stored sideways with an
// EXIF tag saying so and the stream's own size is the stored one
func ShownSize(ctx context.Context, path string) (int, int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, 0, errors.New("probe: empty path")
	}
	bins := Locate()
	if bins.FFprobe == "" {
		return 0, 0, ErrNotFound
	}
	runCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// The turn is only on the decoded frame for a JPEG, not on its stream
	cmd := exec.CommandContext(runCtx, bins.FFprobe, "-v", "error", "-select_streams", "v:0",
		"-read_intervals", "%+#1", "-show_frames",
		"-show_entries", "frame=width,height:frame_side_data=rotation",
		"-print_format", "json", path)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("probe %q: %w", path, err)
	}
	if len(out) > maxProbeBytes {
		return 0, 0, errors.New("probe: ffprobe output exceeded size limit")
	}
	return decodeShownSize(out)
}

// decodeShownSize reads the first frame's size and swaps it for a quarter turn
func decodeShownSize(out []byte) (int, int, error) {
	var raw struct {
		Frames []struct {
			Width    int `json:"width"`
			Height   int `json:"height"`
			SideData []struct {
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return 0, 0, fmt.Errorf("probe: malformed ffprobe json: %w", err)
	}
	if len(raw.Frames) == 0 || raw.Frames[0].Width <= 0 || raw.Frames[0].Height <= 0 {
		return 0, 0, errors.New("probe: no picture")
	}
	f := raw.Frames[0]
	for i := 0; i < len(f.SideData) && i < maxStreams; i++ {
		if quarter := math.Mod(math.Abs(math.Round(f.SideData[i].Rotation/90)), 2); quarter == 1 {
			return f.Height, f.Width, nil
		}
	}
	return f.Width, f.Height, nil
}
