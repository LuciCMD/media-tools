package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const redditPreview = "https://preview.redd.it/abc123.gif?width=640&format=mp4&s=0000"

// TestSiteLinkAsksRedditForTheFile checks Reddit's file hosts get an Accept
// header that isn't a web page, and a media viewer link is swapped for its
// file, where yt-dlp was redirected to the viewer and gave up with
// "Unsupported URL"
func TestSiteLinkAsksRedditForTheFile(t *testing.T) {
	asMP4 := []string{"--add-header", "Accept:*/*", "--parse-metadata", "video:mp4:%(ext)s", "-f", "b"}
	viewer := "https://www.reddit.com/media?url=" + url.QueryEscape(redditPreview)
	cases := []struct {
		name, in, link string
		extra          []string
	}{
		{"preview", redditPreview, redditPreview, asMP4},
		{"external preview", "https://external-preview.redd.it/x.gif?format=mp4", "https://external-preview.redd.it/x.gif?format=mp4", asMP4},
		// The page sends the link as pasted, so a trailing newline must not lose format=mp4
		{"pasted with a newline", "https://preview.redd.it/abc123.gif?s=0000&format=mp4\n", "https://preview.redd.it/abc123.gif?s=0000&format=mp4", asMP4},
		{"upper case host", "https://PREVIEW.REDD.IT/abc123.gif?format=mp4", "https://PREVIEW.REDD.IT/abc123.gif?format=mp4", asMP4},
		// Pictures and real GIFs are left to fail plainly, where the header let
		// yt-dlp fetch a GIF it then failed to remux, leaving a broken file
		{"real gif", "https://i.redd.it/abc123.gif", "https://i.redd.it/abc123.gif", nil},
		{"still preview", "https://preview.redd.it/abc123.jpg?width=640&format=pjpg", "https://preview.redd.it/abc123.jpg?width=640&format=pjpg", nil},
		{"viewer of a picture", "https://www.reddit.com/media?url=" + url.QueryEscape("https://i.redd.it/abc123.png"),
			"https://www.reddit.com/media?url=" + url.QueryEscape("https://i.redd.it/abc123.png"), nil},
		{"viewer", viewer, redditPreview, asMP4},
		{"viewer without www", "https://reddit.com/media?url=" + url.QueryEscape(redditPreview), redditPreview, asMP4},
		// Only Reddit's own files are unwrapped, so a viewer link can't smuggle in another target
		{"viewer of another site", "https://www.reddit.com/media?url=" + url.QueryEscape("https://example.com/a.mp4"),
			"https://www.reddit.com/media?url=" + url.QueryEscape("https://example.com/a.mp4"), nil},
		{"viewer of a file path", "https://www.reddit.com/media?url=" + url.QueryEscape("file://i.redd.it/x"),
			"https://www.reddit.com/media?url=" + url.QueryEscape("file://i.redd.it/x"), nil},
		{"lookalike host", "https://preview.redd.it.example.com/a.gif", "https://preview.redd.it.example.com/a.gif", nil},
		// yt-dlp's own Reddit extractor reads posts and v.redd.it
		{"post", "https://www.reddit.com/r/aww/comments/abc/x/", "https://www.reddit.com/r/aww/comments/abc/x/", nil},
		{"reddit video", "https://v.redd.it/abc123", "https://v.redd.it/abc123", nil},
		{"youtube", "https://www.youtube.com/watch?v=x", "https://www.youtube.com/watch?v=x", nil},
	}
	for _, c := range cases {
		link, extra := siteLink(c.in, "")
		if link != c.link || !slices.Equal(extra, c.extra) {
			t.Errorf("%s: got %q %q, want %q %q", c.name, link, extra, c.link, c.extra)
		}
	}
}

// TestSiteLinkLeavesWebMItsOwnChoice checks WebM keeps its own format choice,
// which fails before downloading, where taking Reddit's H.264 left an MP4 that
// then failed to remux into WebM
func TestSiteLinkLeavesWebMItsOwnChoice(t *testing.T) {
	_, extra := siteLink(redditPreview, "webm")
	want := []string{"--add-header", "Accept:*/*", "--parse-metadata", "video:mp4:%(ext)s"}
	if !slices.Equal(extra, want) {
		t.Errorf("got %q, want %q", extra, want)
	}
}

