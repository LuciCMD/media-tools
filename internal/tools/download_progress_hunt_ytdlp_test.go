package tools

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// These run the real yt-dlp against clips served from 127.0.0.1, so they
// only run with MEDIA_TOOLS_YTDLP=1 set

// playlistServer serves n short clips and an HTML page embedding each one,
// which yt-dlp's generic extractor reads as a playlist
func playlistServer(t *testing.T, n int) string {
	t.Helper()
	if os.Getenv("MEDIA_TOOLS_YTDLP") != "1" {
		t.Skip("set MEDIA_TOOLS_YTDLP=1 to run yt-dlp against a local clip")
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp is not installed")
	}
	bins := needFFmpeg(t)
	dir := t.TempDir()
	var page strings.Builder
	page.WriteString("<html><head><title>Local List</title></head><body>\n")
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("clip%d.mp4", i)
		run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=320x180:r=30:d=3",
			"-f", "lavfi", "-i", "sine=f=440:d=3",
			"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-y", filepath.Join(dir, name))
		fmt.Fprintf(&page, "<video controls><source src=\"%s\" type=\"video/mp4\"></video>\n", name)
	}
	page.WriteString("</body></html>\n")
	if err := os.WriteFile(filepath.Join(dir, "list.html"), []byte(page.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return srv.URL + "/list.html"
}

// progressRun builds tool for opts and runs yt-dlp the way the queue does,
// returning the templated lines it printed and the samples they parse to
func progressRun(t *testing.T, toolID, link string, opts map[string]string, extra ...string) ([]string, []ffmpeg.Progress, string) {
	t.Helper()
	tool, _ := Get(toolID)
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	out := t.TempDir()
	plan, err := tool.Build(Inputs{Path: link, Output: out, Options: valid}, softwareEncode())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	args := append(ffmpeg.DownloadProgressArgs(), "--ignore-config", "--cache-dir", t.TempDir())
	args = append(args, extra...)
	args = append(args, plan.Args...)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("yt-dlp failed: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	var lines []string
	for _, l := range strings.Split(stdout.String(), "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, "mediatools-") {
			lines = append(lines, l)
		}
	}
	var samples []ffmpeg.Progress
	_ = ffmpeg.ParseProgress(bytes.NewReader(stdout.Bytes()), ffmpeg.Totals{}, func(p ffmpeg.Progress) {
		samples = append(samples, p)
	})
	return lines, samples, stdout.String()
}

// separatePartsInfo serves a video-only and an audio-only file and writes an
// info JSON offering them as two formats, the way YouTube serves most sizes
func separatePartsInfo(t *testing.T) string {
	t.Helper()
	if os.Getenv("MEDIA_TOOLS_YTDLP") != "1" {
		t.Skip("set MEDIA_TOOLS_YTDLP=1 to run yt-dlp against a local clip")
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp is not installed")
	}
	bins := needFFmpeg(t)
	dir := t.TempDir()
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=20",
		"-c:v", "libx264", "-preset", "ultrafast", "-an", "-y", filepath.Join(dir, "v.mp4"))
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "sine=f=440:d=20",
		"-c:a", "aac", "-b:a", "256k", "-vn", "-y", filepath.Join(dir, "a.m4a"))
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	info := fmt.Sprintf(`{"id": "sep", "title": "Separate Parts", "extractor": "generic", "extractor_key": "Generic",
"webpage_url": "%[1]s/page", "original_url": "%[1]s/page", "_type": "video",
"formats": [
 {"format_id": "v360", "url": "%[1]s/v.mp4", "ext": "mp4", "vcodec": "avc1.64001e", "acodec": "none", "width": 640, "height": 360, "protocol": "http"},
 {"format_id": "a256", "url": "%[1]s/a.m4a", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "protocol": "http"}
]}`, srv.URL)
	path := filepath.Join(t.TempDir(), "sep.info.json")
	if err := os.WriteFile(path, []byte(info), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestYtdlpSeparatePartsBarOnlyMovesForward checks Download Video from a
// site serving picture and sound apart gives one bar that only moves
// forward, where each part runs 0 to 100% on its own and the bar drops back
// to near 0% when the sound starts
func TestYtdlpSeparatePartsBarOnlyMovesForward(t *testing.T) {
	infoPath := separatePartsInfo(t)
	tool, _ := Get("download.video")
	valid, err := tool.Validate(map[string]string{"container": "mp4"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := tool.Build(Inputs{Path: "http://127.0.0.1/page", Output: t.TempDir(), Options: valid}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	// The link is the last argument, swapped for the info JSON
	args := append(ffmpeg.DownloadProgressArgs(), "--ignore-config", "--cache-dir", t.TempDir(), "--limit-rate", "1M")
	args = append(args, plan.Args[:len(plan.Args)-1]...)
	args = append(args, "--load-info-json", infoPath)

	var samples []ffmpeg.Progress
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := ffmpeg.Run(ctx, ffmpeg.Request{Binary: "yt-dlp", Args: args}, func(p ffmpeg.Progress) {
		samples = append(samples, p)
	})
	if err != nil {
		t.Fatalf("yt-dlp failed: %v\n%s", err, res.Stderr)
	}
	// Within a part the bar only moves forward. When the sound starts its
	// size joins the total, so the bar may dip by the sound's share, but it
	// carries on from the picture's bytes rather than starting again
	stages := map[string]bool{}
	last := -1.0
	stage := ""
	var back string
	for _, p := range samples {
		stages[p.Stage] = true
		if p.Fraction < 0 {
			continue
		}
		newPart := p.Stage != stage
		if back == "" && (!newPart && p.Fraction < last-0.001 || newPart && p.Fraction < last*0.75) {
			back = fmt.Sprintf("from %.1f%% to %.1f%% as %q began", last*100, p.Fraction*100, p.Stage)
		}
		last, stage = p.Fraction, p.Stage
	}
	t.Logf("stages seen %v", stages)
	if !stages["Downloading video"] || !stages["Downloading audio"] || !stages["Merging video and audio"] {
		t.Fatalf("not a two-part download with a merge, stages %v", stages)
	}
	if back != "" {
		t.Errorf("the bar went back %s", back)
	}
}

// TestYtdlpStepsAreNamed checks Download Audio and a GIF clip name their
// conversion step, and every templated line yt-dlp printed was read
func TestYtdlpStepsAreNamed(t *testing.T) {
	link := localVideoServer(t)
	cases := []struct {
		name, tool string
		opts       map[string]string
		want       string
	}{
		{"audio", "download.audio", map[string]string{"format": "mp3"}, "Converting audio"},
		{"gif", "download.video", map[string]string{"container": "gif", "start": "00:00:02", "end": "00:00:06"}, "Converting"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines, samples, all := progressRun(t, c.tool, link, c.opts)
			if len(samples) != len(lines) {
				t.Errorf("%d templated lines gave %d samples\n%s", len(lines), len(samples), all)
			}
			named := false
			for _, p := range samples {
				named = named || p.Stage == c.want
			}
			if !named {
				t.Errorf("the %q step was never named\n%s", c.want, all)
			}
		})
	}
}

// TestYtdlpPlaylistRangeCountsChosenEntries checks items 3 to 4 of four read
// as entries 1 and 2 of 2, with the bar inside 0 to 100% and only going
// forward while entries download, where playlist_index gave "item 4 of 2"
// and 200%
func TestYtdlpPlaylistRangeCountsChosenEntries(t *testing.T) {
	link := playlistServer(t, 4)
	_, samples, all := progressRun(t, "download.playlist", link, map[string]string{"archive": "false", "range": "3-4"})
	last := -1.0
	items := map[int]bool{}
	for _, p := range samples {
		if p.Items != 2 && p.Fraction >= 0 {
			t.Fatalf("a sample counts %d entries, want 2: %+v\n%s", p.Items, p, all)
		}
		if p.Item > p.Items || p.Fraction > 1 {
			t.Fatalf("a sample reads item %d of %d at %.3f", p.Item, p.Items, p.Fraction)
		}
		items[p.Item] = true
		if p.Fraction < 0 {
			continue
		}
		if p.Fraction < last-0.001 {
			t.Errorf("the bar went back from %.3f to %.3f", last, p.Fraction)
		}
		last = p.Fraction
	}
	if !items[1] || !items[2] {
		t.Errorf("entries seen %v, want 1 and 2", items)
	}
	if last < 0.999 {
		t.Errorf("the last entry ended at %.3f", last)
	}
}
