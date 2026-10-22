package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
	"media-tools/internal/jobs"
)

// TestHunt2YtdlpRetryClipAfterWorkFolderWent retries a clip GIF that failed
// after its work folder was removed, the way RetryJob reuses the spec, which
// must make the folder again and leave just the GIF
func TestHunt2YtdlpRetryClipAfterWorkFolderWent(t *testing.T) {
	link, bins := localClip(t, "12")
	out := t.TempDir()
	tool, plan, work := gifPlan(t, link, out, map[string]string{"container": "gif", "start": "00:00:02", "end": "00:00:05"})
	spec := downloadSpec(tool, link, out, work, plan, bins)
	end, _ := gifJobRun(t, spec, "")
	if end.State != jobs.StateSucceeded {
		t.Fatalf("the first run ended %s: %s", end.State, end.Error)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("the work folder is still there: %v", err)
	}
	gif := outFiles(t, out)
	spec.Args = retryDownloadArgs(spec.Args)
	end, _ = gifJobRun(t, spec, "")
	if end.State != jobs.StateSucceeded {
		t.Fatalf("the retry ended %s: %s", end.State, end.Error)
	}
	if names := outFiles(t, out); len(names) != 1 || len(gif) != 1 || names[0] != gif[0] {
		t.Errorf("after the retry out holds %q, before %q", names, gif)
	}
}

// hunt2SlowFile reads its file slowly so a download can be caught midway
type hunt2SlowFile struct{ *os.File }

func (s hunt2SlowFile) Read(p []byte) (int, error) {
	time.Sleep(30 * time.Millisecond)
	if len(p) > 16<<10 {
		p = p[:16<<10]
	}
	return s.File.Read(p)
}

// hunt2SlowServer serves a clip of seconds slowly over loopback
func hunt2SlowServer(t *testing.T, seconds string) (string, ffmpeg.Binaries) {
	t.Helper()
	link, bins := localClip(t, seconds)
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	// localClip's own server has the clip, served again here slowly from a copy
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || os.WriteFile(clip, data, 0o600) != nil {
		t.Fatal("couldn't copy the clip", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(clip)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer f.Close()
		info, _ := f.Stat()
		http.ServeContent(w, r, "clip.mp4", info.ModTime(), hunt2SlowFile{f})
	}))
	t.Cleanup(srv.Close)
	return srv.URL + u.Path, bins
}

// TestHunt2YtdlpCancelMidDownloadLeavesNothing cancels GIF downloads while
// yt-dlp is still fetching, a clip cut by yt-dlp's own ffmpeg and a whole
// video, and checks the output folder stays empty, also a while later, so no
// child left running writes into a work folder after it was removed
func TestHunt2YtdlpCancelMidDownloadLeavesNothing(t *testing.T) {
	for name, opts := range map[string]map[string]string{
		"clip":  {"container": "gif", "start": "00:00:02", "end": "00:00:50"},
		"whole": {"container": "gif", "span": "original"},
	} {
		t.Run(name, func(t *testing.T) {
			link, bins := hunt2SlowServer(t, "60")
			out := t.TempDir()
			tool, plan, work := gifPlan(t, link, out, opts)
			spec := downloadSpec(tool, link, out, work, plan, bins)
			spec.Args = append([]string{"--ignore-config", "--cache-dir", t.TempDir()}, spec.Args...)
			q, err := jobs.NewQueue(1, func(jobs.Job) {})
			if err != nil {
				t.Fatal(err)
			}
			q.Start()
			defer q.Shutdown()
			id, err := q.Submit(spec)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(4 * time.Second)
			for _, j := range q.List() {
				if j.ID == id && (j.State != jobs.StateRunning || j.Progress.Pass > 1) {
					t.Fatalf("the download wasn't still running: %s pass %d", j.State, j.Progress.Pass)
				}
			}
			if err := q.Cancel(id); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				ended := false
				for _, j := range q.List() {
					if j.ID == id && j.State == jobs.StateCanceled {
						ended = true
					}
				}
				if ended {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if names := outFiles(t, out); len(names) != 0 {
				t.Errorf("right after cancelling out holds %q", names)
			}
			time.Sleep(3 * time.Second)
			if names := outFiles(t, out); len(names) != 0 {
				t.Errorf("3 s after cancelling out holds %q", names)
			}
		})
	}
}

// TestHunt2YtdlpSameVideoTwiceAtOnce downloads one clip as a GIF twice at the
// same time into one folder, as two clicks of Download would, which must
// both succeed and leave one whole GIF and no work folders
func TestHunt2YtdlpSameVideoTwiceAtOnce(t *testing.T) {
	link, bins := localClip(t, "12")
	out := t.TempDir()
	var wg sync.WaitGroup
	ends := make([]jobs.Job, 2)
	for i := range ends {
		tool, plan, work := gifPlan(t, link, out, map[string]string{"container": "gif", "start": "00:00:01", "end": "00:00:04"})
		spec := downloadSpec(tool, link, out, work, plan, bins)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ends[i], _ = gifJobRun(t, spec, "")
		}()
	}
	wg.Wait()
	for i, e := range ends {
		if e.State != jobs.StateSucceeded {
			t.Errorf("job %d ended %s: %s", i, e.State, e.Error)
		}
	}
	names := outFiles(t, out)
	if len(names) != 1 || !strings.HasSuffix(names[0], ".gif") {
		t.Errorf("out holds %q, want one GIF", names)
	}
}
