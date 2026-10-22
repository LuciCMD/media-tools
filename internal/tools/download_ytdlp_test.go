package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// These run the real yt-dlp against a clip served from 127.0.0.1, so they
// only run with MEDIA_TOOLS_YTDLP=1 set

// localVideoServer serves a 30 s test clip with sound over loopback
func localVideoServer(t *testing.T) string {
	t.Helper()
	if os.Getenv("MEDIA_TOOLS_YTDLP") != "1" {
		t.Skip("set MEDIA_TOOLS_YTDLP=1 to run yt-dlp against a local clip")
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp is not installed")
	}
	bins := needFFmpeg(t)
	dir := t.TempDir()
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=30",
		"-f", "lavfi", "-i", "sine=f=440:d=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "300", "-c:a", "aac", "-y", filepath.Join(dir, "clip.mp4"))
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return srv.URL + "/clip.mp4"
}

// runDownload builds Download Video for opts and runs it, returning the files made
func runDownload(t *testing.T, link string, opts map[string]string) ([]string, string, error) {
	t.Helper()
	tool, _ := Get("download.video")
	valid, err := tool.Validate(opts)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	out := t.TempDir()
	work := testWorkDir(t, out)
	plan, err := tool.Build(Inputs{Path: link, Output: out, WorkDir: work, Options: valid}, softwareEncode())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Keep the author's yt-dlp config and cache out of it
	args := append([]string{"--ignore-config", "--cache-dir", t.TempDir()}, plan.Args...)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	log, runErr := exec.CommandContext(ctx, "yt-dlp", args...).CombinedOutput()
	if runErr != nil {
		t.Logf("yt-dlp %q", args)
	}
	if runErr == nil {
		var more string
		more, runErr = finishDownload(ctx, plan, work)
		log = append(log, more...)
	}
	_ = os.RemoveAll(work)
	entries, _ := os.ReadDir(out)
	var files []string
	for _, e := range entries {
		files = append(files, filepath.Join(out, e.Name()))
	}
	return files, string(log), runErr
}

// testWorkDir makes a download's work folder inside out, as the app does
func testWorkDir(t *testing.T, out string) string {
	t.Helper()
	work, err := os.MkdirTemp(out, ".mediatools-")
	if err != nil {
		t.Fatal(err)
	}
	return work
}

// finishDownload makes the result from what a download left in work, the
// way the job's next stage does, for a plan that has one
func finishDownload(ctx context.Context, plan Plan, work string) (string, error) {
	if plan.Finish == nil {
		return "", nil
	}
	src, err := ffmpeg.FindDownload(work)
	if err != nil {
		return "", err
	}
	fp, err := plan.Finish(src, SourceColor{})
	if err != nil {
		return "", err
	}
	bin := ffmpeg.Locate().FFmpeg
	var log []byte
	for _, args := range fp.Commands() {
		out, err := exec.CommandContext(ctx, bin, append([]string{"-hide_banner", "-nostdin"}, args...)...).CombinedOutput()
		log = append(log, out...)
		if err != nil {
			return string(log), err
		}
	}
	if fp.Staged != "" {
		if err := os.Rename(fp.Staged, fp.OutputPath); err != nil {
			return string(log), err
		}
	}
	return string(log), nil
}

