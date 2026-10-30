package tools

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
)

// imageExts is the accepted input set for image tools.
var imageExts = []string{"png", "jpg", "jpeg", "bmp", "webp", "gif", "tiff", "tif", "ico", "avif"}

// imageTools declares every image operation.
func imageTools() []Tool {
	return []Tool{
		convertImageTool(),
		heicTool(),
		resizeImageTool(),
		imagesToGIFTool(),
		icoTool(),
		stripMetadataTool(),
		watermarkTool(),
		optimizeImageTool(),
	}
}

// convertImageTool re-encodes images into another format.
func convertImageTool() Tool {
	return Tool{
		ID:          "image.convert",
		Icon:        "convert",
		Name:        "Convert Image",
		Category:    CatImage,
		Description: "Re-encode images into another format, with optional random output names.",
		Input:       InputFiles,
		Accept:      imageExts,
		Options: []Option{
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "png", Choices: []Choice{
				{"png", "PNG", "Lossless, transparency"},
				{"jpg", "JPG", "Small, no transparency"},
				{"webp", "WebP", "Small, transparency"},
				{"bmp", "BMP", "Uncompressed"},
				{"tiff", "TIFF", "Archival"},
				{"avif", "AVIF", "Most efficient"},
			}},
			{Key: "quality", Label: "Quality", Kind: OptNumber, Default: "92", Min: 1, Max: 100, Step: 1,
				Help: "Applies to the lossy formats JPG, WebP and AVIF."},
			{Key: "rename", Label: "Randomise Output Names", Kind: OptBool, Default: "false",
				Help: "Write each image under a random name instead of the original one."},
		},
		Build: buildImageConvert,
	}
}

// buildImageConvert renders an image format conversion.
func buildImageConvert(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	format := optStr(in.Options, "format", "png")
	quality := optInt(in.Options, "quality", 92)

	args := []string{"-i", in.Path}
	args = append(args, imageCodecArgs(format, quality)...)
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: false}, nil
}

// imageCodecArgs maps an image format onto encoder flags.
//
// ffmpeg takes JPEG and WebP quality on inverted or offset scales, so the
// familiar 1-100 slider is converted here rather than exposed raw.
func imageCodecArgs(format string, quality int) []string {
	switch format {
	case "png":
		return []string{"-c:v", "png", "-pix_fmt", "rgba"}
	case "jpg", "jpeg":
		// ffmpeg -q:v runs 2 (best) to 31 (worst), so the 1-100 slider is
		// mapped across that span inclusively at both ends.
		scaled := 2 + (100-quality)*29/99
		return []string{"-c:v", "mjpeg", "-q:v", fmt.Sprintf("%d", clampInt(scaled, 2, 31)), "-pix_fmt", "yuvj420p"}
	case "webp":
		return []string{"-c:v", "libwebp", "-quality", fmt.Sprintf("%d", quality), "-pix_fmt", "yuva420p"}
	case "avif":
		return []string{"-c:v", "libaom-av1", "-crf", fmt.Sprintf("%d", clampInt(63-quality*63/100, 0, 63)), "-still-picture", "1"}
	case "bmp":
		return []string{"-c:v", "bmp"}
	case "tiff":
		return []string{"-c:v", "tiff"}
	}
	return []string{"-c:v", "png"}
}

// resizeImageTool rescales images to a bounding box.
func resizeImageTool() Tool {
	return Tool{
		ID:          "image.resize",
		Icon:        "resize",
		Name:        "Resize Images",
		Category:    CatImage,
		Description: "Scale images down to fit a maximum width or height, preserving aspect ratio.",
		Input:       InputFiles,
		Accept:      imageExts,
		Suffix:      "_resized",
		Options: []Option{
			{Key: "mode", Label: "Fit To", Kind: OptSelect, Default: "width", Choices: []Choice{
				{"width", "Maximum Width", ""},
				{"height", "Maximum Height", ""},
				{"box", "Fit Inside a Box", ""},
			}},
			{Key: "width", Label: "Width", Kind: OptNumber, Default: "1920", Min: 16, Max: 16384, Step: 1, Unit: "px"},
			{Key: "height", Label: "Height", Kind: OptNumber, Default: "1080", Min: 16, Max: 16384, Step: 1, Unit: "px"},
			{Key: "noupscale", Label: "Never Enlarge", Kind: OptBool, Default: "true",
				Help: "Leave images already smaller than the target untouched."},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "keep", Choices: []Choice{
				{"keep", "Keep Source Format", ""},
				{"png", "PNG", ""},
				{"jpg", "JPG", ""},
				{"webp", "WebP", ""},
			}},
		},
		Build: buildResizeImage,
	}
}

// buildResizeImage renders the scale filter for the chosen fit mode.
func buildResizeImage(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	scale := scaleExpression(in.Options)
	args := []string{"-i", in.Path, "-vf", scale + ":flags=lanczos", "-y", in.Output}

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: false}, nil
}

