package tools

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// Unit checks of siteLink, always run

// TestHuntSiteLinkViewerVariants checks the viewer is unwrapped wherever
// Reddit serves it, and extra viewer parameters don't get in the way
func TestHuntSiteLinkViewerVariants(t *testing.T) {
	inner := "https://preview.redd.it/abc123.gif?width=640&format=mp4&s=0000"
	enc := url.QueryEscape(inner)
	asMP4 := []string{"--add-header", "Accept:*/*", "--parse-metadata", "video:mp4:%(ext)s", "-f", "b"}
	for _, viewer := range []string{
		"https://old.reddit.com/media?url=" + enc,
		"https://new.reddit.com/media?url=" + enc,
		"https://np.reddit.com/media?url=" + enc,
		"https://m.reddit.com/media?url=" + enc,
		"https://WWW.REDDIT.COM/media?url=" + enc,
		"http://www.reddit.com/media?url=" + enc,
		"https://www.reddit.com:443/media?url=" + enc,
		"https://www.reddit.com/media?url=" + enc + "&rdt=12345",
		"https://www.reddit.com/media?rdt=1&url=" + enc,
		"  https://www.reddit.com/media?url=" + enc + "  ",
	} {
		link, extra := siteLink(viewer, "")
		if link != inner || !slices.Equal(extra, asMP4) {
			t.Errorf("%s: got %q %q, want %q %q", viewer, link, extra, inner, asMP4)
		}
	}
	// An upper case value still serves an MP4
	if _, extra := siteLink("https://preview.redd.it/a.gif?format=MP4", ""); !slices.Equal(extra, asMP4) {
		t.Errorf("format=MP4 got %q", extra)
	}
}

// TestHuntSiteLinkRefusesSmuggledTargets checks a viewer link can only ever
// unwrap to Reddit's own file hosts, however the inner link is dressed up
func TestHuntSiteLinkRefusesSmuggledTargets(t *testing.T) {
	for _, inner := range []string{
		"https://i.redd.it.evil.example/x.gif",
		"https://i.redd.it@evil.example/x.gif",
		"https://evil.example#@i.redd.it/x.gif",
		"https://evil.example?@i.redd.it/x.gif",
		`https://evil.example\@i.redd.it/x.gif`,
		"//i.redd.it/x.gif",
		"javascript:alert(1)//i.redd.it",
		"file:///C:/Windows/win.ini",
		"-o C:/x https://i.redd.it/x.gif",
		" https://i.redd.it/x.gif",
		"https://" + strings.Repeat("a", maxURLLength) + ".i.redd.it/x.gif",
	} {
		viewer := "https://www.reddit.com/media?url=" + url.QueryEscape(inner)
		link, extra := siteLink(viewer, "")
		if link != viewer || extra != nil {
			t.Errorf("inner %q unwrapped to %q %q", inner, link, extra)
		}
	}
	for _, viewer := range []string{
		"https://reddit.com.evil.example/media?url=" + url.QueryEscape("https://i.redd.it/x.gif"),
		"https://evilreddit.com/media?url=" + url.QueryEscape("https://i.redd.it/x.gif"),
		"https://www.reddit.com/r/x/media?url=" + url.QueryEscape("https://i.redd.it/x.gif"),
	} {
		if link, extra := siteLink(viewer, ""); link != viewer || extra != nil {
			t.Errorf("%s unwrapped to %q %q", viewer, link, extra)
		}
	}
}

// TestHuntRedditLinkStaysLastOnRetry checks every tool keeps the Reddit file
// last with its arguments before it, also after Retry puts --force-overwrites
// first, and the queue puts the progress template first
func TestHuntRedditLinkStaysLastOnRetry(t *testing.T) {
	viewer := "https://www.reddit.com/media?url=" + url.QueryEscape(huntPreview)
	for _, c := range []struct {
		id   string
		opts map[string]string
	}{
		{"download.video", map[string]string{"container": "mp4"}},
		{"download.video", map[string]string{"container": "gif", "span": "clip"}},
		{"download.video", map[string]string{"container": "mkv", "subs": "true", "sponsorblock": "true", "cookies": "firefox"}},
		{"download.audio", map[string]string{"split": "true"}},
		{"download.playlist", map[string]string{"range": "1-3", "kind": "audio"}},
	} {
		tool, _ := Get(c.id)
		valid, err := tool.Validate(c.opts)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := tool.Build(Inputs{Path: viewer, Output: t.TempDir(), Options: valid}, softwareEncode())
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		args := append(ffmpeg.DownloadProgressArgs(), plan.Args...)
		if !slices.Contains(args, "--force-overwrites") {
			args = append([]string{"--force-overwrites"}, args...)
		}
		tail := args[len(args)-7:]
		want := []string{"--add-header", "Accept:*/*", "--parse-metadata", "video:mp4:%(ext)s", "-f", "b", huntPreview}
		if !slices.Equal(tail, want) {
			t.Errorf("%s %v ends %q", c.id, c.opts, tail)
		}
		if n := strings.Count(strings.Join(args, "\x00"), "redd.it"); n != 1 {
			t.Errorf("%s names the link %d times", c.id, n)
		}
	}
}

