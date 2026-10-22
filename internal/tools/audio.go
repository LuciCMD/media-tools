package tools

import (
	"fmt"
	"strconv"
	"strings"
)

// audioExts is the accepted input set for audio tools.
var audioExts = []string{"mp3", "flac", "wav", "ogg", "m4a", "aac", "aiff", "opus", "wma"}

// audioTools declares every audio operation.
func audioTools() []Tool {
	return []Tool{
		convertAudioTool(),
		softenTool(),
		enhanceDialogueTool(),
		reduceNoiseTool(),
		normalizeTool(),
		trimSilenceTool(),
		extractAudioTool(),
		audioSpeedTool(),
	}
}

// convertAudioTool re-encodes audio into another container and codec.
func convertAudioTool() Tool {
	return Tool{
		ID:          "audio.convert",
		Icon:        "convert",
		Name:        "Convert Audio",
		Category:    CatAudio,
		Description: "Re-encode audio into another format with an explicit quality setting.",
		Input:       InputFiles,
		Accept:      audioExts,
		Options: []Option{
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "mp3", Choices: []Choice{
				{"mp3", "MP3", "Lossy, universal"},
				{"flac", "FLAC", "Lossless"},
				{"wav", "WAV", "Uncompressed PCM"},
				{"ogg", "OGG Vorbis", "Lossy, open"},
				{"opus", "Opus", "Lossy, most efficient"},
				{"m4a", "M4A / AAC", "Lossy, Apple"},
			}},
			{Key: "bitrate", Label: "Bitrate", Kind: OptSelect, Default: "192", Choices: []Choice{
				{"128", "128 kbps", "Small"},
				{"192", "192 kbps", "Balanced"},
				{"256", "256 kbps", "High"},
				{"320", "320 kbps", "Maximum"},
			}, Help: "Ignored for the lossless formats FLAC and WAV."},
			{Key: "samplerate", Label: "Sample Rate", Kind: OptSelect, Default: "keep", Choices: []Choice{
				{"keep", "Keep Source Rate", ""},
				{"44100", "44.1 kHz", "CD"},
				{"48000", "48 kHz", "Video standard"},
				{"96000", "96 kHz", "High resolution"},
			}, Advanced: true},
		},
		Build: buildAudioConvert,
	}
}

// buildAudioConvert renders the argument vector for an audio conversion.
func buildAudioConvert(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	format := optStr(in.Options, "format", "mp3")
	bitrate := optStr(in.Options, "bitrate", "192")

	args := []string{"-i", in.Path, "-vn"}
	args = append(args, codecArgsFor(format, bitrate)...)

	if rate := optStr(in.Options, "samplerate", "keep"); rate != "keep" {
		args = append(args, "-ar", rate)
	}
	args = append(args, "-y", in.Output)
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// codecArgsFor maps a container choice onto its encoder flags.
func codecArgsFor(format, bitrate string) []string {
	switch format {
	case "mp3":
		return []string{"-c:a", "libmp3lame", "-b:a", bitrate + "k"}
	case "flac":
		return []string{"-c:a", "flac", "-compression_level", "8"}
	case "wav":
		return []string{"-c:a", "pcm_s16le"}
	case "ogg":
		return []string{"-c:a", "libvorbis", "-b:a", bitrate + "k"}
	case "opus":
		return []string{"-c:a", "libopus", "-b:a", bitrate + "k"}
	case "m4a":
		return []string{"-c:a", "aac", "-b:a", bitrate + "k"}
	}
	return []string{"-c:a", "libmp3lame", "-b:a", bitrate + "k"}
}

// softenTool ports audio_soften.bat: multiband compression that tames piercing
// content while preserving perceived loudness.
func softenTool() Tool {
	return Tool{
		ID:          "audio.soften",
		Icon:        "wave",
		Name:        "Soften Harsh Audio",
		Category:    CatAudio,
		Description: "Reduce piercing and fatiguing frequencies with multiband compression, preserving loudness.",
		Input:       InputFiles,
		Accept:      audioExts,
		Suffix:      "_soft",
		Options: []Option{
			{Key: "preset", Label: "Intensity", Kind: OptSelect, Default: "balanced", Choices: []Choice{
				{"gentle", "Gentle", "Light touch"},
				{"balanced", "Balanced", "Recommended"},
				{"strong", "Strong", "Heavily fatiguing sources"},
			}},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "ogg", Choices: []Choice{
				{"ogg", "OGG Vorbis", ""},
				{"wav", "WAV", ""},
				{"flac", "FLAC", ""},
				{"mp3", "MP3", ""},
			}},
			{Key: "limit", Label: "Output Ceiling", Kind: OptNumber, Default: "0.94",
				Min: 0.5, Max: 1.0, Step: 0.01, Advanced: true,
				Help: "Peak limiter ceiling in linear scale. 0.94 leaves a little headroom."},
		},
		Build: buildSoften,
	}
}

