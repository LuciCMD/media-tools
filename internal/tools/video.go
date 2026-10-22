package tools

import (
	"fmt"
	"strconv"
	"strings"
)

// videoExts is the accepted input set for video tools.
var videoExts = []string{"mp4", "mkv", "mov", "avi", "webm", "flv", "m4v", "wmv", "ts", "mpg"}

// videoTools declares every video operation.
func videoTools() []Tool {
	return []Tool{
		convertVideoTool(),
		compressToSizeTool(),
		changeContainerTool(),
		joinClipsTool(),
		resolutionTool(),
		trimTool(),
		cropTool(),
		rotateTool(),
		toGIFTool(),
		removeAudioTool(),
		contactSheetTool(),
		extractFramesTool(),
		colorFilterTool(),
	}
}

// convertVideoTool re-encodes video into another container.
func convertVideoTool() Tool {
	return Tool{
		ID:          "video.convert",
		Icon:        "convert",
		Name:        "Convert Video",
		Category:    CatVideo,
		Description: "Re-encode video into another container using the selected hardware or software encoder.",
		Input:       InputFiles,
		Accept:      videoExts,
		UsesEncode:  true,
		Options: []Option{
			{Key: "format", Label: "Container", Kind: OptSelect, Default: "mp4", Choices: []Choice{
				{"mp4", "MP4", "Most compatible"},
				{"mkv", "MKV", "Flexible, many tracks"},
				{"webm", "WebM", "Web native"},
				{"mov", "MOV", "Apple / editing"},
			}},
			qualityOption(),
			{Key: "audio", Label: "Audio Handling", Kind: OptSelect, Default: "encode", Choices: []Choice{
				{"encode", "Re-Encode to AAC", ""},
				{"copy", "Copy Untouched", ""},
				{"none", "Remove Audio", ""},
			}, Advanced: true},
		},
		Build: buildVideoConvert,
	}
}

