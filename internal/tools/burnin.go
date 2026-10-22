package tools

import (
	"errors"
	"fmt"
)

// subtitleSidecars are the subtitle files ffmpeg's subtitles filter reads.
var subtitleSidecars = []string{"srt", "ass", "ssa", "vtt"}

// Where Burn In Subtitles takes its subtitles from.
const (
	fromAuto   = "auto"
	fromFile   = "file"
	fromVideo  = "video"
	fromSpeech = "speech"
)

// speechSuffix names the subtitles written from the speech for a burn-in,
// kept beside the output only while the job runs.
const speechSuffix = ".speech.srt"

// burnSubsTool draws subtitles permanently into the picture, from a file, a
// track inside the video, or the speech, written first by whisper.
func burnSubsTool() Tool {
	return Tool{
		ID:           "video.burnsubs",
		Icon:         "captions",
		Name:         "Burn In Subtitles",
		Category:     CatSubtitle,
		Description:  "Draw subtitles permanently into the picture. With none to hand, they're written from the speech first.",
		Input:        InputFile,
		Accept:       videoExts,
		Suffix:       "_subbed",
		UsesEncode:   true,
		NeedsStreams: true,
		Sidecars:     subtitleSidecars,
		Options: []Option{
			{Key: "from", Label: "Subtitles From", Kind: OptSelect, Default: fromAuto, Choices: []Choice{
				{fromAuto, "Automatic", "A file beside it, its own track, or the speech"},
				{fromFile, "Choose a File", "Any name"},
				{fromVideo, "Track in the Video", ""},
				{fromSpeech, "Write from the Speech", "Needs a speech model"},
			}, Requires: map[string]string{fromSpeech: RequireSpeechModel},
				Help: "Automatic uses a subtitle file with the video's name beside it, then a track inside the video, and otherwise writes subtitles from the speech."},
			{Key: "subfile", Label: "Subtitle File", Kind: OptFile, Accept: subtitleSidecars, When: "from=" + fromFile,
				Help: "SRT, ASS, SSA or VTT."},
			{Key: "track", Label: "Subtitle Track", Kind: OptNumber, Default: "0", Min: 0, Max: 15, Step: 1,
				When: "from=" + fromVideo, Help: "Which track inside the video, counting from 0."},
			{Key: "language", Label: "Spoken Language", Kind: OptSelect, Default: "auto", Choices: languageChoices(),
				When: "from=" + fromAuto + "|" + fromSpeech, Help: "Used when subtitles are written from the speech."},
			{Key: "translate", Label: "Translate to English", Kind: OptBool, Default: "false",
				When: "from=" + fromAuto + "|" + fromSpeech},
			{Key: "size", Label: "Font Size", Kind: OptNumber, Default: "24", Min: 8, Max: 96, Step: 1, Advanced: true},
			qualityOption(),
		},
		Build: buildBurnSubs,
	}
}

// buildBurnSubs draws the chosen subtitles into the picture, writing them
// from the speech in a first pass when that is where they come from.
func buildBurnSubs(in Inputs, enc Encode) (Plan, error) {
	if err := requireInput(in); err != nil {
		return Plan{}, err
	}
	source, err := subtitleSource(in, enc)
	if err != nil {
		return Plan{}, err
	}

	var first []string
	var cleanup []string
	subtitles := source.file
	if source.speech {
		subtitles = in.Output + speechSuffix
		first = []string{"-i", in.Path, "-vn", "-af",
			whisperFilter(enc.SpeechModel, optStr(in.Options, "language", "auto"),
				optBool(in.Options, "translate"), subtitles, "srt", 0),
			"-f", "null", "-"}
		cleanup = []string{subtitles}
	}

	// A subtitle file is read whole; a video's own track is picked by index.
	value := escapeFilterValue(subtitles)
	if source.track >= 0 {
		value = fmt.Sprintf("%s:si=%d", escapeFilterValue(in.Path), source.track)
	}
	filter := fmt.Sprintf("subtitles=%s:force_style='Fontsize=%d'", value, optInt(in.Options, "size", 24))

	burn := []string{"-i", in.Path, "-vf", filter}
	burn = append(burn, videoEncodeArgs(enc)...)
	burn = append(burn, audioArgs(enc)...)
	burn = append(burn, "-y", in.Output)

	if first == nil {
		return Plan{Args: burn, OutputPath: in.Output, NeedsProbe: true}, nil
	}
	return Plan{Args: first, Passes: [][]string{burn}, Cleanup: cleanup, OutputPath: in.Output, NeedsProbe: true}, nil
}

// subtitles is where a burn-in's subtitles come from: a file, a track
// inside the video (track >= 0), or the speech.
type subtitles struct {
	file   string
	track  int
	speech bool
}

// subtitleSource resolves the "Subtitles From" choice against what the
// source has. Automatic tries a file beside the video, then a track inside
// it, then the speech.
func subtitleSource(in Inputs, enc Encode) (subtitles, error) {
	track := optInt(in.Options, "track", 0)
	switch optStr(in.Options, "from", fromAuto) {
	case fromFile:
		file := optStr(in.Options, "subfile", "")
		if file == "" {
			return subtitles{}, errors.New("choose a subtitle file first")
		}
		return subtitles{file: file, track: -1}, nil
	case fromVideo:
		if in.SubtitleTracks != UnknownCount && track >= in.SubtitleTracks {
			return subtitles{}, trackError(in.SubtitleTracks, track)
		}
		return subtitles{track: track}, nil
	case fromSpeech:
		if enc.SpeechModel == "" {
			return subtitles{}, errors.New("download a speech model in Settings first")
		}
		return subtitles{track: -1, speech: true}, nil
	}

	switch {
	case in.Sidecar != "":
		return subtitles{file: in.Sidecar, track: -1}, nil
	case in.SubtitleTracks != 0:
		// Unknown counts as present until the source is probed.
		return subtitles{track: 0}, nil
	case enc.SpeechModel != "":
		return subtitles{track: -1, speech: true}, nil
	}
	return subtitles{}, errors.New("this video has no subtitles, inside it or beside it. " +
		"Download a speech model in Settings to have them written from the speech, or choose a subtitle file")
}

// trackError says which tracks exist when the one asked for doesn't.
func trackError(have, want int) error {
	switch have {
	case 0:
		return errors.New("this video has no subtitle track; choose Automatic to have subtitles written from the speech")
	case 1:
		return fmt.Errorf("this video has one subtitle track, numbered 0, so track %d doesn't exist", want)
	}
	return fmt.Errorf("this video has %d subtitle tracks, numbered 0 to %d, so track %d doesn't exist", have, have-1, want)
}