// softenPreset holds the per-intensity filter parameters.
type softenPreset struct {
	mid    string
	high   string
	trim   string
	makeup string
}

// softenPresets mirrors the three presets from the original batch script.
func softenPresets() map[string]softenPreset {
	return map[string]softenPreset{
		"gentle": {
			mid:    "acompressor=threshold=0.10:ratio=2.5:attack=4:release=110:knee=6",
			high:   "acompressor=threshold=0.08:ratio=3:attack=2:release=80:knee=6",
			trim:   "anull",
			makeup: "1.12",
		},
		"balanced": {
			mid:    "acompressor=threshold=0.06:ratio=4:attack=3:release=90:knee=5",
			high:   "acompressor=threshold=0.04:ratio=6:attack=1:release=60:knee=5",
			trim:   "equalizer=f=6500:t=q:w=1.4:g=-2",
			makeup: "1.26",
		},
		"strong": {
			mid:    "acompressor=threshold=0.045:ratio=6:attack=2:release=90:knee=4",
			high:   "acompressor=threshold=0.03:ratio=8:attack=1:release=60:knee=4",
			trim:   "equalizer=f=3200:t=q:w=1.2:g=-2,equalizer=f=6800:t=q:w=1.3:g=-4",
			makeup: "1.41",
		},
	}
}

// buildSoften renders the multiband softening filtergraph.
func buildSoften(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	name := optStr(in.Options, "preset", "balanced")
	preset, ok := softenPresets()[name]
	if !ok {
		return Plan{}, fmt.Errorf("unknown softening preset %q", name)
	}
	limit := optFloat(in.Options, "limit", 0.94)
	if limit < 0.5 || limit > 1.0 {
		return Plan{}, fmt.Errorf("output ceiling %.2f is outside 0.50-1.00", limit)
	}

	graph := softenGraph(preset, limit)
	args := []string{"-i", in.Path, "-filter_complex", graph, "-map", "[out]"}
	args = append(args, codecArgsFor(optStr(in.Options, "format", "ogg"), "192")...)
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// softenGraph assembles the three-band crossover filtergraph.
func softenGraph(p softenPreset, limit float64) string {
	var b strings.Builder
	b.WriteString("acrossover=split=2000 6000:order=8th[low][mid][high];")
	b.WriteString("[mid]" + p.mid + "[midc];")
	b.WriteString("[high]" + p.high + "," + p.trim + "[highc];")
	b.WriteString("[low][midc][highc]amix=inputs=3:normalize=0[mixed];")
	b.WriteString("[mixed]volume=" + p.makeup + ",")
	b.WriteString("alimiter=limit=" + strconv.FormatFloat(limit, 'f', 2, 64))
	b.WriteString(":attack=5:release=50:level=disabled[out]")
	return b.String()
}

// normalizeTool applies broadcast-standard loudness normalization.
func normalizeTool() Tool {
	return Tool{
		ID:          "audio.normalize",
		Icon:        "levels",
		Name:        "Normalize Loudness",
		Category:    CatAudio,
		Description: "Bring audio to a consistent perceived loudness using the EBU R128 standard.",
		Input:       InputFiles,
		Accept:      audioExts,
		Suffix:      "_norm",
		Options: []Option{
			{Key: "target", Label: "Target Loudness", Kind: OptSelect, Default: "-16", Choices: []Choice{
				{"-14", "-14 LUFS", "Streaming (Spotify, YouTube)"},
				{"-16", "-16 LUFS", "Podcast / general"},
				{"-23", "-23 LUFS", "EBU broadcast"},
			}},
			{Key: "peak", Label: "True Peak Ceiling", Kind: OptNumber, Default: "-1.5",
				Min: -9, Max: 0, Step: 0.5, Unit: "dBTP", Advanced: true},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "flac", Choices: []Choice{
				{"flac", "FLAC", "Lossless"},
				{"wav", "WAV", ""},
				{"mp3", "MP3", ""},
				{"ogg", "OGG Vorbis", ""},
			}},
		},
		Build: buildNormalize,
	}
}