// probeDuration reads a file's duration in seconds with ffprobe
func probeDuration(t *testing.T, path string) float64 {
	t.Helper()
	bins := needFFmpeg(t)
	out, err := exec.Command(bins.FFprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

// probeStreams lists a file's stream types
func probeStreams(t *testing.T, path string) string {
	t.Helper()
	bins := needFFmpeg(t)
	out, _ := exec.Command(bins.FFprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,width",
		"-of", "csv=p=0", path).Output()
	return strings.TrimSpace(string(out))
}

// TestYtdlpGIFClipLengths checks the clip yt-dlp cuts is the one the app
// validated, for each range the app accepts
func TestYtdlpGIFClipLengths(t *testing.T) {
	link := localVideoServer(t)
	cases := []struct {
		start, end string
		want       float64
	}{
		{"00:00:00", "00:00:10", 10},
		{"00:00:05.5", "00:00:08", 2.5},
		{"00:00:02", "00:00:05", 3},
		// 00:00:005 and 00:00:020, which yt-dlp reads as fractions, are now
		// refused before yt-dlp runs, see TestDownloadGIFRejectsTimesYtdlpReadsDifferently
	}
	for _, c := range cases {
		t.Run(c.start+"-"+c.end, func(t *testing.T) {
			files, log, err := runDownload(t, link, map[string]string{"container": "gif", "start": c.start, "end": c.end})
			if err != nil {
				t.Fatalf("yt-dlp failed: %v\n%s", err, log)
			}
			if len(files) != 1 || filepath.Ext(files[0]) != ".gif" {
				t.Fatalf("made %v, want one GIF\n%s", files, log)
			}
			got := probeDuration(t, files[0])
			if got < c.want-0.5 || got > c.want+0.5 {
				t.Errorf("GIF lasts %.2f s, want about %.1f s\n%s", got, c.want, log)
			}
			streams := probeStreams(t, files[0])
			if streams != "gif,video,480" {
				t.Errorf("streams = %q, want one 480 px gif video stream", streams)
			}
		})
	}
}

// TestYtdlpGIFWholeOriginal checks Whole Video with the original width and
// frame rate keeps the source's length, 640 px width and 30 fps, so the
// quoted frame rate cap survives yt-dlp's splitting
func TestYtdlpGIFWholeOriginal(t *testing.T) {
	link := localVideoServer(t)
	files, log, err := runDownload(t, link, map[string]string{"container": "gif",
		"span": "original", "sizing": "original", "pace": "original"})
	if err != nil {
		t.Fatalf("yt-dlp failed: %v\n%s", err, log)
	}
	if len(files) != 1 || filepath.Ext(files[0]) != ".gif" {
		t.Fatalf("made %v, want one GIF\n%s", files, log)
	}
	if got := probeDuration(t, files[0]); got < 29.5 || got > 30.5 {
		t.Errorf("GIF lasts %.2f s, want the whole 30 s", got)
	}
	if streams := probeStreams(t, files[0]); streams != "gif,video,640" {
		t.Errorf("streams = %q, want one 640 px gif video stream", streams)
	}
	bins := needFFmpeg(t)
	rate, _ := exec.Command(bins.FFprobe, "-v", "error", "-show_entries", "stream=r_frame_rate",
		"-of", "default=nw=1:nk=1", files[0]).Output()
	if got := strings.TrimSpace(string(rate)); got != "30/1" {
		t.Errorf("frame rate %q, want the source's 30/1", got)
	}
}

// TestYtdlpGIFPastTheEnd checks a start after the video ends fails the job
// rather than leaving an empty or broken GIF
func TestYtdlpGIFPastTheEnd(t *testing.T) {
	link := localVideoServer(t)
	files, log, err := runDownload(t, link, map[string]string{"container": "gif", "start": "00:01:00", "end": "00:01:10"})
	if err == nil {
		for _, f := range files {
			if filepath.Ext(f) == ".gif" && probeDuration(t, f) > 0.1 {
				return
			}
		}
		t.Errorf("yt-dlp reported success but made %v\n%s", files, log)
		return
	}
	t.Logf("failed as it should, leaving %v\n%s", files, log)
}

// TestYtdlpWebMFromMP4OnlySite checks a site without WebM fails cleanly and
// leaves nothing behind
func TestYtdlpWebMFromMP4OnlySite(t *testing.T) {
	link := localVideoServer(t)
	files, log, err := runDownload(t, link, map[string]string{"container": "webm"})
	if err == nil {
		t.Errorf("WebM from an MP4-only source succeeded with %v\n%s", files, log)
	}
	if len(files) != 0 {
		t.Errorf("left %v behind", files)
	}
	t.Logf("%s", log)
}

// localWebMServer serves a 5 s VP9 and Opus clip, the streams a site like
// YouTube gives for Best Available
func localWebMServer(t *testing.T) string {
	t.Helper()
	if os.Getenv("MEDIA_TOOLS_YTDLP") != "1" {
		t.Skip("set MEDIA_TOOLS_YTDLP=1 to run yt-dlp against a local clip")
	}
	bins := needFFmpeg(t)
	dir := t.TempDir()
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=320x180:r=30:d=5",
		"-f", "lavfi", "-i", "sine=f=440:d=5",
		"-c:v", "libvpx-vp9", "-deadline", "realtime", "-b:v", "300k", "-c:a", "libopus", "-y", filepath.Join(dir, "clip.webm"))
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return srv.URL + "/clip.webm"
}

// TestYtdlpContainersFromVP9Opus checks VP9 and Opus streams end up in each
// container that can hold them, and MOV, which can't, fails before
// downloading so nothing is left behind
func TestYtdlpContainersFromVP9Opus(t *testing.T) {
	link := localWebMServer(t)
	for _, c := range []string{"mp4", "mkv", "webm", "mov"} {
		t.Run(c, func(t *testing.T) {
			files, log, err := runDownload(t, link, map[string]string{"container": c, "quality": "best"})
			if c == "mov" {
				if err == nil || len(files) != 0 {
					t.Errorf("MOV from VP9 and Opus: err %v, left %v\n%s", err, files, log)
				}
				return
			}
			if err != nil {
				t.Fatalf("yt-dlp failed: %v\n%s", err, log)
			}
			if len(files) != 1 || filepath.Ext(files[0]) != "."+c {
				t.Fatalf("made %v, want one .%s\n%s", files, c, log)
			}
			if s := probeStreams(t, files[0]); !strings.Contains(s, "audio") {
				t.Errorf("lost the sound: %q", s)
			}
		})
	}
}

// TestYtdlpGIFFromVP9 checks a site with only VP9 still gives the clip
func TestYtdlpGIFFromVP9(t *testing.T) {
	link := localWebMServer(t)
	files, log, err := runDownload(t, link, map[string]string{"container": "gif", "start": "00:00:01", "end": "00:00:03"})
	if err != nil {
		t.Fatalf("yt-dlp failed: %v\n%s", err, log)
	}
	if len(files) != 1 || filepath.Ext(files[0]) != ".gif" {
		t.Fatalf("made %v, want one GIF\n%s", files, log)
	}
	if got := probeDuration(t, files[0]); got < 1.5 || got > 2.5 {
		t.Errorf("GIF lasts %.2f s, want about 2 s", got)
	}
}

// TestYtdlpContainers checks each sound-keeping container comes out as asked
func TestYtdlpContainers(t *testing.T) {
	link := localVideoServer(t)
	for _, c := range []string{"mp4", "mkv", "mov"} {
		t.Run(c, func(t *testing.T) {
			files, log, err := runDownload(t, link, map[string]string{"container": c, "quality": "best"})
			if err != nil {
				t.Fatalf("yt-dlp failed: %v\n%s", err, log)
			}
			if len(files) != 1 || filepath.Ext(files[0]) != "."+c {
				t.Fatalf("made %v, want one .%s\n%s", files, c, log)
			}
			if s := probeStreams(t, files[0]); !strings.Contains(s, "audio") {
				t.Errorf("lost the sound: %q", s)
			}
		})
	}
}