// Real yt-dlp checks, opt-in with MEDIA_TOOLS_YTDLP=1

const huntPreview = "https://preview.redd.it/abc123.gif?width=640&format=mp4&s=0000"

// redditHunt is a local stand-in for Reddit's file hosts, serving an MP4
// under a .gif name for format=mp4, a real GIF otherwise, and the viewer page
// to any request that accepts a web page
type redditHunt struct {
	url     string
	mu      sync.Mutex
	accepts []string
}

func newRedditHunt(t *testing.T) *redditHunt {
	t.Helper()
	if os.Getenv("MEDIA_TOOLS_YTDLP") != "1" {
		t.Skip("set MEDIA_TOOLS_YTDLP=1 to run yt-dlp against a local clip")
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp is not installed")
	}
	bins := needFFmpeg(t)
	dir := t.TempDir()
	silent := filepath.Join(dir, "silent.mp4")
	sound := filepath.Join(dir, "sound.mp4")
	gif := filepath.Join(dir, "real.gif")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-an", "-movflags", "+faststart", "-y", silent)
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=6", "-f", "lavfi", "-i", "sine=f=440:d=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-movflags", "+faststart", "-y", sound)
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=10:d=3", "-loop", "0", "-y", gif)

	h := &redditHunt{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.accepts = append(h.accepts, r.Header.Get("Accept"))
		h.mu.Unlock()
		if r.URL.Path == "/media" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><head><title>Reddit</title></head><body>viewer</body></html>"))
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/media?url="+url.QueryEscape(srv.URL+r.URL.RequestURI()), http.StatusTemporaryRedirect)
			return
		}
		switch {
		case strings.EqualFold(r.URL.Query().Get("format"), "mp4") && strings.HasPrefix(r.URL.Path, "/snd"):
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeFile(w, r, sound)
		case strings.EqualFold(r.URL.Query().Get("format"), "mp4"):
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeFile(w, r, silent)
		default:
			w.Header().Set("Content-Type", "image/gif")
			http.ServeFile(w, r, gif)
		}
	}))
	t.Cleanup(srv.Close)
	h.url = srv.URL
	return h
}

type huntResult struct {
	files   []string
	log     string
	err     error
	samples []ffmpeg.Progress
}

// runHunt builds toolID for the Reddit link, checks the link is last, then
// swaps it for local and runs yt-dlp the way the queue does, into out
func runHunt(t *testing.T, toolID, reddit, local, out string, opts map[string]string, prefix ...string) huntResult {
	t.Helper()
	tool, _ := Get(toolID)
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := tool.Build(Inputs{Path: reddit, Output: out, Options: valid}, softwareEncode())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if last := plan.Args[len(plan.Args)-1]; !strings.Contains(last, "redd.it") {
		t.Fatalf("last argument %q isn't the Reddit file", last)
	}
	args := []string{"--ignore-config", "--cache-dir", t.TempDir()}
	args = append(args, prefix...)
	args = append(args, ffmpeg.DownloadProgressArgs()...)
	args = append(args, plan.Args[:len(plan.Args)-1]...)
	args = append(args, local)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	runErr := cmd.Run()
	var files []string
	_ = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			files = append(files, p)
		}
		return nil
	})
	var samples []ffmpeg.Progress
	_ = ffmpeg.ParseProgress(bytes.NewReader(buf.Bytes()), ffmpeg.Totals{}, func(p ffmpeg.Progress) {
		samples = append(samples, p)
	})
	return huntResult{files, buf.String(), runErr, samples}
}

