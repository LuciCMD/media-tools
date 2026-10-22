package tools

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Bounds and constants for size targeting.
const (
	// kbitPerMB converts decimal megabytes to kilobits. Upload limits are
	// usually decimal, and aiming at the smaller unit keeps the file under
	// either reading.
	kbitPerMB = 8000
	// sizeMargin leaves room for the container and for the encoder landing a
	// little over its target bitrate.
	sizeMargin = 0.95
	// minVideoKbps is the least bitrate worth encoding; below it the picture
	// is too damaged to be worth sending.
	minVideoKbps = 60
)

// compressToSizeTool shrinks a video to fit under a file size.
func compressToSizeTool() Tool {
	return Tool{
		ID:            "video.compress",
		Icon:          "target",
		Name:          "Compress to Size",
		Category:      CatVideo,
		Description:   "Shrink a video to fit under a file size, such as an upload limit.",
		Input:         InputFiles,
		Accept:        videoExts,
		OutputExt:     "mp4",
		Suffix:        "_small",
		UsesEncode:    true,
		NeedsDuration: true,
		Options: []Option{
			{Key: "size", Label: "Target Size", Kind: OptNumber, Default: "10", Min: 1, Max: 4000, Step: 1, Unit: "MB",
				Help: "Discord allows 10 MB without Nitro. The file lands a little under this."},
			{Key: "method", Label: "Method", Kind: OptSelect, Default: "accurate", Choices: []Choice{
				{"accurate", "Accurate", "Software, two passes"},
				{"fast", "Fast", "Your encoder, one pass"},
			}, Help: "Accurate hits the size most reliably. Fast uses your graphics card if you have one."},
			{Key: "height", Label: "Resolution", Kind: OptSelect, Default: "auto", Choices: []Choice{
				{"auto", "Automatic", "Smaller when the size is tight"},
				{"keep", "Keep Original", ""},
				{"1080", "1080p", ""},
				{"720", "720p", ""},
				{"480", "480p", ""},
				{"360", "360p", ""},
			}},
			{Key: "audio", Label: "Audio", Kind: OptSelect, Default: "96", Choices: []Choice{
				{"128", "128 kbps", "Music"},
				{"96", "96 kbps", "Balanced"},
				{"64", "64 kbps", "Speech"},
				{"0", "No Audio", "Most room for the picture"},
			}, Advanced: true},
		},
		Build: buildCompressToSize,
	}
}

// buildCompressToSize computes the bitrate that fits the target and renders
// either a two-pass software encode or a one-pass encode with the chosen
// encoder.
func buildCompressToSize(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	if in.DurationS <= 0 {
		return Plan{}, fmt.Errorf("the length of this video couldn't be read, so it can't be sized")
	}

	targetMB := optFloat(in.Options, "size", 10)
	audioKbps := optInt(in.Options, "audio", 96)
	videoKbps := sizeBudgetKbps(targetMB, in.DurationS) - audioKbps
	if videoKbps < minVideoKbps {
		return Plan{}, fmt.Errorf("%g MB is too small for %s of video; try %d MB or more, or trim it first",
			targetMB, spokenLength(in.DurationS), smallestSensibleMB(in.DurationS, audioKbps))
	}

	shared := sizingFilterArgs(optStr(in.Options, "height", "auto"), videoKbps)
	audio := []string{"-an"}
	if audioKbps > 0 {
		audio = []string{"-c:a", "aac", "-b:a", strconv.Itoa(audioKbps) + "k"}
	}

	if optStr(in.Options, "method", "accurate") == "fast" {
		args := append([]string{"-i", in.Path}, shared...)
		args = append(args, bitrateEncodeArgs(enc, videoKbps)...)
		args = append(args, audio...)
		args = append(args, "-movflags", "+faststart", "-y", in.Output)
		return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
	}
	return twoPassPlan(in, enc, shared, audio, videoKbps), nil
}

