package jobs

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// TestJobsProgressHelper stands in for yt-dlp, printing its templated
// progress lines with real gaps, doing nothing unless MEDIA_TOOLS_JOBS_PROGRESS is set
func TestJobsProgressHelper(t *testing.T) {
	if os.Getenv("MEDIA_TOOLS_JOBS_PROGRESS") != "1" {
		return
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(3)
	}
	part := func(total int, vcodec, acodec string) {
		line := func(status string, got int) {
			fmt.Println(templatedLine(map[string]string{"progress.status": status,
				"progress.downloaded_bytes": strconv.Itoa(got), "progress.total_bytes": strconv.Itoa(total),
				"progress.speed": "1000000", "progress.eta": strconv.Itoa((total - got) / 1000000),
				"info.vcodec": vcodec, "info.acodec": acodec}))
		}
		for got := 0; got < total; got += total / 50 {
			line("downloading", got)
			time.Sleep(5 * time.Millisecond)
		}
		line("finished", total)
	}
	switch args[0] {
	case "convert":
		// A GIF, the conversion starting the moment the download ends and running long
		part(10_000_000, "avc1.64001f", "mp4a.40.2")
		fmt.Println("mediatools-pp VideoConvertor started")
		fmt.Println("[VideoConvertor] Converting video from mp4 to gif")
		time.Sleep(1500 * time.Millisecond)
		fmt.Println("mediatools-pp VideoConvertor finished")
		fmt.Println("mediatools-pp MoveFiles started")
		fmt.Println("mediatools-pp MoveFiles finished")
		os.Exit(0)
	case "merge":
		// A site serving picture and sound apart, then a long merge
		part(10_000_000, "avc1.64001f", "none")
		part(2_000_000, "none", "mp4a.40.2")
		fmt.Println("mediatools-pp Merger started")
		time.Sleep(1500 * time.Millisecond)
		fmt.Println("mediatools-pp Merger finished")
		os.Exit(0)
	}
	os.Exit(4)
}

// templatedLine renders DownloadProgressArgs' download template the way
// yt-dlp would, NA for any field not given, whatever order its fields are in
func templatedLine(values map[string]string) string {
	args := ffmpeg.DownloadProgressArgs()
	tmpl := ""
	for i := 0; i+1 < len(args); i += 2 {
		if strings.HasPrefix(args[i+1], "download:") {
			tmpl = strings.TrimPrefix(args[i+1], "download:")
		}
	}
	ref := regexp.MustCompile(`%\(([a-z_.]+)\)s`)
	return ref.ReplaceAllStringFunc(tmpl, func(m string) string {
		if v, ok := values[ref.FindStringSubmatch(m)[1]]; ok {
			return v
		}
		return "NA"
	})
}

// progressArgsFor builds the helper's argument vector
func progressArgsFor(mode string) []string {
	return []string{"-test.run=^TestJobsProgressHelper$", "--", mode}
}

// runRecorded runs one helper job through the queue and returns every
// snapshot the listener saw while it was running, each with when it came
func runRecorded(t *testing.T, mode string) ([]Job, []time.Time) {
	t.Helper()
	t.Setenv("MEDIA_TOOLS_JOBS_PROGRESS", "1")
	var mu sync.Mutex
	var seen []Job
	var at []time.Time
	q, err := NewQueue(1, func(j Job) {
		if j.State != StateRunning {
			return
		}
		mu.Lock()
		seen = append(seen, j)
		at = append(at, time.Now())
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Shutdown()
	id, err := q.Submit(Spec{Tool: "download.video", Binary: os.Args[0], Output: t.TempDir(), Args: progressArgsFor(mode)})
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(30*time.Second, func() bool {
		s := stateOf(q, id)
		return s == StateSucceeded || s == StateFailed || s == StateCanceled
	}) {
		t.Fatal("the job never finished")
	}
	if s := stateOf(q, id); s != StateSucceeded {
		t.Fatalf("the job ended %q", s)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]Job(nil), seen...), append([]time.Time(nil), at...)
}

// TestHuntQueueShowsAStepThatStartsRightAfterTheDownload checks a long step
// that starts the moment the download ends, such as a GIF conversion, gets
// its name on screen, rather than the throttle dropping its one line and
// leaving the last download sample, 100% with 0:00 left, up for the whole step
func TestHuntQueueShowsAStepThatStartsRightAfterTheDownload(t *testing.T) {
	seen, _ := runRecorded(t, "convert")
	if len(seen) == 0 {
		t.Fatal("no progress while running")
	}
	for _, j := range seen {
		if j.Progress.Stage == "Converting" {
			return
		}
	}
	last := seen[len(seen)-1].Progress
	t.Errorf("the conversion was never shown, the last sample while running was fraction %.3f, eta %.0f, stage %q",
		last.Fraction, last.ETASeconds, last.Stage)
}

// TestHuntQueueShowsTheMerge checks the same for the merge after separate
// video and audio parts
func TestHuntQueueShowsTheMerge(t *testing.T) {
	seen, _ := runRecorded(t, "merge")
	for _, j := range seen {
		if j.Progress.Stage == "Merging video and audio" {
			return
		}
	}
	last := seen[len(seen)-1].Progress
	t.Errorf("the merge was never shown, the last sample while running was fraction %.3f, eta %.0f, stage %q",
		last.Fraction, last.ETASeconds, last.Stage)
}

// TestHuntQueueFractionNeverGoesBack checks the bar of one download only
// moves forward within a part, and that when the audio follows the video it
// carries on from the video's bytes, where each part used to run 0 to 100%
// The audio's size is unknown until it starts, so the total grows then and
// the bar may dip a little, but never back towards the start
func TestHuntQueueFractionNeverGoesBack(t *testing.T) {
	seen, _ := runRecorded(t, "merge")
	last := -1.0
	stage := ""
	for _, j := range seen {
		f := j.Progress.Fraction
		if f < 0 {
			continue
		}
		newPart := j.Progress.Stage != stage
		if !newPart && f < last-0.001 {
			t.Errorf("the bar went back from %.1f%% to %.1f%% within %s", last*100, f*100, j.Progress.Stage)
			return
		}
		if newPart && last >= 0 && f < last*0.75 {
			t.Errorf("the bar restarted from %.1f%% to %.1f%% as %s began", last*100, f*100, j.Progress.Stage)
			return
		}
		last, stage = f, j.Progress.Stage
	}
}
