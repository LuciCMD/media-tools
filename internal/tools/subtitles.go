package tools

import (
	"errors"
	"fmt"
	"strconv"
)

// subtitleTools lists the subtitle group, most used first.
func subtitleTools() []Tool {
	return []Tool{
		transcribeTool(),
		burnSubsTool(),
		extractSubsTool(),
	}
}

// transcribeTool writes subtitles from speech with whisper.cpp, which runs
// inside ffmpeg on this machine.
func transcribeTool() Tool {
	return Tool{
		ID:          "subtitles.transcribe",
		Icon:        "transcribe",
		Name:        "Transcribe to Subtitles",
		Category:    CatSubtitle,
		Description: "Write subtitles from the speech in a video or recording. It runs on this computer; nothing is uploaded.",
		Input:       InputFiles,
		Accept:      soundExts,
		Requires:    RequireSpeechModel,
		Options: []Option{
			{Key: "language", Label: "Language", Kind: OptSelect, Default: "auto", Choices: languageChoices(),
				Help: "Naming the language is faster and more reliable than detecting it."},
			{Key: "translate", Label: "Translate to English", Kind: OptBool, Default: "false",
				Help: "Writes English subtitles whatever language is spoken."},
			{Key: "format", Label: "Output Format", Kind: OptSelect, Default: "srt", Choices: []Choice{
				{"srt", "SRT", "Timed subtitles"},
				{"txt", "Text", "A plain transcript"},
			}},
			{Key: "maxlen", Label: "Longest Line", Kind: OptNumber, Default: "0", Min: 0, Max: 200, Step: 1,
				Unit: "chars", Advanced: true, Help: "0 keeps whole sentences together. Around 42 suits subtitles on a TV."},
		},
		Build: buildTranscribe,
	}
}

// buildTranscribe renders the whisper filter, which writes the subtitle file
// itself; ffmpeg's own output is discarded.
func buildTranscribe(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	if enc.SpeechModel == "" {
		return Plan{}, errors.New("download a speech model in Settings first")
	}
	format := "srt"
	if optStr(in.Options, "format", "srt") == "txt" {
		format = "text"
	}

	filter := whisperFilter(enc.SpeechModel, optStr(in.Options, "language", "auto"),
		optBool(in.Options, "translate"), in.Output, format, optInt(in.Options, "maxlen", 0))

	args := []string{"-i", in.Path, "-vn", "-af", filter, "-f", "null", "-"}
	return Plan{Args: args, OutputPath: in.Output, NeedsProbe: true}, nil
}

// languageChoices are the spoken languages offered for transcription.
func languageChoices() []Choice {
	return []Choice{
		{"auto", "Detect Automatically", ""},
		{"en", "English", ""},
		{"es", "Spanish", ""},
		{"fr", "French", ""},
		{"de", "German", ""},
		{"it", "Italian", ""},
		{"pt", "Portuguese", ""},
		{"nl", "Dutch", ""},
		{"pl", "Polish", ""},
		{"ru", "Russian", ""},
		{"uk", "Ukrainian", ""},
		{"tr", "Turkish", ""},
		{"ar", "Arabic", ""},
		{"hi", "Hindi", ""},
		{"ja", "Japanese", ""},
		{"ko", "Korean", ""},
		{"zh", "Chinese", ""},
	}
}

// whisperFilter renders ffmpeg's whisper filter, which writes its subtitles
// to destination itself. maxLen 0 keeps whole segments together.
func whisperFilter(model, language string, translate bool, destination, format string, maxLen int) string {
	filter := fmt.Sprintf("whisper=model=%s:language=%s:translate=%s:destination=%s:format=%s",
		escapeFilterValue(model), language, strconv.FormatBool(translate),
		escapeFilterValue(destination), format)
	if maxLen > 0 {
		filter += fmt.Sprintf(":max_len=%d", maxLen)
	}
	return filter
}
