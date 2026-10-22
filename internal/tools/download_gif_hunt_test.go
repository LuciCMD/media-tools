package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// gifGraph builds Download Video as GIF for opts and returns its -vf graph,
// unwrapped from the double quotes yt-dlp splits on
func gifGraph(t *testing.T, opts map[string]string) (Plan, string) {
	t.Helper()
	opts["container"] = "gif"
	plan := buildVideoDownload(t, opts)
	pp := argValue(plan.Args, "--postprocessor-args")
	_, rest, ok := strings.Cut(pp, `-vf "`)
	if !ok {
		t.Fatalf("no quoted -vf in %q", pp)
	}
	graph, _, ok := strings.Cut(rest, `"`)
	if !ok {
		t.Fatalf("unterminated -vf quote in %q", pp)
	}
	return plan, graph
}

// TestDownloadGIFEveryChoiceCombination checks each of the eight span,
// sizing and pace combinations gives the cut, scale and rate it names
func TestDownloadGIFEveryChoiceCombination(t *testing.T) {
	for _, span := range []string{"clip", "original"} {
		for _, sizing := range []string{"set", "original"} {
			for _, pace := range []string{"set", "original"} {
				name := span + "-" + sizing + "-" + pace
				t.Run(name, func(t *testing.T) {
					plan, graph := gifGraph(t, map[string]string{"span": span, "sizing": sizing, "pace": pace,
						"start": "00:00:02", "end": "00:00:04", "width": "300", "fps": "12"})
					cut := slices.Contains(plan.Args, "--download-sections")
					if cut != (span == "clip") {
						t.Errorf("cuts a section = %v for span %s: %v", cut, span, plan.Args)
					}
					if cut && !argsContain(plan.Args, "--download-sections", "*00:00:02-00:00:04") {
						t.Errorf("wrong section: %v", plan.Args)
					}
					if cut != slices.Contains(plan.Args, "--force-keyframes-at-cuts") {
						t.Errorf("keyframe forcing doesn't follow the cut: %v", plan.Args)
					}
					if strings.Contains(graph, "scale=300:-1") != (sizing == "set") {
						t.Errorf("sizing %s gives graph %q", sizing, graph)
					}
					if sizing == "original" && strings.Contains(graph, "scale") {
						t.Errorf("original sizing still scales: %q", graph)
					}
					wantRate := "fps=12,"
					if pace == "original" {
						wantRate = "fps='min(source_fps,50)'"
					}
					if !strings.HasPrefix(graph, wantRate) {
						t.Errorf("pace %s gives graph %q, want it to start %q", pace, graph, wantRate)
					}
					if strings.Count(graph, `"`) != 0 || strings.Count(graph, "'")%2 != 0 {
						t.Errorf("graph %q has a stray quote", graph)
					}
				})
			}
		}
	}
}

// TestDownloadGIFRememberedFromBeforeTheChange checks settings saved before
// span, sizing and pace existed still give the old clip, width and rate
func TestDownloadGIFRememberedFromBeforeTheChange(t *testing.T) {
	plan, graph := gifGraph(t, map[string]string{"start": "00:00:01", "end": "00:00:03", "width": "320", "fps": "10"})
	if !argsContain(plan.Args, "--download-sections", "*00:00:01-00:00:03") {
		t.Errorf("old settings lost the clip: %v", plan.Args)
	}
	if !strings.HasPrefix(graph, "fps=10,scale=320:-1:flags=lanczos,") {
		t.Errorf("old settings lost the width or rate: %q", graph)
	}
}

// TestDownloadGIFRefusesUnknownChoices checks a span, sizing or pace that
// isn't offered is refused, since anything not "clip" would drop the cut
func TestDownloadGIFRefusesUnknownChoices(t *testing.T) {
	tool, _ := Get("download.video")
	for _, key := range []string{"span", "sizing", "pace"} {
		for _, v := range []string{"whole", "Original", `original" -y "`, "set;rm"} {
			if _, err := tool.Validate(map[string]string{"container": "gif", key: v}); err == nil {
				t.Errorf("%s=%q passes validation", key, v)
			}
		}
	}
}

// TestDownloadGIFNumbersStayPositiveInTheGraph checks every number the
// validator lets through reaches the graph as a positive whole number
func TestDownloadGIFNumbersStayPositiveInTheGraph(t *testing.T) {
	tool, _ := Get("download.video")
	cases := []map[string]string{
		{"fps": "NaN"},
		{"width": "NaN"},
		{"fps": "nan"},
		{"width": "+NaN"},
		{"fps": "49.99"},
		{"width": "1e3"},
		{"width": "0x1p5"},
		{"fps": "1_0"},
	}
	num := regexp.MustCompile(`fps=(-?\d+)|scale=(-?\d+):`)
	for _, c := range cases {
		c["container"] = "gif"
		valid, err := tool.Validate(c)
		if err != nil {
			continue
		}
		plan, err := tool.Build(defaultInputs(tool, valid), softwareEncode())
		if err != nil {
			continue
		}
		pp := argValue(plan.Args, "--postprocessor-args")
		for _, m := range num.FindAllStringSubmatch(pp, -1) {
			for _, s := range m[1:] {
				if s == "" {
					continue
				}
				if n, _ := strconv.Atoi(s); n < 1 || n > 1920 {
					t.Errorf("%v reaches the graph as %s: %q", c, s, pp)
				}
			}
		}
	}
}

// gifPeakMemory runs ffmpeg with the GIF graph on a generated clip of secs
// seconds and returns its peak memory in KiB, as -benchmark reports it
func gifPeakMemory(t *testing.T, bin, graph string, secs int) int {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	run(t, bin, "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=s=640x360:r=30:d=%d", secs),
		"-c:v", "libx264", "-preset", "ultrafast", "-y", src)
	out, err := exec.Command(bin, "-hide_banner", "-nostdin", "-benchmark", "-i", src,
		"-an", "-sn", "-vf", graph, "-loop", "0", "-y", filepath.Join(dir, "out.gif")).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`maxrss=(\d+)KiB`).FindStringSubmatch(string(out))
	if m == nil {
		t.Skipf("this ffmpeg doesn't report maxrss:\n%s", out)
	}
	kib, _ := strconv.Atoi(m[1])
	return kib
}

// TestDownloadGIFWholeVideoMemoryIsBounded checks the GIF graph's memory
// doesn't grow with the video's length, since Whole Video now feeds it the
// entire download, and the one-pass palette holds every frame until the end
func TestDownloadGIFWholeVideoMemoryIsBounded(t *testing.T) {
	bins := needFFmpeg(t)
	_, graph := gifGraph(t, map[string]string{"span": "original", "sizing": "original", "pace": "original"})
	short := gifPeakMemory(t, bins.FFmpeg, graph, 4)
	long := gifPeakMemory(t, bins.FFmpeg, graph, 16)
	t.Logf("peak memory: 4 s %d MiB, 16 s %d MiB", short/1024, long/1024)
	if long > short*2 {
		t.Errorf("peak memory grows with length: 4 s of 640x360 takes %d MiB, 16 s takes %d MiB, "+
			"so a whole video at its own size can run out of memory", short/1024, long/1024)
	}
}
