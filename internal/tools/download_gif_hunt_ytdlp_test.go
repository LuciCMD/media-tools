package tools

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These run the real yt-dlp against clips served from 127.0.0.1, so they
// only run with MEDIA_TOOLS_YTDLP=1 set

// localClipServer serves a clip made from a testsrc2 spec over loopback
func localClipServer(t *testing.T, name, spec string, extra ...string) string {
	t.Helper()
	if os.Getenv("MEDIA_TOOLS_YTDLP") != "1" {
		t.Skip("set MEDIA_TOOLS_YTDLP=1 to run yt-dlp against a local clip")
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp is not installed")
	}
	bins := needFFmpeg(t)
	dir := t.TempDir()
	args := []string{"-f", "lavfi", "-i", spec}
	if len(extra) == 0 {
		extra = []string{"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p"}
	}
	args = append(append(args, extra...), "-y", filepath.Join(dir, name))
	run(t, bins.FFmpeg, args...)
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return srv.URL + "/" + name
}

// gifRate counts a GIF's frames and divides by its length
func gifRate(t *testing.T, path string) (frames int, secs, rate float64) {
	t.Helper()
	bins := needFFmpeg(t)
	out, err := exec.Command(bins.FFprobe, "-v", "error", "-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	frames, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	secs = probeDuration(t, path)
	if secs > 0 {
		rate = float64(frames) / secs
	}
	return frames, secs, rate
}

// oneGIF runs Download Video as GIF and returns the single GIF it made
func oneGIF(t *testing.T, link string, opts map[string]string) string {
	t.Helper()
	opts["container"] = "gif"
	files, log, err := runDownload(t, link, opts)
	if err != nil {
		t.Fatalf("yt-dlp failed: %v\n%s", err, log)
	}
	if len(files) != 1 || filepath.Ext(files[0]) != ".gif" {
		t.Fatalf("made %v, want one GIF\n%s", files, log)
	}
	return files[0]
}

// TestYtdlpGIFOriginalRateCaps checks Original frame rate keeps the
// source's rate, fractional ones included, and brings faster video to 50
func TestYtdlpGIFOriginalRateCaps(t *testing.T) {
	cases := []struct {
		name, rate string
		secs       float64
		want       float64
	}{
		{"60", "60", 6, 50},
		{"24", "24", 6, 24},
		{"23.976", "24000/1001", 6, 23.976},
		{"29.97", "30000/1001", 6, 29.97},
		{"2", "2", 6, 2},
		{"0.5", "1/2", 10, 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			link := localClipServer(t, "clip.mp4", "testsrc2=s=320x180:r="+c.rate+":d="+strconv.FormatFloat(c.secs, 'f', -1, 64))
			gif := oneGIF(t, link, map[string]string{"span": "original", "sizing": "original", "pace": "original"})
			frames, secs, rate := gifRate(t, gif)
			t.Logf("%d frames over %.3f s, %.3f fps", frames, secs, rate)
			if secs < c.secs-0.6 || secs > c.secs+0.6 {
				t.Errorf("GIF lasts %.2f s, want %.1f s", secs, c.secs)
			}
			if rate < c.want*0.95 || rate > c.want*1.05 {
				t.Errorf("GIF plays %.3f fps, want about %.3f", rate, c.want)
			}
		})
	}
}

// TestYtdlpGIFOriginalRateOnAClip checks the 50 fps cap also holds on a cut
// section, which yt-dlp re-encodes before the GIF stage sees it
func TestYtdlpGIFOriginalRateOnAClip(t *testing.T) {
	link := localClipServer(t, "clip.mp4", "testsrc2=s=320x180:r=60:d=12")
	gif := oneGIF(t, link, map[string]string{"span": "clip", "start": "00:00:02", "end": "00:00:06",
		"sizing": "set", "width": "200", "pace": "original"})
	frames, secs, rate := gifRate(t, gif)
	t.Logf("%d frames over %.3f s, %.3f fps", frames, secs, rate)
	if secs < 3.5 || secs > 4.5 {
		t.Errorf("GIF lasts %.2f s, want 4 s", secs)
	}
	if rate < 47.5 || rate > 52.5 {
		t.Errorf("GIF plays %.3f fps, want 50", rate)
	}
}

// TestYtdlpGIFOriginalOddWidth checks Original keeps an odd source width
func TestYtdlpGIFOriginalOddWidth(t *testing.T) {
	// testsrc2 rounds to even sizes and 4:2:0 can't hold odd ones, so scale
	// to the odd size and keep 4:4:4
	link := localClipServer(t, "odd.mp4", "testsrc2=s=320x180:r=10:d=3",
		"-vf", "scale=321:181", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv444p")
	gif := oneGIF(t, link, map[string]string{"span": "original", "sizing": "original", "pace": "set", "fps": "10"})
	if streams := probeStreams(t, gif); streams != "gif,video,321" {
		t.Errorf("streams = %q, want one 321 px gif video stream", streams)
	}
}

// TestYtdlpGIFOriginalRateVariableSource checks a variable frame rate WebM,
// where the source rate is a guess, still makes a GIF no faster than 50
func TestYtdlpGIFOriginalRateVariableSource(t *testing.T) {
	// Frames bunch up then spread out, so the timestamps vary
	link := localClipServer(t, "vfr.webm", "testsrc2=s=320x180:r=30:d=6",
		"-vf", "setpts='N/(30*TB)+0.05*floor(N/15)/TB'", "-fps_mode", "vfr",
		"-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", "-b:v", "200k")
	gif := oneGIF(t, link, map[string]string{"span": "original", "sizing": "original", "pace": "original"})
	frames, secs, rate := gifRate(t, gif)
	t.Logf("%d frames over %.3f s, %.3f fps", frames, secs, rate)
	if frames == 0 || rate > 50.5 {
		t.Errorf("GIF has %d frames at %.3f fps, want some, at 50 fps or less", frames, rate)
	}
}

// TestYtdlpGIFWholeVideoIgnoresRememberedBadTimes checks a whole-video GIF
// still runs with an end before the start left over from Clip
func TestYtdlpGIFWholeVideoIgnoresRememberedBadTimes(t *testing.T) {
	link := localClipServer(t, "clip.mp4", "testsrc2=s=320x180:r=10:d=5")
	gif := oneGIF(t, link, map[string]string{"span": "original", "start": "00:00:09", "end": "00:00:01"})
	if secs := probeDuration(t, gif); secs < 4.5 || secs > 5.5 {
		t.Errorf("GIF lasts %.2f s, want the whole 5 s", secs)
	}
}