// twoPassPlan renders a libx264 two-pass encode at videoKbps. The first pass
// only gathers statistics, so it writes nothing but its log.
func twoPassPlan(in Inputs, enc Encode, shared, audio []string, videoKbps int) Plan {
	logPrefix := in.Output + ".passlog"
	rate := strconv.Itoa(videoKbps) + "k"
	codec := []string{"-c:v", "libx264", "-preset", x264Preset(enc.Preset), "-b:v", rate,
		"-pix_fmt", "yuv420p", "-passlogfile", logPrefix}

	first := append([]string{"-i", in.Path}, shared...)
	first = append(first, codec...)
	first = append(first, "-pass", "1", "-an", "-f", "null", "-y", "-")

	second := append([]string{"-i", in.Path}, shared...)
	second = append(second, codec...)
	second = append(second, "-pass", "2")
	second = append(second, audio...)
	second = append(second, "-movflags", "+faststart", "-y", in.Output)

	return Plan{
		Args:       first,
		Passes:     [][]string{second},
		OutputPath: in.Output,
		NeedsProbe: true,
		Cleanup:    []string{logPrefix + "-0.log", logPrefix + "-0.log.mbtree"},
	}
}

// sizeBudgetKbps is the total bitrate that fits targetMB over durationS.
func sizeBudgetKbps(targetMB, durationS float64) int {
	return int(math.Floor(targetMB * kbitPerMB * sizeMargin / durationS))
}

// smallestSensibleMB is the least target that leaves minVideoKbps for the
// picture, rounded up to a whole megabyte.
func smallestSensibleMB(durationS float64, audioKbps int) int {
	need := float64(minVideoKbps+audioKbps) * durationS / (kbitPerMB * sizeMargin)
	return int(math.Ceil(need))
}

// sizingFilterArgs scales the picture down when asked to, or automatically
// when the bitrate is too low to carry the full resolution well. The height
// never grows: min() keeps a smaller source as it is.
func sizingFilterArgs(choice string, videoKbps int) []string {
	height := 0
	switch choice {
	case "keep":
		return nil
	case "auto":
		height = autoHeight(videoKbps)
	default:
		height, _ = strconv.Atoi(choice)
	}
	if height <= 0 {
		return nil
	}
	return []string{"-vf", fmt.Sprintf("scale=-2:'min(%d,ih)'", height)}
}

// autoHeight picks the tallest resolution the bitrate can carry cleanly in
// H.264. The thresholds are conservative rules of thumb, not measurements of
// any particular source.
func autoHeight(videoKbps int) int {
	switch {
	case videoKbps < 400:
		return 360
	case videoKbps < 900:
		return 480
	case videoKbps < 2000:
		return 720
	case videoKbps < 4500:
		return 1080
	}
	return 0
}

// bitrateEncodeArgs renders a one-pass encode held to videoKbps with the
// chosen encoder. The rate is capped as well as targeted, so a busy scene
// can't push the file over the size.
func bitrateEncodeArgs(enc Encode, videoKbps int) []string {
	rate := strconv.Itoa(videoKbps) + "k"
	args := []string{"-c:v", enc.Encoder}
	switch {
	case strings.Contains(enc.Encoder, "nvenc"), strings.Contains(enc.Encoder, "amf"):
		args = append(args, "-rc", "cbr")
	case strings.HasPrefix(enc.Encoder, "libx26"):
		args = append(args, "-preset", x264Preset(enc.Preset))
	}
	args = append(args, "-b:v", rate, "-maxrate", rate, "-bufsize", rate)
	if enc.UseHardware && enc.DeviceIndex >= 0 && strings.Contains(enc.Encoder, "nvenc") {
		args = append(args, "-gpu", strconv.Itoa(enc.DeviceIndex))
	}
	return append(args, "-pix_fmt", "yuv420p")
}

// x264Preset passes a known preset through and falls back to medium.
func x264Preset(preset string) string {
	switch preset {
	case "ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow":
		return preset
	}
	return "medium"
}

// spokenLength renders a duration the way a person would say it.
func spokenLength(seconds float64) string {
	total := int(math.Round(seconds))
	if total < 60 {
		return fmt.Sprintf("%d seconds", total)
	}
	if total < 3600 {
		return fmt.Sprintf("%d minutes", (total+30)/60)
	}
	return fmt.Sprintf("%.1f hours", seconds/3600)
}