// huntProbe reads a file's container, first video codec and width, and length
func huntProbe(t *testing.T, path string) (format, vcodec string, width int, secs float64) {
	t.Helper()
	bins := needFFmpeg(t)
	out, err := exec.Command(bins.FFprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "format=format_name,duration:stream=codec_name,width", "-of", "default=nw=1", path).Output()
	if err != nil {
		return "", "", 0, 0
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "format_name":
			format = v
		case "codec_name":
			vcodec = v
		case "width":
			width, _ = strconv.Atoi(v)
		case "duration":
			secs, _ = strconv.ParseFloat(v, 64)
		}
	}
	return
}

// TestHuntYtdlpRedditPreviewEveryContainer runs Download Video for a Reddit
// animated preview into every container that keeps sound and the GIF
func TestHuntYtdlpRedditPreviewEveryContainer(t *testing.T) {
	h := newRedditHunt(t)
	local := h.url + "/abc123.gif?width=640&format=mp4&s=0000"
	viewer := "https://www.reddit.com/media?url=" + url.QueryEscape(huntPreview)
	for _, c := range []struct {
		container, ext, format string
	}{
		{"mp4", ".mp4", "mov,mp4"},
		{"mkv", ".mkv", "matroska"},
		// MOV takes MP4 from a site that doesn't name its codecs, and this
		// is an H.264 MP4 once the .gif name is set aside
		{"mov", ".mov", "mov,mp4"},
	} {
		t.Run(c.container, func(t *testing.T) {
			r := runHunt(t, "download.video", viewer, local, t.TempDir(), map[string]string{"container": c.container})
			if r.err != nil {
				t.Fatalf("yt-dlp failed: %v\n%s", r.err, r.log)
			}
			if len(r.files) != 1 || filepath.Ext(r.files[0]) != c.ext {
				t.Fatalf("made %q, want one %s\n%s", r.files, c.ext, r.log)
			}
			if f, v, _, _ := huntProbe(t, r.files[0]); !strings.HasPrefix(f, c.format) || v != "h264" {
				t.Errorf("%s is %s with %s", r.files[0], f, v)
			}
		})
	}
	// Control for mov, the same file under an .mp4 name
	t.Run("mov from an mp4 name", func(t *testing.T) {
		r := runHunt(t, "download.video", "https://preview.redd.it/abc123.mp4?format=mp4", h.url+"/abc123.mp4?format=mp4",
			t.TempDir(), map[string]string{"container": "mov"})
		if r.err != nil || len(r.files) != 1 || filepath.Ext(r.files[0]) != ".mov" {
			t.Errorf("yt-dlp failed: %v %q\n%s", r.err, r.files, r.log)
		}
	})
	// WebM is only where the site has it, so a clear failure is right here
	t.Run("webm", func(t *testing.T) {
		r := runHunt(t, "download.video", viewer, local, t.TempDir(), map[string]string{"container": "webm"})
		if r.err == nil || !strings.Contains(r.log, "Requested format is not available") {
			t.Errorf("want a clear failure, got %v\n%s", r.err, r.log)
		}
	})
	for _, c := range []struct {
		name string
		opts map[string]string
		secs float64
	}{
		{"gif whole", map[string]string{"container": "gif", "span": "original", "width": "320"}, 6},
		{"gif clip", map[string]string{"container": "gif", "span": "clip", "start": "00:00:01", "end": "00:00:03", "width": "320"}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runHunt(t, "download.video", viewer, local, t.TempDir(), c.opts)
			if r.err != nil {
				t.Fatalf("yt-dlp failed: %v\n%s", r.err, r.log)
			}
			if len(r.files) != 1 || filepath.Ext(r.files[0]) != ".gif" {
				t.Fatalf("made %q, want one .gif\n%s", r.files, r.log)
			}
			f, _, w, secs := huntProbe(t, r.files[0])
			if f != "gif" || w != 320 || secs < c.secs-0.3 || secs > c.secs+0.3 {
				t.Errorf("%s is %s %dpx %.2fs, want gif 320px %.0fs\n%s", r.files[0], f, w, secs, c.secs, r.log)
			}
		})
	}
	for _, a := range h.accepts {
		if strings.Contains(a, "text/html") {
			t.Errorf("a request asked for a web page: %q", a)
		}
	}
}

