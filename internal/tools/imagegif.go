package tools

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// Bounds on Images to GIF
const (
	MinGIFImages = 2
	MaxGIFImages = 500
	// maxGIFSide is the largest side a GIF's header can hold
	maxGIFSide = 65535
)

// GIF frame disposal as the format numbers it, what a viewer does with a
// frame before drawing the next
const (
	DisposeKeep  = 1 // leave it, so the next is drawn over it
	DisposeClear = 2 // clear its area to transparent
)

// gifImageExts are the stills a GIF can be made from, without GIF and ICO
// since only their first picture would be used
var gifImageExts = []string{"png", "jpg", "jpeg", "bmp", "webp", "tiff", "tif", "avif"}

// imagesToGIFTool makes one animated GIF from several images in order
func imagesToGIFTool() Tool {
	return Tool{
		ID:          "image.gif",
		Icon:        "gif",
		Name:        "Images to GIF",
		Category:    CatImage,
		Description: "Make an animated GIF from several images, one frame each, in the order listed.",
		Input:       InputFiles,
		Accept:      gifImageExts,
		OutputExt:   "gif",
		Combine:     true,
		SortByName:  true,
		// The GIF takes the first image's shape, so it's read before building
		NeedsSize: true,
		Options: []Option{
			{Key: "timing", Label: "Timing", Kind: OptSelect, Default: "fps", Choices: []Choice{
				{"fps", "Frame Rate", "Images each second"},
				{"delay", "Time per Frame", "How long each image shows"},
			}, Help: "Two ways to set the same speed. A GIF counts time in hundredths of a second, so either is rounded to that."},
			{Key: "fps", Label: "Frame Rate", Kind: OptNumber, Default: "10", Min: 1, Max: 50, Step: 1, Unit: "fps",
				When: "timing=fps"},
			{Key: "delay", Label: "Time per Frame", Kind: OptNumber, Default: "100", Min: 20, Max: 10000, Step: 10, Unit: "ms",
				When: "timing=delay", Help: "Browsers slow anything under 20 ms right down, so that's the shortest."},
			{Key: "sizing", Label: "GIF Width", Kind: OptSelect, Default: gifSet, Choices: []Choice{
				{gifSet, "Set Width", ""},
				{gifOriginal, "Original", "The first image's width"},
			}, Help: "The GIF takes the first image's shape, and its height follows the width."},
			{Key: "width", Label: "Width", Kind: OptNumber, Default: "480", Min: 32, Max: 1920, Step: 2, Unit: "px",
				When: "sizing=" + gifSet},
			{Key: "fit", Label: "Fit", Kind: OptSelect, Default: gifFitBars, Choices: []Choice{
				{gifFitBars, "Add Bars", "Transparent around other shapes"},
				{gifFitCrop, "Crop to Fill", "Same size, edges trimmed"},
				{gifFitStretch, "Stretch", "Same size, shapes distorted"},
			}, Help: "What happens to an image whose shape differs from the first. Crop and Stretch make every image fill the frame."},
			{Key: "loop", Label: "Loop", Kind: OptSelect, Default: "forever", Choices: []Choice{
				{"forever", "Forever", ""},
				{"once", "Play Once", ""},
				{"count", "Set Count", ""},
			}},
			{Key: "plays", Label: "Play Count", Kind: OptNumber, Default: "3", Min: 2, Max: 100, Step: 1,
				When: "loop=count", Help: "How many times it plays in all before stopping on the last image."},
			{Key: "hold", Label: "Hold Last Frame", Kind: OptNumber, Default: "0", Min: 0, Max: 10000, Step: 10, Unit: "ms",
				Help: "Extra time on the last image before it starts again or stops. 0 shows it as long as the others."},
			{Key: "dispose", Label: "Frame Disposal", Kind: OptSelect, Default: "auto", Advanced: true, Choices: []Choice{
				{"auto", "Auto", "Clears when Fit adds bars"},
				{"clear", "Clear Each Frame", "Every image on its own"},
				{"keep", "Draw Over", "Earlier images show through"},
			}, Help: "Clearing stops an earlier image showing through a later one's transparent bars. Draw Over leaves it showing, which can make a smaller file."},
			{Key: "colors", Label: "Palette Size", Kind: OptNumber, Default: "256", Min: 8, Max: 256, Step: 8, Advanced: true,
				Help: "The colours shared by the whole GIF. Fewer makes a smaller file with more banding."},
			{Key: "dither", Label: "Dithering", Kind: OptSelect, Default: "sierra2_4a", Advanced: true, Choices: gifDitherChoices},
		},
		Build: buildImagesToGIF,
	}
}