// TestDownloadsFetchTheRedditFile checks every download tool hands yt-dlp the
// file behind a viewer link, last, with Reddit's arguments just before it
func TestDownloadsFetchTheRedditFile(t *testing.T) {
	viewer := "https://www.reddit.com/media?url=" + url.QueryEscape(redditPreview)
	for _, id := range []string{"download.video", "download.audio", "download.playlist"} {
		tool, _ := Get(id)
		valid, err := tool.Validate(map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := tool.Build(Inputs{Path: viewer, Output: t.TempDir(), Options: valid}, softwareEncode())
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		tail := plan.Args[len(plan.Args)-7:]
		want := []string{"--add-header", "Accept:*/*", "--parse-metadata", "video:mp4:%(ext)s", "-f", "b", redditPreview}
		if !slices.Equal(tail, want) {
			t.Errorf("%s ends %q, want %q", id, tail, want)
		}
	}
}

// redditLikeServer serves a clip the way Reddit's file hosts do, redirecting
// a request that accepts a web page to a viewer page yt-dlp can't read
func redditLikeServer(t *testing.T) string {
	t.Helper()
	if os.Getenv("MEDIA_TOOLS_YTDLP") != "1" {
		t.Skip("set MEDIA_TOOLS_YTDLP=1 to run yt-dlp against a local clip")
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp is not installed")
	}
	bins := needFFmpeg(t)
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-an", "-movflags", "+faststart", "-y", clip)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html><head><title>Reddit</title></head><body>viewer</body></html>")
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/media?url="+url.QueryEscape(srv.URL+r.URL.RequestURI()), http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeFile(w, r, clip)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/abc123.gif?width=640&format=mp4&s=0000"
}

// runRedditPlan builds Download Video for a Reddit viewer link, points its
// link at the local stand-in, and runs yt-dlp with or without Reddit's arguments
func runRedditPlan(t *testing.T, local string, opts map[string]string, fixed bool) ([]string, string, error) {
	t.Helper()
	tool, _ := Get("download.video")
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	viewer := "https://www.reddit.com/media?url=" + url.QueryEscape(redditPreview)
	plan, err := tool.Build(Inputs{Path: viewer, Output: out, Options: valid}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--ignore-config", "--cache-dir", t.TempDir()}, plan.Args[:len(plan.Args)-1]...)
	if !fixed {
		args = args[:len(args)-6]
	}
	args = append(args, local)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log, runErr := exec.CommandContext(ctx, "yt-dlp", args...).CombinedOutput()
	entries, _ := os.ReadDir(out)
	var files []string
	for _, e := range entries {
		files = append(files, filepath.Join(out, e.Name()))
	}
	return files, string(log), runErr
}

// TestYtdlpDownloadsARedditPreview runs the real yt-dlp against the stand-in,
// as an MP4 and as a GIF, and shows it fails the old way without Reddit's arguments
func TestYtdlpDownloadsARedditPreview(t *testing.T) {
	local := redditLikeServer(t)
	if _, log, err := runRedditPlan(t, local, map[string]string{"container": "mp4"}, false); err == nil ||
		!strings.Contains(log, "Unsupported URL") {
		t.Fatalf("without the header the stand-in should fail like Reddit, err %v\n%s", err, log)
	}
	// The content is checked too, since a renamed MP4 would pass by name alone
	for _, c := range []struct{ container, ext, magic string }{{"mp4", ".mp4", "ftyp"}, {"gif", ".gif", "GIF8"}} {
		files, log, err := runRedditPlan(t, local, map[string]string{"container": c.container, "span": "original"}, true)
		if err != nil {
			t.Fatalf("%s: yt-dlp failed: %v\n%s", c.container, err, log)
		}
		if len(files) != 1 || filepath.Ext(files[0]) != c.ext {
			t.Fatalf("%s: made %q, want one %s file\n%s", c.container, files, c.ext, log)
		}
		head, err := os.ReadFile(files[0])
		if err != nil || len(head) < 12 || !strings.Contains(string(head[:12]), c.magic) {
			t.Errorf("%s: %s doesn't start like one (%v)\n%s", c.container, files[0], err, log)
		}
	}
}