// scaleExpression builds the scale filter, honouring the no-upscale rule.
func scaleExpression(opts map[string]string) string {
	width := optInt(opts, "width", 1920)
	height := optInt(opts, "height", 1080)
	noUpscale := optBool(opts, "noupscale")

	switch optStr(opts, "mode", "width") {
	case "height":
		if noUpscale {
			return fmt.Sprintf("scale=-2:'min(%d,ih)'", height)
		}
		return fmt.Sprintf("scale=-2:%d", height)
	case "box":
		if noUpscale {
			return fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", width, height)
		}
		return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", width, height)
	}
	if noUpscale {
		return fmt.Sprintf("scale='min(%d,iw)':-2", width)
	}
	return fmt.Sprintf("scale=%d:-2", width)
}

// icoTool builds a Windows icon from an image.
func icoTool() Tool {
	return Tool{
		ID:          "image.ico",
		Icon:        "icon",
		Name:        "Create Icon",
		Category:    CatImage,
		Description: "Build a Windows .ico file at a standard icon size.",
		Input:       InputFiles,
		Accept:      []string{"png", "jpg", "jpeg", "bmp", "webp"},
		OutputExt:   "ico",
		Options: []Option{
			{Key: "size", Label: "Icon Size", Kind: OptSelect, Default: "256", Choices: []Choice{
				{"16", "16 x 16", ""},
				{"32", "32 x 32", ""},
				{"48", "48 x 48", ""},
				{"128", "128 x 128", ""},
				{"256", "256 x 256", "Recommended"},
			}},
		},
		Build: buildICO,
	}
}

// buildICO renders a square icon at the requested size.
func buildICO(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	size := optStr(in.Options, "size", "256")
	filter := fmt.Sprintf("scale=%s:%s:force_original_aspect_ratio=decrease,"+
		"pad=%s:%s:(ow-iw)/2:(oh-ih)/2:color=#00000000", size, size, size, size)

	args := []string{"-i", in.Path, "-vf", filter, "-y", in.Output}
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: false}, nil
}

// stripMetadataTool removes embedded metadata such as EXIF and GPS tags.
func stripMetadataTool() Tool {
	return Tool{
		ID:          "image.stripmeta",
		Icon:        "eraser",
		Name:        "Strip Metadata",
		Category:    CatImage,
		Description: "Remove EXIF, GPS and other embedded metadata before sharing an image.",
		Input:       InputFiles,
		Accept:      imageExts,
		Suffix:      "_clean",
		Options: []Option{
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "keep", Choices: []Choice{
				{"keep", "Keep Source Format", ""},
				{"png", "PNG", ""},
				{"jpg", "JPG", ""},
			}},
		},
		Build: buildStripMeta,
	}
}

// buildStripMeta renders a metadata-free re-encode.
func buildStripMeta(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	args := []string{
		"-i", in.Path,
		"-map_metadata", "-1",
		"-map", "0:v",
		"-y", in.Output,
	}
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: false}, nil
}

// watermarkTool overlays text onto an image.
func watermarkTool() Tool {
	return Tool{
		ID:          "image.watermark",
		Icon:        "text",
		Name:        "Add Watermark",
		Category:    CatImage,
		Description: "Overlay a line of text onto an image at a chosen corner and opacity.",
		Input:       InputFiles,
		Accept:      imageExts,
		Suffix:      "_wm",
		Options: []Option{
			{Key: "text", Label: "Watermark Text", Kind: OptText, Default: "watermark"},
			{Key: "position", Label: "Position", Kind: OptSelect, Default: "br", Choices: []Choice{
				{"tl", "Top Left", ""},
				{"tr", "Top Right", ""},
				{"bl", "Bottom Left", ""},
				{"br", "Bottom Right", ""},
				{"c", "Centre", ""},
			}},
			{Key: "size", Label: "Font Size", Kind: OptNumber, Default: "32", Min: 8, Max: 200, Step: 1, Unit: "px"},
			{Key: "opacity", Label: "Opacity", Kind: OptNumber, Default: "0.6", Min: 0.05, Max: 1.0, Step: 0.05},
			{Key: "color", Label: "Colour", Kind: OptSelect, Default: "white", Choices: []Choice{
				{"white", "White", ""},
				{"black", "Black", ""},
				{"red", "Red", ""},
				{"yellow", "Yellow", ""},
			}},
		},
		Build: buildWatermark,
	}
}

// buildWatermark renders the drawtext overlay.
func buildWatermark(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	text := optStr(in.Options, "text", "watermark")
	size := optInt(in.Options, "size", 32)
	opacity := optFloat(in.Options, "opacity", 0.6)
	color := optStr(in.Options, "color", "white")
	x, y := watermarkAnchor(optStr(in.Options, "position", "br"), size)

	filter := fmt.Sprintf("drawtext=%stext=%s:expansion=none:fontsize=%d:fontcolor=%s@%.2f:x=%s:y=%s:"+
		"shadowcolor=black@0.5:shadowx=2:shadowy=2",
		fontOption(), escapeFilterValue(text), size, color, opacity, x, y)

	args := []string{"-i", in.Path, "-vf", filter, "-y", in.Output}
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: false}, nil
}