// buildNormalize renders a single-pass loudnorm filter chain.
func buildNormalize(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	target := optStr(in.Options, "target", "-16")
	peak := optFloat(in.Options, "peak", -1.5)

	filter := fmt.Sprintf("loudnorm=I=%s:TP=%.1f:LRA=11", target, peak)
	args := []string{"-i", in.Path, "-vn", "-af", filter}
	args = append(args, codecArgsFor(optStr(in.Options, "format", "flac"), "256")...)
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// trimSilenceTool removes silence from the ends, and optionally the middle.
func trimSilenceTool() Tool {
	return Tool{
		ID:          "audio.trimsilence",
		Icon:        "scissors",
		Name:        "Trim Silence",
		Category:    CatAudio,
		Description: "Strip silence from the start and end of a recording, and optionally from the middle.",
		Input:       InputFiles,
		Accept:      audioExts,
		Suffix:      "_trim",
		Options: []Option{
			{Key: "threshold", Label: "Silence Threshold", Kind: OptNumber, Default: "-50",
				Min: -90, Max: -10, Step: 1, Unit: "dB",
				Help: "Audio quieter than this counts as silence."},
			{Key: "duration", Label: "Minimum Silence", Kind: OptNumber, Default: "0.5",
				Min: 0.1, Max: 10, Step: 0.1, Unit: "s"},
			{Key: "middle", Label: "Also Remove Internal Silence", Kind: OptBool, Default: "false"},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "flac", Choices: []Choice{
				{"flac", "FLAC", "Lossless"},
				{"wav", "WAV", ""},
				{"mp3", "MP3", ""},
			}},
		},
		Build: buildTrimSilence,
	}
}

// buildTrimSilence renders the silenceremove filter chain.
func buildTrimSilence(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	threshold := optFloat(in.Options, "threshold", -50)
	duration := optFloat(in.Options, "duration", 0.5)

	filter := fmt.Sprintf(
		"silenceremove=start_periods=1:start_duration=%.2f:start_threshold=%.0fdB:detection=peak",
		duration, threshold)
	if optBool(in.Options, "middle") {
		filter += fmt.Sprintf(
			",silenceremove=stop_periods=-1:stop_duration=%.2f:stop_threshold=%.0fdB:detection=peak",
			duration, threshold)
	}
	filter += ",areverse,silenceremove=start_periods=1:start_duration=" +
		strconv.FormatFloat(duration, 'f', 2, 64) +
		fmt.Sprintf(":start_threshold=%.0fdB:detection=peak,areverse", threshold)

	args := []string{"-i", in.Path, "-vn", "-af", filter}
	args = append(args, codecArgsFor(optStr(in.Options, "format", "flac"), "256")...)
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// extractAudioTool pulls the audio track out of a video file.
func extractAudioTool() Tool {
	return Tool{
		ID:          "audio.extract",
		Icon:        "note",
		Name:        "Extract Audio",
		Category:    CatAudio,
		Description: "Pull the audio track out of a video, either copied untouched or re-encoded.",
		Input:       InputFiles,
		Accept:      []string{"mp4", "mkv", "mov", "avi", "webm", "flv", "m4v", "wmv"},
		Options: []Option{
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "flac", Choices: []Choice{
				{"copy", "Copy Without Re-Encoding", "Fastest"},
				{"flac", "FLAC", "Lossless"},
				{"mp3", "MP3", ""},
				{"wav", "WAV", ""},
				{"m4a", "M4A / AAC", ""},
			}},
			{Key: "track", Label: "Audio Track", Kind: OptNumber, Default: "0",
				Min: 0, Max: 15, Step: 1, Advanced: true,
				Help: "Which audio stream to take when the file has several."},
		},
		Build: buildExtractAudio,
	}
}

