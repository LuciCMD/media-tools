package ffmpeg

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

// fieldRef matches one %(name)s field of a yt-dlp output template
var fieldRef = regexp.MustCompile(`%\(([a-z_.]+)\)s`)

// ytdlpLine renders the download template the way yt-dlp would for values,
// printing NA for any field not given, so these tests follow the template
// whatever order its fields are in
func ytdlpLine(t *testing.T, values map[string]string) string {
	t.Helper()
	args := DownloadProgressArgs()
	tmpl := ""
	for i := 0; i+1 < len(args); i += 2 {
		if strings.HasPrefix(args[i+1], "download:") {
			tmpl = strings.TrimPrefix(args[i+1], "download:")
		}
	}
	if tmpl == "" {
		t.Fatalf("no download template in %q", args)
	}
	return fieldRef.ReplaceAllStringFunc(tmpl, func(ref string) string {
		name := fieldRef.FindStringSubmatch(ref)[1]
		if v, ok := values[name]; ok {
			return v
		}
		return "NA"
	})
}

// entry renders one playlist entry's progress line
func entry(t *testing.T, status, got, total, n, of string) string {
	return ytdlpLine(t, map[string]string{
		"progress.status": status, "progress.downloaded_bytes": got, "progress.total_bytes": total,
		"progress.speed": "1000000", "progress.eta": "3",
		"info.playlist_index": n, "info.playlist_autonumber": n, "info.n_entries": of,
		"info.vcodec": "avc1.64001f", "info.acodec": "mp4a.40.2",
	})
}

// TestHuntPlaylistStepKeepsTheEntriesDone checks the steps yt-dlp runs after
// each playlist entry keep the bar at the entries done, where they set it to
// unknown, so the bar flips to indeterminate and the window title drops to 0%
// between every entry of a playlist
func TestHuntPlaylistStepKeepsTheEntriesDone(t *testing.T) {
	got := parseLines(t,
		entry(t, "downloading", "50", "100", "2", "4"),
		entry(t, "finished", "100", "100", "2", "4"),
		"mediatools-pp Metadata started",
		"mediatools-pp Metadata finished",
		"mediatools-pp MoveFiles started",
	)
	if len(got) != 5 {
		t.Fatalf("got %d samples", len(got))
	}
	if math.Abs(got[1].Fraction-0.5) > 0.001 {
		t.Fatalf("entry 2 of 4 finished reads %.3f, want 0.5", got[1].Fraction)
	}
	for _, p := range got[2:] {
		if p.Fraction < 0.25 {
			t.Errorf("during %q after entry 2 of 4 the bar reads %.3f, want at least the entry before it done",
				p.Stage, p.Fraction)
		}
	}
}

// TestHuntSizesOverFourGigabytes checks sizes past 32 bits, and the exponent
// form Python prints for floats of 1e16 and over
func TestHuntSizesOverFourGigabytes(t *testing.T) {
	got := parseLines(t,
		ytdlpLine(t, map[string]string{"progress.status": "downloading",
			"progress.downloaded_bytes": "5000000000", "progress.total_bytes": "10000000000"}),
		ytdlpLine(t, map[string]string{"progress.status": "downloading",
			"progress.downloaded_bytes": "6000000000", "progress.total_bytes_estimate": "1.2e+16"}),
	)
	if got[0].Fraction != 0.5 || got[0].SizeBytes != 5_000_000_000 || got[0].TotalBytes != 10_000_000_000 {
		t.Errorf("a 10 GB part half done reads %+v", got[0])
	}
	if got[1].TotalBytes != 12_000_000_000_000_000 || got[1].Fraction <= 0 {
		t.Errorf("an estimate in exponent form reads %+v", got[1])
	}
}

// TestHuntUnknownStepAndOddStageLines checks an unknown postprocessor is
// Finishing, and broken stage lines are swallowed without changing anything
func TestHuntUnknownStepAndOddStageLines(t *testing.T) {
	got := parseLines(t,
		ytdlpLine(t, map[string]string{"progress.status": "downloading",
			"progress.downloaded_bytes": "10", "progress.total_bytes": "100"}),
		"mediatools-pp FixupM3u8 started",
		"mediatools-pp",
		"mediatools-pp started",
		"mediatools-pp <script>alert(1)</script> started",
	)
	if got[1].Stage != "Finishing" {
		t.Errorf("an unknown step is %q", got[1].Stage)
	}
	for _, p := range got {
		if strings.ContainsAny(p.Stage, "<>") {
			t.Errorf("a stage carried markup through: %q", p.Stage)
		}
	}
}

// TestHuntOtherStdoutAround checks yt-dlp's own lines between the templated
// ones, CRLF endings and a title that starts like a field, change nothing
func TestHuntOtherStdoutAround(t *testing.T) {
	half := ytdlpLine(t, map[string]string{"progress.status": "downloading",
		"progress.downloaded_bytes": "50", "progress.total_bytes": "100"})
	stream := strings.Join([]string{
		"[download] Destination: C:\\Users\\Eli\\Videos\\mediatools-dl finished 1 1 [x].mp4",
		half,
		"[info] progress=end",
		"progress=end",
		"mediatools-dlx finished 1 1",
		" " + half,
	}, "\r\n") + "\r\n"
	var got []Progress
	_ = ParseProgress(strings.NewReader(stream), Totals{}, func(p Progress) { got = append(got, p) })
	if len(got) == 0 || math.Abs(got[0].Fraction-0.5) > 0.001 {
		t.Fatalf("the templated line between others reads %+v", got)
	}
	for _, p := range got[1:] {
		if p.Fraction == 1 && !p.Done {
			t.Errorf("a line that only looks like progress finished the part: %+v", p)
		}
	}
}
