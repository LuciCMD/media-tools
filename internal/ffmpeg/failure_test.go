package ffmpeg

import "testing"

// TestFailureReasonFindsTheCause checks the real cause is found behind
// ffmpeg's generic closing lines, and put in plain words where it's known.
func TestFailureReasonFindsTheCause(t *testing.T) {
	cases := []struct {
		name, stderr, want string
	}{
		{
			"no subtitle track, as reported",
			"Input #0, mov,mp4, from 'clip.mp4':\n" +
				"[Parsed_subtitles_0 @ 00000191ecd4ce40] Unable to locate subtitle stream in C:/clip.mp4\n" +
				"[AVFilterGraph @ 00000191ec57e740] Error initializing filters\n" +
				"Error opening output file out.mp4.\n" +
				"Error opening output files: Operation not permitted\n",
			"the video has no subtitle track to use, and no subtitle file with its name was found beside it",
		},
		{
			"an unknown cause keeps its words, without the component prefix",
			"[libx264 @ 0x55d0c0] width not divisible by 2 (641x360)\nConversion failed!\n",
			"width not divisible by 2 (641x360)",
		},
		{
			"only trailers: the last one is still better than nothing",
			"Error opening input file missing.mp4.\nError opening input files: No such file or directory\n",
			"Error opening input files: No such file or directory",
		},
		{
			"a yt-dlp error passes through",
			"[youtube] abc: Downloading webpage\nERROR: [youtube] abc: Video unavailable\n",
			"ERROR: [youtube] abc: Video unavailable",
		},
		{"nothing at all", "\n\n", "the command failed with no diagnostic output"},
	}
	for _, c := range cases {
		if got := failureReason(c.stderr); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
