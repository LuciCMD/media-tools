package tools

import "testing"

// TestDownloadGIFRejectsTimesYtdlpReadsDifferently checks a time yt-dlp
// would read another way, or refuse, never passes as valid
// yt-dlp reads a third field past two digits as a fraction, so 00:00:020 is
// 0.02 s rather than 20 s, and it refuses a lone dot or a fractional hour
func TestDownloadGIFRejectsTimesYtdlpReadsDifferently(t *testing.T) {
	tool, _ := Get("download.video")
	cases := []struct{ start, end string }{
		{"00:00:005", "00:00:008"},
		{"00:00:03", "00:00:020"},
		{"00:00:100", "00:00:200"},
		{"00:00:.5", "00:00:10"},
		{"0:0:0.", "0:0:5"},
		{"00:00:05.", "00:00:08"},
		{"00:1.5:00", "00:02:00"},
		{"1.5:00:00", "2:00:00"},
		{"000:000:005", "000:000:008"},
	}
	for _, c := range cases {
		opts, err := tool.Validate(map[string]string{"container": "gif", "start": c.start, "end": c.end})
		if err != nil {
			continue
		}
		plan, err := tool.Build(defaultInputs(tool, opts), softwareEncode())
		if err == nil {
			t.Errorf("%q to %q was accepted as %q", c.start, c.end, argValue(plan.Args, "--download-sections"))
		}
	}
}

// TestDownloadGIFAcceptsPlainTimes checks ordinary forms still pass, padded
// or not, with spaces around them
func TestDownloadGIFAcceptsPlainTimes(t *testing.T) {
	cases := []struct{ start, end, want string }{
		{"0:0:5", "0:0:8", "*0:0:5-0:0:8"},
		{" 00:00:01 ", "00:00:04\t", "*00:00:01-00:00:04"},
		{"00:00:59", "00:00:60", "*00:00:59-00:00:60"},
		{"01:00:00", "01:00:02.250", "*01:00:00-01:00:02.250"},
	}
	for _, c := range cases {
		plan := buildVideoDownload(t, map[string]string{"container": "gif", "start": c.start, "end": c.end})
		if got := argValue(plan.Args, "--download-sections"); got != c.want {
			t.Errorf("%q to %q: section %q, want %q", c.start, c.end, got, c.want)
		}
	}
}

// TestDownloadHiddenGIFOptionsDontBlockVideo checks a bad remembered GIF
// value can't stop an MP4 download, should the frontend ever send it
func TestDownloadHiddenGIFOptionsDontBlockVideo(t *testing.T) {
	tool, _ := Get("download.video")
	opts, err := tool.Validate(map[string]string{"container": "mp4", "start": "nonsense", "end": "00:00:00"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, err := tool.Build(defaultInputs(tool, opts), softwareEncode()); err != nil {
		t.Errorf("an MP4 download failed on a hidden GIF time: %v", err)
	}
}

// TestDownloadGIFFractionalNumbersAreKept checks a fractional frame rate or
// width the validator accepts isn't silently swapped for the default
func TestDownloadGIFFractionalNumbersAreKept(t *testing.T) {
	tool, _ := Get("download.video")
	opts, err := tool.Validate(map[string]string{"container": "gif", "fps": "2.5", "width": "321.5"})
	if err != nil {
		return
	}
	plan, err := tool.Build(defaultInputs(tool, opts), softwareEncode())
	if err != nil {
		return
	}
	pp, _ := gifGraphs(gifFinishOf(t, plan))
	if !containsAll(pp, "fps=2", "scale=32") {
		t.Errorf("fps 2.5 and width 321.5 were accepted but became %q", pp)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