// How images of another shape are fitted to the GIF's frame
const (
	gifFitBars    = "pad"
	gifFitCrop    = "crop"
	gifFitStretch = "stretch"
)

// buildImagesToGIF makes the GIF in three commands, every image brought to
// one size, a palette from all of them, then the frames drawn with it
func buildImagesToGIF(in Inputs, enc Encode) (Plan, error) {
	if strings.TrimSpace(in.Output) == "" {
		return Plan{}, errors.New("no output path resolved")
	}
	if len(in.Paths) < MinGIFImages {
		return Plan{}, errors.New("choose at least two images")
	}
	if len(in.Paths) > MaxGIFImages {
		return Plan{}, fmt.Errorf("a GIF can be made from at most %d images", MaxGIFImages)
	}
	w, h, err := gifCanvas(in.Options, in.Color)
	if err != nil {
		return Plan{}, err
	}
	fit := optStr(in.Options, "fit", gifFitBars)
	colors := optRound(in.Options, "colors", 256)
	dither := optStr(in.Options, "dither", "sierra2_4a")
	// The same finer pattern Convert to GIF uses
	if dither == "bayer" {
		dither = "bayer:bayer_scale=4"
	}
	disposal, flags := gifDisposal(optStr(in.Options, "dispose", "auto"), fit)
	pts, last := gifTiming(in.Options)

	// Everything sits beside the output, which is claimed for this job
	// Frames are NUT files since the image demuxer reads a % in a name as a
	// numbered sequence, and it never opens a NUT
	stem := in.Output + ".mediatools-"
	list, palette, staged := stem+"frames.txt", stem+"palette.nut", stem+"staged.gif"
	frames := make([]string, len(in.Paths))
	cmds := make([][]string, 0, len(in.Paths)+2)
	steps := make([]string, 0, len(in.Paths)+2)
	stage := gifFitStage(fit, w, h)
	for i, p := range in.Paths {
		frames[i] = fmt.Sprintf("%s%04d.nut", stem, i+1)
		cmds = append(cmds, gifPrepare(p, stage, frames[i]))
		steps = append(steps, fmt.Sprintf("Preparing image %d of %d", i+1, len(in.Paths)))
	}

	reserve := 0
	if fit == gifFitBars {
		reserve = 1
	}
	joined := []string{"-f", "concat", "-safe", "0", "-i", list}
	// Full, since diff left the last image's colours out of the palette
	cmds = append(cmds, append(append([]string{}, joined...), "-vf",
		fmt.Sprintf("palettegen=max_colors=%d:stats_mode=full:reserve_transparent=%d", colors, reserve),
		"-c:v", "ffv1", "-y", palette))
	draw := append(append([]string{}, joined...), "-i", palette,
		"-lavfi", fmt.Sprintf("[0:v]settb=1/100,setpts=%s[f];[f][1:v]paletteuse=dither=%s", pts, dither),
		"-fps_mode", "passthrough", "-enc_time_base", "1/100")
	if flags != "" {
		draw = append(draw, "-gifflags", flags)
	}
	cmds = append(cmds, append(draw, "-final_delay", strconv.Itoa(last), "-loop", gifLoop(in.Options), "-y", staged))
	steps = append(steps, "Making the palette", "Drawing the GIF")

	return Plan{
		// One command per image, so only one is ever open, where a graph
		// holding every image took about 245 MB for each 12 MP photo
		Args:       cmds[0],
		Passes:     cmds[1:],
		Files:      []File{{Path: list, Content: gifFrameList(frames)}},
		Cleanup:    append(frames, palette, staged),
		Steps:      steps,
		Staged:     staged,
		OutputPath: in.Output,
		Frames:     len(in.Paths),
		Disposal:   disposal,
	}, nil
}

// gifPrepare brings one image to the GIF's size as a single frame, the first
// of an animated one, turned upright as its orientation tag says
func gifPrepare(src, stage, out string) []string {
	var args []string
	// Read as named rather than as a numbered sequence, which AVIF never is
	ext := strings.ToLower(filepath.Ext(src))
	if strings.Contains(src, "%") && ext != ".avif" {
		args = append(args, "-f", "image2", "-pattern_type", "none")
	}
	return append(args, "-i", src, "-frames:v", "1", "-an", "-sn", "-vf", stage, "-c:v", "ffv1", "-y", out)
}