// buildVideoConvert renders a straightforward container/codec conversion.
func buildVideoConvert(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	args := decodeArgs(enc)
	args = append(args, "-i", in.Path)
	args = append(args, videoEncodeArgs(enc)...)
	args = append(args, audioModeArgs(optStr(in.Options, "audio", "encode"), enc)...)
	args = append(args, "-movflags", "+faststart", "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// audioModeArgs maps the audio handling choice onto flags.
func audioModeArgs(mode string, enc Encode) []string {
	switch mode {
	case "copy":
		return []string{"-c:a", "copy"}
	case "none":
		return []string{"-an"}
	}
	return audioArgs(enc)
}

// resolutionTool rescales video to a standard resolution.
func resolutionTool() Tool {
	return Tool{
		ID:          "video.resolution",
		Icon:        "monitor",
		Name:        "Change Resolution",
		Category:    CatVideo,
		Description: "Scale video to a standard resolution, padding to preserve the original aspect ratio.",
		Input:       InputFiles,
		Accept:      videoExts,
		UsesEncode:  true,
		Options: []Option{
			{Key: "resolution", Label: "Resolution", Kind: OptSelect, Default: "1920:1080", Choices: []Choice{
				{"3840:2160", "2160p", "4K UHD"},
				{"2560:1440", "1440p", "QHD"},
				{"1920:1080", "1080p", "Full HD"},
				{"1280:720", "720p", "HD"},
				{"854:480", "480p", "SD"},
			}},
			{Key: "fps", Label: "Frame Rate", Kind: OptSelect, Default: "keep", Choices: []Choice{
				{"keep", "Keep Source Rate", ""},
				{"24", "24 fps", "Film"},
				{"30", "30 fps", ""},
				{"60", "60 fps", ""},
			}},
			qualityOption(),
			{Key: "pad", Label: "Pad to Exact Size", Kind: OptBool, Default: "true", Advanced: true,
				Help: "When off, the output keeps the source aspect ratio instead of adding bars."},
		},
		Build: buildResolution,
	}
}

// buildResolution renders the scale and optional pad filter chain.
func buildResolution(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	res := optStr(in.Options, "resolution", "1920:1080")
	dims := strings.SplitN(res, ":", 2)
	if len(dims) != 2 {
		return Plan{}, fmt.Errorf("malformed resolution %q", res)
	}

	filter := fmt.Sprintf("scale=%s:force_original_aspect_ratio=decrease", res)
	if optBool(in.Options, "pad") {
		filter += fmt.Sprintf(",pad=%s:(ow-iw)/2:(oh-ih)/2", res)
	}

	args := decodeArgs(enc)
	args = append(args, "-i", in.Path, "-vf", filter)
	if fps := optStr(in.Options, "fps", "keep"); fps != "keep" {
		args = append(args, "-r", fps)
	}
	args = append(args, videoEncodeArgs(enc)...)
	args = append(args, audioArgs(enc)...)
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// trimTool cuts a section out of a video by timestamp.
func trimTool() Tool {
	return Tool{
		ID:          "video.trim",
		Icon:        "scissors",
		Name:        "Trim",
		Category:    CatVideo,
		Description: "Keep only the section between two timestamps, optionally without re-encoding.",
		Input:       InputFile,
		Accept:      videoExts,
		Suffix:      "_trim",
		UsesEncode:  true,
		Options: []Option{
			{Key: "start", Label: "Start Time", Kind: OptText, Default: "00:00:00",
				Help: "Format HH:MM:SS or HH:MM:SS.mmm"},
			{Key: "end", Label: "End Time", Kind: OptText, Default: "",
				Help: "Leave blank to run to the end of the file."},
			{Key: "mode", Label: "Method", Kind: OptSelect, Default: "copy", Choices: []Choice{
				{"copy", "Copy Streams", "Instant, cuts on keyframes"},
				{"encode", "Re-Encode", "Exact cut, slower"},
			}},
			qualityOption(),
		},
		Build: buildTrim,
	}
}

// buildTrim renders a stream-copy or re-encoding trim.
func buildTrim(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	start := optStr(in.Options, "start", "00:00:00")
	if err := validateTimecode(start); err != nil {
		return Plan{}, err
	}
	end := strings.TrimSpace(in.Options["end"])
	if end != "" {
		if err := validateTimecode(end); err != nil {
			return Plan{}, err
		}
	}

	args := []string{"-ss", start}
	if end != "" {
		args = append(args, "-to", end)
	}
	args = append(args, "-i", in.Path)

	if optStr(in.Options, "mode", "copy") == "copy" {
		args = append(args, "-c", "copy", "-avoid_negative_ts", "make_zero")
	} else {
		args = append(args, videoEncodeArgs(enc)...)
		args = append(args, audioArgs(enc)...)
	}
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// validateTimecode enforces the HH:MM:SS[.mmm] form.
func validateTimecode(v string) error {
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return fmt.Errorf("timestamp %q must look like HH:MM:SS", v)
	}
	for i := 0; i < len(parts); i++ {
		if _, err := strconv.ParseFloat(parts[i], 64); err != nil {
			return fmt.Errorf("timestamp %q contains a non-numeric field", v)
		}
	}
	return nil
}

// cropTool removes edges from the frame.
func cropTool() Tool {
	return Tool{
		ID:          "video.crop",
		Icon:        "crop",
		Name:        "Crop",
		Category:    CatVideo,
		Description: "Cut the frame down to a region, by preset aspect ratio or explicit pixels.",
		Input:       InputFile,
		Accept:      videoExts,
		Suffix:      "_crop",
		UsesEncode:  true,
		Options: []Option{
			{Key: "mode", Label: "Crop Mode", Kind: OptSelect, Default: "16:9", Choices: []Choice{
				{"16:9", "16:9 Widescreen", ""},
				{"1:1", "1:1 Square", ""},
				{"9:16", "9:16 Vertical", ""},
				{"4:3", "4:3 Classic", ""},
				{"manual", "Manual Pixel Values", ""},
			}},
			{Key: "width", Label: "Width", Kind: OptNumber, Default: "1920", Min: 2, Max: 16384, Step: 2},
			{Key: "height", Label: "Height", Kind: OptNumber, Default: "1080", Min: 2, Max: 16384, Step: 2},
			{Key: "x", Label: "X Offset", Kind: OptNumber, Default: "0", Min: 0, Max: 16384, Step: 1, Advanced: true},
			{Key: "y", Label: "Y Offset", Kind: OptNumber, Default: "0", Min: 0, Max: 16384, Step: 1, Advanced: true},
			qualityOption(),
		},
		Build: buildCrop,
	}
}

// buildCrop renders the crop filter for the chosen mode.
func buildCrop(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	filter := cropFilter(in.Options)

	args := decodeArgs(enc)
	args = append(args, "-i", in.Path, "-vf", filter)
	args = append(args, videoEncodeArgs(enc)...)
	args = append(args, audioArgs(enc)...)
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// cropFilter builds an aspect-ratio-driven or explicit crop expression.
func cropFilter(opts map[string]string) string {
	mode := optStr(opts, "mode", "16:9")
	if mode == "manual" {
		return fmt.Sprintf("crop=%d:%d:%d:%d",
			optInt(opts, "width", 1920), optInt(opts, "height", 1080),
			optInt(opts, "x", 0), optInt(opts, "y", 0))
	}

	ratio := strings.SplitN(mode, ":", 2)
	if len(ratio) != 2 {
		return "crop=iw:ih"
	}
	// Centre-crop to the widest region matching the target ratio.
	return fmt.Sprintf(
		"crop='min(iw,ih*%s/%s)':'min(ih,iw*%s/%s)':'(iw-ow)/2':'(ih-oh)/2'",
		ratio[0], ratio[1], ratio[1], ratio[0])
}

// rotateTool rotates or mirrors the frame.
func rotateTool() Tool {
	return Tool{
		ID:          "video.rotate",
		Icon:        "rotate",
		Name:        "Rotate & Flip",
		Category:    CatVideo,
		Description: "Rotate video in 90 degree steps or mirror it along either axis.",
		Input:       InputFiles,
		Accept:      videoExts,
		Suffix:      "_rot",
		UsesEncode:  true,
		Options: []Option{
			{Key: "transform", Label: "Transform", Kind: OptSelect, Default: "cw90", Choices: []Choice{
				{"cw90", "Rotate 90 Clockwise", ""},
				{"ccw90", "Rotate 90 Counter-Clockwise", ""},
				{"180", "Rotate 180", ""},
				{"hflip", "Mirror Horizontally", ""},
				{"vflip", "Mirror Vertically", ""},
			}},
			qualityOption(),
		},
		Build: buildRotate,
	}
}

// buildRotate renders the transpose or flip filter.
func buildRotate(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	filters := map[string]string{
		"cw90":  "transpose=1",
		"ccw90": "transpose=2",
		"180":   "transpose=1,transpose=1",
		"hflip": "hflip",
		"vflip": "vflip",
	}
	name := optStr(in.Options, "transform", "cw90")
	filter, ok := filters[name]
	if !ok {
		return Plan{}, fmt.Errorf("unknown transform %q", name)
	}

	args := []string{"-i", in.Path, "-vf", filter}
	args = append(args, videoEncodeArgs(enc)...)
	args = append(args, "-c:a", "copy", "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// toGIFTool converts video to an optimised animated GIF.
func toGIFTool() Tool {
	return Tool{
		ID:          "video.gif",
		Icon:        "gif",
		Name:        "Convert to GIF",
		Category:    CatVideo,
		Description: "Turn a clip into an animated GIF using a generated palette for clean colours.",
		Input:       InputFile,
		Accept:      append([]string{"gif"}, videoExts...),
		OutputExt:   "gif",
		// Built once probed, so an untagged HD video's colours come out right
		NeedsColor: true,
		Options: []Option{
			{Key: "fps", Label: "Frame Rate", Kind: OptNumber, Default: "15", Min: 1, Max: 50, Step: 1, Unit: "fps"},
			{Key: "width", Label: "Width", Kind: OptNumber, Default: "640", Min: 32, Max: 1920, Step: 2, Unit: "px",
				Help: "Height follows automatically to preserve the aspect ratio."},
			{Key: "colors", Label: "Palette Size", Kind: OptNumber, Default: "128", Min: 8, Max: 256, Step: 8, Advanced: true},
			{Key: "dither", Label: "Dithering", Kind: OptSelect, Default: "sierra2_4a", Advanced: true, Choices: gifDitherChoices},
		},
		Build: buildGIF,
	}
}

// buildGIF makes the GIF in two commands, a palette and then the frames
func buildGIF(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	fps := optInt(in.Options, "fps", 15)
	width := optInt(in.Options, "width", 640)
	colors := optInt(in.Options, "colors", 128)
	dither := optStr(in.Options, "dither", "sierra2_4a")

	// The output path is claimed for this job, so the palette beside it is too
	stages := gifColorStage(in.Color) + fmt.Sprintf("fps=%d,scale=%d:-1:flags=lanczos", fps, width)
	return gifTwoPass(in.Path, in.Output, in.Output+".mediatools-palette.png", stages, colors, dither), nil
}

// gifDitherChoices are the ways a GIF fakes the colours its palette lacks
var gifDitherChoices = []Choice{
	{"sierra2_4a", "Sierra", "Smooth gradients"},
	{"bayer", "Bayer", "Smaller files"},
	{"none", "None", "Flat colour art"},
}

// gifColorStage tags video that names no colour standard with the one a
// player would show it in, HD's for 1280 wide or over 576 tall, so its turn
// into RGB matches what was watched, where ffmpeg assumed SD's and shifted
// greens brighter and reds and blues duller
// Empty when the video names its own, is SD, or wasn't probed
func gifColorStage(c SourceColor) string {
	named := c.Space != "" && c.Space != "unknown"
	if named || c.Height <= 0 || c.Width < 1280 && c.Height <= 576 {
		return ""
	}
	return "setparams=colorspace=bt709,"
}

// gifTwoPass makes a GIF in two commands, one palette for the whole clip and
// then every frame drawn with it, so no colour shifts from frame to frame and
// memory stays flat however long it runs
// One command with split held every frame until palettegen saw the last
func gifTwoPass(src, out, palette, stages string, colors int, dither string) Plan {
	// A finer pattern than the default 2, which on a minute of 720p was closer
	// to the source (43.8 dB against 38.2 at the edges) and a little smaller
	if dither == "bayer" {
		dither = "bayer:bayer_scale=4"
	}
	return Plan{
		Args: []string{"-i", src, "-an", "-sn",
			"-vf", fmt.Sprintf("%s,palettegen=max_colors=%d:stats_mode=diff", stages, colors),
			"-update", "1", "-y", palette},
		Passes: [][]string{{"-i", src, "-i", palette, "-an", "-sn",
			"-lavfi", fmt.Sprintf("[0:v]%s[x];[x][1:v]paletteuse=dither=%s:diff_mode=rectangle", stages, dither),
			"-loop", "0", "-y", out}},
		Cleanup:    []string{palette},
		Steps:      []string{"Making the palette", "Drawing the GIF"},
		OutputPath: out,
		NeedsProbe: true,
	}
}

// removeAudioTool strips audio without touching the video stream.
func removeAudioTool() Tool {
	return Tool{
		ID:          "video.mute",
		Icon:        "mute",
		Name:        "Remove Audio",
		Category:    CatVideo,
		Description: "Strip every audio track, copying the video stream untouched so it is near-instant.",
		Input:       InputFiles,
		Accept:      videoExts,
		Suffix:      "_muted",
		Options: []Option{
			{Key: "subs", Label: "Keep Subtitles", Kind: OptBool, Default: "true"},
		},
		Build: buildMute,
	}
}

// buildMute renders an audio-dropping stream copy.
func buildMute(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	args := []string{"-i", in.Path, "-c:v", "copy", "-an"}
	if optBool(in.Options, "subs") {
		args = append(args, "-c:s", "copy")
	} else {
		args = append(args, "-sn")
	}
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// extractSubsTool exports a subtitle track to a sidecar file.
func extractSubsTool() Tool {
	return Tool{
		ID:          "video.extractsubs",
		Icon:        "captions-out",
		Name:        "Extract Subtitles",
		Category:    CatSubtitle,
		Description: "Export an embedded subtitle track to a standalone SRT or ASS file.",
		Input:       InputFile,
		Accept:      videoExts,
		Options: []Option{
			{Key: "track", Label: "Subtitle Track", Kind: OptNumber, Default: "0", Min: 0, Max: 15, Step: 1},
			{Key: "format", Label: "Format", Kind: OptSelect, Default: "srt", Choices: []Choice{
				{"srt", "SRT", "Plain text, universal"},
				{"ass", "ASS", "Keeps styling"},
			}},
		},
		Build: buildExtractSubs,
	}
}

// buildExtractSubs renders a subtitle demux.
func buildExtractSubs(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	track := optInt(in.Options, "track", 0)
	args := []string{
		"-i", in.Path,
		"-map", fmt.Sprintf("0:s:%d", track),
		"-c:s", optStr(in.Options, "format", "srt"),
		"-y", in.Output,
	}
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: false}, nil
}

// contactSheetTool builds a grid of thumbnails from a video.
func contactSheetTool() Tool {
	return Tool{
		ID:          "video.contactsheet",
		Icon:        "grid",
		Name:        "Contact Sheet",
		Category:    CatVideo,
		Description: "Generate a single image showing a grid of thumbnails sampled across the video.",
		Input:       InputFile,
		Accept:      videoExts,
		OutputExt:   "png",
		Suffix:      "_sheet",
		Options: []Option{
			{Key: "columns", Label: "Columns", Kind: OptNumber, Default: "4", Min: 1, Max: 10, Step: 1},
			{Key: "rows", Label: "Rows", Kind: OptNumber, Default: "4", Min: 1, Max: 10, Step: 1},
			{Key: "width", Label: "Thumbnail Width", Kind: OptNumber, Default: "320", Min: 80, Max: 960, Step: 10, Unit: "px"},
		},
		Build: buildContactSheet,
	}
}

// buildContactSheet renders an evenly-sampled thumbnail tile grid.
func buildContactSheet(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	cols := optInt(in.Options, "columns", 4)
	rows := optInt(in.Options, "rows", 4)
	width := optInt(in.Options, "width", 320)
	if cols < 1 || rows < 1 {
		return Plan{}, fmt.Errorf("contact sheet needs at least one row and column")
	}

	// thumbnail picks the most representative frame from each batch, giving a
	// more useful sheet than sampling at fixed intervals.
	filter := fmt.Sprintf("thumbnail=100,scale=%d:-1,tile=%dx%d", width, cols, rows)
	args := []string{"-i", in.Path, "-vf", filter, "-frames:v", "1", "-y", in.Output}

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: false}, nil
}

// extractFramesTool writes individual frames as image files.
func extractFramesTool() Tool {
	return Tool{
		ID:          "video.frames",
		Icon:        "layers",
		Name:        "Extract Frames",
		Category:    CatVideo,
		Description: "Write video frames out as numbered image files, at full or reduced rate.",
		Input:       InputFile,
		Accept:      append([]string{"gif"}, videoExts...),
		Options: []Option{
			{Key: "rate", Label: "Extract Rate", Kind: OptSelect, Default: "all", Choices: []Choice{
				{"all", "Every Frame", ""},
				{"1", "1 per Second", ""},
				{"0.2", "1 Every 5 Seconds", ""},
				{"0.0166", "1 per Minute", ""},
			}},
			{Key: "format", Label: "Image Format", Kind: OptSelect, Default: "png", Choices: []Choice{
				{"png", "PNG", "Lossless"},
				{"jpg", "JPG", "Smaller"},
			}},
		},
		Build: buildExtractFrames,
	}
}

// buildExtractFrames renders a numbered image sequence into the output folder.
func buildExtractFrames(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	args := decodeArgs(enc)
	args = append(args, "-i", in.Path)
	if rate := optStr(in.Options, "rate", "all"); rate != "all" {
		args = append(args, "-vf", "fps="+rate)
	}
	args = append(args, "-q:v", "2", "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// colorFilterTool applies a stylised colour grade, including the blue diamond
// look carried over from the original application.
func colorFilterTool() Tool {
	return Tool{
		ID:          "video.colorfilter",
		Icon:        "palette",
		Name:        "Colour Filter",
		Category:    CatVideo,
		Description: "Apply a stylised colour grade over the whole clip.",
		Input:       InputFiles,
		Accept:      videoExts,
		Suffix:      "_graded",
		UsesEncode:  true,
		Options: []Option{
			{Key: "look", Label: "Look", Kind: OptSelect, Default: "bluediamond", Choices: []Choice{
				{"bluediamond", "Blue Diamond", "Cool cinematic"},
				{"warm", "Warm", "Golden hour"},
				{"noir", "Noir", "High contrast mono"},
				{"vivid", "Vivid", "Boosted saturation"},
				{"faded", "Faded", "Washed film"},
			}},
			{Key: "strength", Label: "Strength", Kind: OptNumber, Default: "1.0",
				Min: 0.2, Max: 2.0, Step: 0.1, Unit: "x", Advanced: true},
			qualityOption(),
		},
		Build: buildColorFilter,
	}
}

// buildColorFilter renders the selected grade.
func buildColorFilter(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	strength := optFloat(in.Options, "strength", 1.0)
	look := optStr(in.Options, "look", "bluediamond")
	filter, ok := colorLook(look, strength)
	if !ok {
		return Plan{}, fmt.Errorf("unknown colour look %q", look)
	}

	args := decodeArgs(enc)
	args = append(args, "-i", in.Path, "-vf", filter)
	args = append(args, videoEncodeArgs(enc)...)
	args = append(args, audioArgs(enc)...)
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// colorLook returns the filter expression for a named grade.
func colorLook(name string, s float64) (string, bool) {
	switch name {
	case "bluediamond":
		return fmt.Sprintf(
			"colorbalance=bs=%.2f:bm=%.2f:bh=%.2f,colorlevels=rimin=%.2f:gimin=%.2f:bimin=%.2f",
			0.3*s, 0.3*s, 0.3*s, 0.2*s, 0.2*s, 0.4*s), true
	case "warm":
		return fmt.Sprintf("colorbalance=rs=%.2f:rm=%.2f:bh=%.2f,eq=saturation=%.2f",
			0.25*s, 0.15*s, -0.15*s, 1.0+0.15*s), true
	case "noir":
		return fmt.Sprintf("hue=s=0,eq=contrast=%.2f:brightness=%.2f", 1.0+0.4*s, -0.02*s), true
	case "vivid":
		return fmt.Sprintf("eq=saturation=%.2f:contrast=%.2f", 1.0+0.5*s, 1.0+0.1*s), true
	case "faded":
		return fmt.Sprintf("curves=all='0/0.08 1/0.92',eq=saturation=%.2f", 1.0-0.3*s), true
	}
	return "", false
}