// TestHuntYtdlpRedditAudioPlaylistAndRetry checks Download Audio, Download
// Playlist and a retried Download Video into the same folder
func TestHuntYtdlpRedditAudioPlaylistAndRetry(t *testing.T) {
	h := newRedditHunt(t)
	local := h.url + "/abc123.gif?width=640&format=mp4&s=0000"
	viewer := "https://www.reddit.com/media?url=" + url.QueryEscape(huntPreview)

	r := runHunt(t, "download.audio", "https://preview.redd.it/snd123.gif?format=mp4", h.url+"/snd123.gif?format=mp4",
		t.TempDir(), map[string]string{})
	if r.err != nil || len(r.files) != 1 || filepath.Ext(r.files[0]) != ".mp3" {
		t.Errorf("audio: %v %q\n%s", r.err, r.files, r.log)
	}

	out := t.TempDir()
	r = runHunt(t, "download.playlist", viewer, local, out, map[string]string{})
	if r.err != nil || len(r.files) != 1 || filepath.Ext(r.files[0]) != ".mp4" {
		t.Errorf("playlist: %v %q\n%s", r.err, r.files, r.log)
	}

	out = t.TempDir()
	first := runHunt(t, "download.video", viewer, local, out, map[string]string{"container": "mp4"})
	again := runHunt(t, "download.video", viewer, local, out, map[string]string{"container": "mp4"}, "--force-overwrites")
	if first.err != nil || again.err != nil || len(again.files) != 1 || strings.Contains(again.log, "has already been downloaded") {
		t.Errorf("retry: %v %v %q\n%s", first.err, again.err, again.files, again.log)
	}
	if f, v, _, _ := huntProbe(t, again.files[0]); !strings.HasPrefix(f, "mov,mp4") || v != "h264" {
		t.Errorf("retried file is %s %s", f, v)
	}
}

// TestHuntYtdlpRedditProgressStages checks the bar reads every download line
// with Reddit's arguments, and the metadata step yt-dlp runs before the
// download doesn't label the download itself as Finishing
func TestHuntYtdlpRedditProgressStages(t *testing.T) {
	h := newRedditHunt(t)
	local := h.url + "/abc123.gif?width=640&format=mp4&s=0000"
	viewer := "https://www.reddit.com/media?url=" + url.QueryEscape(huntPreview)
	for _, c := range []struct {
		name string
		opts map[string]string
	}{
		{"mp4", map[string]string{"container": "mp4"}},
		{"gif clip", map[string]string{"container": "gif", "span": "clip", "start": "00:00:01", "end": "00:00:03"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runHunt(t, "download.video", viewer, local, t.TempDir(), c.opts)
			if r.err != nil {
				t.Fatalf("yt-dlp failed: %v\n%s", r.err, r.log)
			}
			lines := strings.Count(r.log, "mediatools-")
			if len(r.samples) != lines {
				t.Errorf("%d templated lines but %d samples", lines, len(r.samples))
			}
			reached, early := false, false
			var stages []string
			for _, p := range r.samples {
				stages = append(stages, p.Stage+" "+strconv.FormatFloat(p.Fraction, 'f', 2, 64))
				early = early || (!reached && p.Stage == "Finishing")
				if strings.HasPrefix(p.Stage, "Downloading") && p.Fraction >= 1 {
					reached = true
				}
			}
			if early {
				t.Errorf("shows Finishing before the download has come down: %q", stages)
			}
			if !reached {
				t.Errorf("the download never reached 100%%\n%s", r.log)
			}
		})
	}
}

// TestHuntYtdlpRedditRealGIF checks an i.redd.it GIF, which Reddit serves as
// image/gif, fails plainly as before, where asking for the file let yt-dlp
// fetch it, fail to remux it to MP4 and leave a broken .mp4 beside it
func TestHuntYtdlpRedditRealGIF(t *testing.T) {
	h := newRedditHunt(t)
	r := runHunt(t, "download.video", "https://i.redd.it/real.gif", h.url+"/real.gif", t.TempDir(),
		map[string]string{"container": "mp4"})
	if r.err == nil || !strings.Contains(r.log, "Unsupported URL") || len(r.files) != 0 {
		t.Errorf("want a plain Unsupported URL and no files, got %v %q\n%s", r.err, r.files, r.log)
	}
}