// watermarkAnchor maps a corner name onto drawtext x/y expressions.
func watermarkAnchor(position string, size int) (string, string) {
	margin := size / 2
	switch position {
	case "tl":
		return fmt.Sprintf("%d", margin), fmt.Sprintf("%d", margin)
	case "tr":
		return fmt.Sprintf("w-tw-%d", margin), fmt.Sprintf("%d", margin)
	case "bl":
		return fmt.Sprintf("%d", margin), fmt.Sprintf("h-th-%d", margin)
	case "c":
		return "(w-tw)/2", "(h-th)/2"
	}
	return fmt.Sprintf("w-tw-%d", margin), fmt.Sprintf("h-th-%d", margin)
}

// fontCandidates are common system fonts, in order of preference, for
// each platform.
var fontCandidates = map[string][]string{
	"windows": {"Fonts/segoeui.ttf", "Fonts/arial.ttf", "Fonts/tahoma.ttf"},
	"darwin": {"/System/Library/Fonts/Supplemental/Arial.ttf", "/Library/Fonts/Arial.ttf",
		"/System/Library/Fonts/Helvetica.ttc"},
	"linux": {"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/usr/share/fonts/TTF/DejaVuSans.ttf",
		"/usr/share/fonts/dejavu/DejaVuSans.ttf", "/usr/share/fonts/noto/NotoSans-Regular.ttf",
		"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf"},
}

// fontOption names a font file for drawtext. Without one, drawtext asks
// fontconfig, which most Windows builds of ffmpeg ship unconfigured, and
// ffmpeg crashes. When no known font exists it is left to fontconfig.
func fontOption() string {
	for _, candidate := range fontCandidates[goruntime.GOOS] {
		path := candidate
		if goruntime.GOOS == "windows" {
			path = filepath.Join(os.Getenv("WINDIR"), candidate)
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return "fontfile=" + escapeFilterValue(filepath.ToSlash(path)) + ":"
		}
	}
	return ""
}

// optimizeImageTool re-encodes images for the smallest reasonable size.
func optimizeImageTool() Tool {
	return Tool{
		ID:          "image.optimize",
		Icon:        "compress",
		Name:        "Optimise Size",
		Category:    CatImage,
		Description: "Re-encode images at maximum compression effort to reduce file size.",
		Input:       InputFiles,
		Accept:      imageExts,
		Suffix:      "_opt",
		Options: []Option{
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "webp", Choices: []Choice{
				{"webp", "WebP", "Best size for the web"},
				{"png", "PNG", "Lossless, maximum effort"},
				{"jpg", "JPG", "Photographic"},
				{"avif", "AVIF", "Smallest, slower"},
			}},
			{Key: "quality", Label: "Quality", Kind: OptNumber, Default: "82", Min: 1, Max: 100, Step: 1,
				Help: "Ignored for PNG, which is always lossless."},
		},
		Build: buildOptimizeImage,
	}
}

// buildOptimizeImage renders a maximum-effort re-encode.
func buildOptimizeImage(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	format := optStr(in.Options, "format", "webp")
	quality := optInt(in.Options, "quality", 82)

	args := []string{"-i", in.Path}
	if format == "png" {
		args = append(args, "-c:v", "png", "-compression_level", "100", "-pred", "mixed")
	} else {
		args = append(args, imageCodecArgs(format, quality)...)
	}
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: false}, nil
}

// clampInt constrains v to the inclusive range lo..hi.
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// heicExts are the High Efficiency Image File names phones write.
var heicExts = []string{"heic", "heif", "hif"}

// heicTool converts phone photos in HEIC into formats that open everywhere.
func heicTool() Tool {
	return Tool{
		ID:          "image.heic",
		Icon:        "phone",
		Name:        "HEIC to JPG",
		Category:    CatImage,
		Description: "Turn iPhone and other HEIC photos into JPGs that open everywhere.",
		Input:       InputFiles,
		Accept:      heicExts,
		Options: []Option{
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "jpg", Choices: []Choice{
				{"jpg", "JPG", "Opens everywhere"},
				{"png", "PNG", "Lossless, larger"},
			}},
			{Key: "quality", Label: "Quality", Kind: OptNumber, Default: "92", Min: 1, Max: 100, Step: 1,
				Help: "Applies to JPG."},
		},
		Build: buildHEIC,
	}
}

// buildHEIC decodes the photo, which ffmpeg assembles from its tiles, and
// writes one still image.
func buildHEIC(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	format := optStr(in.Options, "format", "jpg")
	args := []string{"-i", in.Path, "-frames:v", "1", "-update", "1"}
	args = append(args, imageCodecArgs(format, optInt(in.Options, "quality", 92))...)
	args = append(args, "-y", in.Output)
	return Plan{Args: args, OutputPath: in.Output}, nil
}