// buildExtractAudio renders an audio demux or re-encode.
func buildExtractAudio(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	track := optInt(in.Options, "track", 0)
	format := optStr(in.Options, "format", "flac")

	args := []string{"-i", in.Path, "-vn", "-map", fmt.Sprintf("0:a:%d", track)}
	if format == "copy" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, codecArgsFor(format, "256")...)
	}
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// audioSpeedTool changes playback speed with or without pitch correction.
func audioSpeedTool() Tool {
	return Tool{
		ID:          "audio.speed",
		Icon:        "gauge",
		Name:        "Change Speed",
		Category:    CatAudio,
		Description: "Speed audio up or slow it down, keeping the pitch natural by default.",
		Input:       InputFiles,
		Accept:      audioExts,
		Suffix:      "_speed",
		Options: []Option{
			{Key: "factor", Label: "Speed", Kind: OptNumber, Default: "1.25",
				Min: 0.5, Max: 4.0, Step: 0.05, Unit: "x",
				Help: "2.0 is twice as fast, 0.5 is half speed."},
			{Key: "pitch", Label: "Preserve Pitch", Kind: OptBool, Default: "true",
				Help: "When off, the audio changes pitch like a tape machine."},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "flac", Choices: []Choice{
				{"flac", "FLAC", "Lossless"},
				{"mp3", "MP3", ""},
				{"wav", "WAV", ""},
			}},
		},
		Build: buildAudioSpeed,
	}
}

// buildAudioSpeed renders the atempo or asetrate speed change.
func buildAudioSpeed(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	factor := optFloat(in.Options, "factor", 1.25)
	if factor < 0.5 || factor > 4.0 {
		return Plan{}, fmt.Errorf("speed %.2f is outside the supported 0.5-4.0 range", factor)
	}

	filter := chainTempo(factor)
	if !optBool(in.Options, "pitch") {
		filter = fmt.Sprintf("asetrate=44100*%.4f,aresample=44100", factor)
	}

	args := []string{"-i", in.Path, "-vn", "-af", filter}
	args = append(args, codecArgsFor(optStr(in.Options, "format", "flac"), "256")...)
	args = append(args, "-y", in.Output)

	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// chainTempo builds an atempo chain, since one atempo only spans 0.5x-2.0x.
func chainTempo(factor float64) string {
	parts := make([]string, 0, 4)
	remaining := factor

	// Bounded: each step halves or doubles, so 0.5-4.0 needs at most 2 stages.
	for i := 0; i < 4 && remaining > 2.0; i++ {
		parts = append(parts, "atempo=2.0")
		remaining /= 2.0
	}
	for i := 0; i < 4 && remaining < 0.5; i++ {
		parts = append(parts, "atempo=0.5")
		remaining /= 0.5
	}
	parts = append(parts, "atempo="+strconv.FormatFloat(remaining, 'f', 4, 64))
	return strings.Join(parts, ",")
}
