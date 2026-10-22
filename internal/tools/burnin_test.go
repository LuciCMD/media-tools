package tools

import (
	"strings"
	"testing"
)

// burnInputs builds Burn In Subtitles inputs with the given options.
func burnInputs(t *testing.T, opts map[string]string, sidecar string, tracks int) Inputs {
	t.Helper()
	tool, _ := Get("video.burnsubs")
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatalf("options %v: %v", opts, err)
	}
	return Inputs{Path: `C:\films\clip.mp4`, Output: `C:\films\clip_subbed.mp4`, Options: valid,
		Sidecar: sidecar, SubtitleTracks: tracks}
}

// TestSubtitleSourceFollowsTheChoice checks where each choice takes its
// subtitles from, and that Automatic prefers a file, then a track, then
// the speech.
func TestSubtitleSourceFollowsTheChoice(t *testing.T) {
	enc := softwareEncode()
	chosen := `D:\subs\any name.ass`
	beside := `C:\films\clip.srt`

	cases := []struct {
		name    string
		opts    map[string]string
		sidecar string
		tracks  int
		want    subtitles
	}{
		{"a chosen file", map[string]string{"from": "file", "subfile": chosen}, beside, 2, subtitles{file: chosen, track: -1}},
		{"a track by number", map[string]string{"from": "video", "track": "1"}, beside, 2, subtitles{track: 1}},
		{"always the speech", map[string]string{"from": "speech"}, beside, 2, subtitles{track: -1, speech: true}},
		{"automatic: the file beside it first", nil, beside, 2, subtitles{file: beside, track: -1}},
		{"automatic: then the video's track", nil, "", 2, subtitles{track: 0}},
		{"automatic: tracks not yet counted", nil, "", UnknownCount, subtitles{track: 0}},
		{"automatic: then the speech", nil, "", 0, subtitles{track: -1, speech: true}},
	}
	for _, c := range cases {
		got, err := subtitleSource(burnInputs(t, c.opts, c.sidecar, c.tracks), enc)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
}

// TestSubtitleSourceExplainsWhatsMissing checks each dead end says what to
// do about it.
func TestSubtitleSourceExplainsWhatsMissing(t *testing.T) {
	noModel := softwareEncode()
	noModel.SpeechModel = ""

	cases := []struct {
		name   string
		opts   map[string]string
		tracks int
		enc    Encode
		want   string
	}{
		{"no file chosen", map[string]string{"from": "file"}, 0, softwareEncode(), "choose a subtitle file"},
		{"no tracks at all", map[string]string{"from": "video"}, 0, softwareEncode(), "no subtitle track"},
		{"a track past the end", map[string]string{"from": "video", "track": "2"}, 2, softwareEncode(), "numbered 0 to 1"},
		{"speech without a model", map[string]string{"from": "speech"}, 0, noModel, "speech model"},
		{"nothing anywhere", nil, 0, noModel, "choose a subtitle file"},
	}
	for _, c := range cases {
		_, err := subtitleSource(burnInputs(t, c.opts, "", c.tracks), c.enc)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error mentioning %q", c.name, err, c.want)
		}
	}
}

// TestFileOptionTakesFullPathsOfItsTypes checks a file option refuses a
// relative path, another type and a line break.
func TestFileOptionTakesFullPathsOfItsTypes(t *testing.T) {
	tool, _ := Get("video.burnsubs")
	good := []string{`C:\subs\one.srt`, `D:\Ocean's [cut].VTT`}
	bad := []string{`one.srt`, `C:\subs\notes.txt`, "C:\\subs\\a\nb.srt"}
	for _, path := range good {
		if _, err := tool.Validate(map[string]string{"subfile": path}); err != nil {
			t.Errorf("%q was refused: %v", path, err)
		}
	}
	for _, path := range bad {
		if _, err := tool.Validate(map[string]string{"subfile": path}); err == nil {
			t.Errorf("%q was accepted", path)
		}
	}
}
