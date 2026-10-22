package tools

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Bounds on joining (Power of Ten rule 2).
const (
	MinJoinClips = 2
	MaxJoinClips = 100
)

// changeContainerTool moves streams into another container without
// re-encoding them.
func changeContainerTool() Tool {
	return Tool{
		ID:          "video.remux",
		Icon:        "box",
		Name:        "Change Container",
		Category:    CatVideo,
		Description: "Move a video into another file type without re-encoding. Instant, and nothing is lost.",
		Input:       InputFiles,
		Accept:      videoExts,
		Options: []Option{
			{Key: "format", Label: "Container", Kind: OptSelect, Default: "mp4", Choices: []Choice{
				{"mp4", "MP4", "Plays almost anywhere"},
				{"mkv", "MKV", "Holds any codec"},
				{"mov", "MOV", "Apple and editors"},
				{"webm", "WebM", "VP9 or AV1 video only"},
			}},
			{Key: "subs", Label: "Keep Subtitles", Kind: OptBool, Default: "true",
				Help: "MP4 and MOV only take text subtitles. Turn this off if a picture-based track stops the move."},
		},
		Build: buildChangeContainer,
	}
}

// buildChangeContainer copies every video and audio stream, and subtitles
// when asked, converting only the subtitle format each container requires.
func buildChangeContainer(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	format := optStr(in.Options, "format", "mp4")
	args := []string{"-i", in.Path, "-map", "0:v", "-map", "0:a?"}
	if optBool(in.Options, "subs") {
		args = append(args, "-map", "0:s?")
	}
	if format == "mkv" {
		// Attachments are usually the fonts an ASS subtitle track needs.
		args = append(args, "-map", "0:t?")
	}
	args = append(args, "-c", "copy")
	args = append(args, containerSubtitleArgs(format)...)
	if format == "mp4" || format == "mov" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, "-y", in.Output)
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// containerSubtitleArgs converts subtitles to the one text format a
// container accepts. Converting subtitle text is not re-encoding the picture.
func containerSubtitleArgs(format string) []string {
	switch format {
	case "mp4", "mov":
		return []string{"-c:s", "mov_text"}
	case "webm":
		return []string{"-c:s", "webvtt"}
	}
	return nil
}

// joinClipsTool puts several clips end to end in one file.
func joinClipsTool() Tool {
	return Tool{
		ID:          "video.join",
		Icon:        "join",
		Name:        "Join Clips",
		Category:    CatVideo,
		Description: "Put several clips end to end in one file, in the order listed.",
		Input:       InputFiles,
		Accept:      videoExts,
		Suffix:      "_joined",
		UsesEncode:  true,
		Combine:     true,
		Options: []Option{
			{Key: "method", Label: "Method", Kind: OptSelect, Default: "copy", Choices: []Choice{
				{"copy", "Copy", "Instant; clips must match"},
				{"encode", "Re-Encode", "Any clips, slower"},
			}, Help: "Copy needs every clip to share a format, size and frame rate, as clips from one camera do. Re-Encode needs every clip to have sound."},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "keep", Choices: []Choice{
				{"keep", "Same as First Clip", ""},
				{"mp4", "MP4", ""},
				{"mkv", "MKV", ""},
			}},
			{Key: "resolution", Label: "Resolution", Kind: OptSelect, Default: "1920:1080", Choices: []Choice{
				{"3840:2160", "2160p", "4K UHD"},
				{"2560:1440", "1440p", ""},
				{"1920:1080", "1080p", ""},
				{"1280:720", "720p", ""},
				{"1080:1920", "1080 × 1920", "Vertical"},
			}, Advanced: true, Help: "When re-encoding. Clips of another shape get bars rather than being stretched."},
			{Key: "fps", Label: "Frame Rate", Kind: OptSelect, Default: "30", Choices: []Choice{
				{"24", "24 fps", ""},
				{"25", "25 fps", ""},
				{"30", "30 fps", ""},
				{"60", "60 fps", ""},
			}, Advanced: true, Help: "When re-encoding."},
		},
		Build: buildJoinClips,
	}
}

// buildJoinClips renders either a stream-copy join through the concat
// demuxer or a re-encode through the concat filter.
func buildJoinClips(in Inputs, enc Encode) (Plan, error) {
	if strings.TrimSpace(in.Output) == "" {
		return Plan{}, errors.New("no output path resolved")
	}
	if len(in.Paths) < MinJoinClips {
		return Plan{}, errors.New("choose at least two clips to join")
	}
	if len(in.Paths) > MaxJoinClips {
		return Plan{}, fmt.Errorf("join at most %d clips at once", MaxJoinClips)
	}
	if optStr(in.Options, "method", "copy") == "encode" {
		return joinByEncoding(in, enc)
	}
	return joinByCopying(in), nil
}

// joinByCopying writes a concat list beside the output and copies streams.
func joinByCopying(in Inputs) Plan {
	list := in.Output + ".list.txt"
	args := []string{"-f", "concat", "-safe", "0", "-i", list, "-map", "0", "-c", "copy"}
	args = append(args, fastStartArgs(in.Output)...)
	args = append(args, "-y", in.Output)
	return Plan{
		Args:       args,
		OutputPath: in.Output,
		NeedsProbe: true,
		Files:      []File{{Path: list, Content: concatList(in.Paths)}},
	}
}

// concatList renders the concat demuxer's list. Each path is single-quoted,
// and a quote inside a name is written as '\” — close, escaped quote, reopen.
func concatList(paths []string) string {
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for _, p := range paths {
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(p, "'", `'\''`))
		b.WriteString("'\n")
	}
	return b.String()
}

// joinByEncoding fits every clip to one size and frame rate and joins them
// with the concat filter, so clips from different sources can be combined.
func joinByEncoding(in Inputs, enc Encode) (Plan, error) {
	res := optStr(in.Options, "resolution", "1920:1080")
	dims := strings.SplitN(res, ":", 2)
	if len(dims) != 2 {
		return Plan{}, fmt.Errorf("malformed resolution %q", res)
	}
	fps := optStr(in.Options, "fps", "30")

	args := make([]string, 0, 2*len(in.Paths)+24)
	var graph, joined strings.Builder
	for i, p := range in.Paths {
		args = append(args, "-i", p)
		fmt.Fprintf(&graph,
			"[%d:v]scale=%s:%s:force_original_aspect_ratio=decrease,pad=%s:%s:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%s,format=yuv420p[v%d];"+
				"[%d:a]aformat=sample_rates=48000:channel_layouts=stereo[a%d];",
			i, dims[0], dims[1], dims[0], dims[1], fps, i, i, i)
		fmt.Fprintf(&joined, "[v%d][a%d]", i, i)
	}
	fmt.Fprintf(&graph, "%sconcat=n=%d:v=1:a=1[v][a]", joined.String(), len(in.Paths))

	args = append(args, "-filter_complex", graph.String(), "-map", "[v]", "-map", "[a]")
	args = append(args, videoEncodeArgs(enc)...)
	args = append(args, audioArgs(enc)...)
	args = append(args, fastStartArgs(in.Output)...)
	args = append(args, "-y", in.Output)
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// fastStartArgs moves an MP4 or MOV index to the front of the file, so it can
// start playing before it has fully downloaded. Other containers don't take
// the flag.
func fastStartArgs(output string) []string {
	switch strings.ToLower(filepath.Ext(output)) {
	case ".mp4", ".mov", ".m4v":
		return []string{"-movflags", "+faststart"}
	}
	return nil
}