// gifFrameList is the concat demuxer's list of prepared frames, each given a
// length so their times rise, retimed when drawn
func gifFrameList(frames []string) string {
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for _, f := range frames {
		fmt.Fprintf(&b, "file '%s'\nduration 1\n", strings.ReplaceAll(f, "'", `'\''`))
	}
	return b.String()
}

// optRound reads a validated numeric option to the nearest whole number,
// since a typed 640.5 passes validation and optInt would drop it for def
func optRound(opts map[string]string, key string, def int) int {
	return int(math.Round(optFloat(opts, key, float64(def))))
}

// gifCanvas is the GIF's size, the first image's shape at the chosen width
func gifCanvas(opts map[string]string, first SourceColor) (int, int, error) {
	w0, h0 := first.Width, first.Height
	// A stand-in for the check made before the first image is read
	if w0 <= 0 || h0 <= 0 {
		w0, h0 = 4, 3
	}
	w := w0
	if optStr(opts, "sizing", gifSet) == gifSet {
		w = optRound(opts, "width", 480)
	}
	h := int(math.Max(1, math.Round(float64(w)*float64(h0)/float64(w0))))
	if w > maxGIFSide || h > maxGIFSide {
		return 0, 0, fmt.Errorf("a GIF can't be over %d px on a side, and this would be %d × %d", maxGIFSide, w, h)
	}
	return w, h, nil
}

// gifFitStage brings one image to the GIF's size
// Transparency is flattened onto black first, since a GIF pixel is either
// clear or solid and soft edges would come out jagged
func gifFitStage(fit string, w, h int) string {
	flatten := "format=rgba,premultiply=inplace=1,format=rgb24,"
	switch fit {
	case gifFitCrop:
		return flatten + fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase:flags=lanczos,crop=%d:%d,setsar=1,format=rgb24", w, h, w, h)
	case gifFitStretch:
		return flatten + fmt.Sprintf("scale=%d:%d:flags=lanczos,setsar=1,format=rgb24", w, h)
	}
	// Fitted by hand, as force_original_aspect_ratio rounds a side of a very
	// wide or tall image to 0 and ffmpeg refuses it
	fitted := fmt.Sprintf("min(%d/iw,%d/ih)", w, h)
	return flatten + fmt.Sprintf("scale=w='max(1,round(iw*%s))':h='max(1,round(ih*%s))':flags=lanczos,"+
		"format=rgba,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black@0,setsar=1,format=rgba", fitted, fitted, w, h)
}

// gifTiming gives each frame's time in hundredths of a second, as a setpts
// expression, and how long the last one shows
// A rate is spread over whole hundredths, so 30 fps alternates 3 and 4
func gifTiming(opts map[string]string) (string, int) {
	hold := int(math.Round(optFloat(opts, "hold", 0) / 10))
	if optStr(opts, "timing", "fps") == "delay" {
		cs := int(math.Max(2, math.Round(optFloat(opts, "delay", 100)/10)))
		return fmt.Sprintf("N*%d", cs), cs + hold
	}
	fps := optRound(opts, "fps", 10)
	if fps < 1 {
		fps = 1
	}
	return fmt.Sprintf("round(N*100/%d)", fps), int(math.Round(100/float64(fps))) + hold
}

// gifLoop is the GIF's loop count as ffmpeg takes it, the repeats after the
// first play, or -1 for none
func gifLoop(opts map[string]string) string {
	switch optStr(opts, "loop", "forever") {
	case "once":
		return "-1"
	case "count":
		return strconv.Itoa(optRound(opts, "plays", 3) - 1)
	}
	return "0"
}

// gifDisposal decides what a viewer does with each frame, and ffmpeg's GIF
// flags to match
// ffmpeg marks a frame to clear only when that frame has transparency, but it's
// the frame under the bars that needs clearing, so the job rewrites the marks
// Clearing needs whole frames, since ffmpeg's own diffing leaves unchanged
// pixels clear and relies on the last frame showing through them
func gifDisposal(choice, fit string) (int, string) {
	switch choice {
	case "clear":
		return DisposeClear, "0"
	case "keep":
		return DisposeKeep, ""
	}
	if fit == gifFitBars {
		return DisposeClear, "0"
	}
	// Every frame is solid, so ffmpeg's own marks are right
	return 0, ""
}
