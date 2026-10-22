package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// soundExts is every file whose sound track the clean-up tools can work on:
// audio files, and videos, whose picture is copied untouched.
var soundExts = append(append([]string{}, audioExts...), videoExts...)

// keepFormatChoice leads each clean-up tool's format list.
var keepFormatChoice = Choice{"keep", "Same as Original", "Videos always keep theirs"}

// reduceNoiseTool removes steady background noise and mains hum.
func reduceNoiseTool() Tool {
	return Tool{
		ID:          "audio.denoise",
		Icon:        "noise",
		Name:        "Reduce Noise",
		Category:    CatAudio,
		Description: "Take out steady hiss, fan noise and mains hum. Videos keep their picture untouched.",
		Input:       InputFiles,
		Accept:      soundExts,
		Suffix:      "_clean",
		// A video's sound track is replaced in place, so it stays a video.
		VideoKeepsFormat: true,
		Options: []Option{
			{Key: "strength", Label: "Strength", Kind: OptSelect, Default: "12", Choices: []Choice{
				{"6", "Light", "Keeps the most detail"},
				{"12", "Medium", "Recommended"},
				{"20", "Strong", "Can sound watery"},
			}},
			{Key: "hum", Label: "Remove Hum", Kind: OptSelect, Default: "off", Choices: []Choice{
				{"off", "Off", ""},
				{"50", "50 Hz", "Europe, Asia, Africa"},
				{"60", "60 Hz", "The Americas"},
			}, Help: "Hum follows the local mains frequency."},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "keep", Choices: []Choice{
				keepFormatChoice,
				{"flac", "FLAC", "Lossless"},
				{"wav", "WAV", ""},
				{"mp3", "MP3", ""},
			}, Advanced: true},
		},
		Build: buildReduceNoise,
	}
}

// buildReduceNoise renders the hum notches and the FFT noise reducer.
func buildReduceNoise(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	strength := optInt(in.Options, "strength", 12)
	filters := make([]string, 0, 5)
	if hum := optInt(in.Options, "hum", 0); hum > 0 {
		// A gentle high-pass for rumble, then notches at the hum and its first
		// two harmonics, where most of its energy sits.
		filters = append(filters, "highpass=f=40")
		for h := 1; h <= 3; h++ {
			filters = append(filters, fmt.Sprintf("bandreject=f=%d:width_type=h:w=4", hum*h))
		}
	}
	// tn tracks the noise floor as it changes rather than assuming it fixed.
	filters = append(filters, fmt.Sprintf("afftdn=nr=%d:nf=-30:tn=1", strength))
	return soundTrackPlan(in, strings.Join(filters, ",")), nil
}

// enhanceDialogueTool lifts voices over music and effects.
func enhanceDialogueTool() Tool {
	return Tool{
		ID:          "audio.dialogue",
		Icon:        "voice",
		Name:        "Enhance Dialogue",
		Category:    CatAudio,
		Description: "Make voices clearer over music and effects, as in a film with quiet talking and loud action.",
		Input:       InputFiles,
		Accept:      soundExts,
		Suffix:      "_dialogue",
		// A video's sound track is replaced in place, so it stays a video.
		VideoKeepsFormat: true,
		Options: []Option{
			{Key: "strength", Label: "Strength", Kind: OptSelect, Default: "2", Choices: []Choice{
				{"1", "Light", ""},
				{"2", "Medium", "Recommended"},
				{"3", "Strong", ""},
			}},
			{Key: "level", Label: "Even Out Volume", Kind: OptBool, Default: "true",
				Help: "Brings quiet scenes up and loud ones down, so you don't reach for the volume."},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "keep", Choices: []Choice{
				keepFormatChoice,
				{"flac", "FLAC", "Lossless"},
				{"wav", "WAV", ""},
				{"mp3", "MP3", ""},
			}, Advanced: true, Help: "Surround sound comes out as stereo."},
		},
		Build: buildEnhanceDialogue,
	}
}

// buildEnhanceDialogue downmixes to stereo, extracts and boosts the centred
// voice with dialoguenhance, folds its centre channel back into stereo, and
// optionally evens out the loudness.
func buildEnhanceDialogue(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	strength := optInt(in.Options, "strength", 2)
	filters := []string{
		"aformat=channel_layouts=stereo",
		fmt.Sprintf("dialoguenhance=original=1:enhance=%d:voice=2", strength),
		// dialoguenhance outputs three channels; the centre is shared out
		// equally at -3 dB so the voice stays in the middle.
		"pan=stereo|FL<FL+0.707*FC|FR<FR+0.707*FC",
	}
	if optBool(in.Options, "level") {
		filters = append(filters, "dynaudnorm=f=250:g=15:p=0.95")
	}
	return soundTrackPlan(in, strings.Join(filters, ",")), nil
}

// soundTrackPlan applies an audio filter chain to a file. A video keeps its
// picture and subtitles as they are, with only the first sound track
// processed; an audio file is written in the chosen format.
func soundTrackPlan(in Inputs, filter string) Plan {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(in.Path)), ".")
	args := []string{"-i", in.Path}
	if isVideoExt(ext) {
		args = append(args, "-map", "0:v?", "-map", "0:a:0", "-map", "0:s?",
			"-c:v", "copy", "-c:s", "copy", "-af", filter)
		args = append(args, videoContainerAudioArgs(ext)...)
	} else {
		format := optStr(in.Options, "format", "keep")
		if format == "keep" {
			format = ext
		}
		args = append(args, "-vn", "-af", filter)
		args = append(args, sameFormatAudioArgs(format)...)
	}
	args = append(args, "-y", in.Output)
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}
}

// isVideoExt reports whether ext names a video container.
func isVideoExt(ext string) bool {
	for _, v := range videoExts {
		if v == ext {
			return true
		}
	}
	return false
}

// videoContainerAudioArgs picks an audio codec each video container accepts.
func videoContainerAudioArgs(ext string) []string {
	if ext == "webm" {
		return []string{"-c:a", "libopus", "-b:a", "160k"}
	}
	return []string{"-c:a", "aac", "-b:a", "192k"}
}

// sameFormatAudioArgs encodes audio in the format its extension names, at a
// generous bitrate for the lossy ones.
func sameFormatAudioArgs(ext string) []string {
	switch ext {
	case "aac":
		return []string{"-c:a", "aac", "-b:a", "192k"}
	case "aiff":
		return []string{"-c:a", "pcm_s16be"}
	case "wma":
		return []string{"-c:a", "wmav2", "-b:a", "192k"}
	}
	return codecArgsFor(ext, "192")
}
