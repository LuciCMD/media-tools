package tools

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// processesMentioning lists the PID and name of every process whose command
// line holds marker, a temp folder only this test uses
func processesMentioning(t *testing.T, marker string) []string {
	t.Helper()
	script := "Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -and $_.CommandLine.Contains('" +
		strings.ReplaceAll(marker, "'", "''") + "') -and $_.ProcessId -ne $PID } | " +
		`ForEach-Object { "$($_.ProcessId) $($_.Name) $($_.CommandLine)" }`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		t.Fatalf("listing processes: %v", err)
	}
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.Contains(strings.ToLower(line), "powershell") {
			found = append(found, line)
		}
	}
	return found
}

// TestYtdlpGIFCancelStopsTheConversion checks cancelling a whole-video GIF
// job, run the way the queue runs it, ends promptly and leaves no ffmpeg
// behind still converting, since Whole Video makes that stage long and large
func TestYtdlpGIFCancelStopsTheConversion(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("lists processes with PowerShell")
	}
	link := localClipServer(t, "long.mp4", "testsrc2=s=320x180:r=30:d=240")
	ytdlp, _ := exec.LookPath("yt-dlp")
	tool, _ := Get("download.video")
	valid, err := tool.Validate(map[string]string{"container": "gif", "span": "original",
		"sizing": "original", "pace": "original"})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	plan, err := tool.Build(Inputs{Path: link, Output: out, Options: valid}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--ignore-config", "--cache-dir", t.TempDir()}, plan.Args...)

	// Anything this test started and left running is stopped by its PID
	t.Cleanup(func() {
		for _, p := range processesMentioning(t, out) {
			pid, _ := strconv.Atoi(strings.Fields(p)[0])
			if pid > 0 {
				_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ffmpeg.Run(ctx, ffmpeg.Request{Binary: ytdlp, Args: args}, nil)
		done <- err
	}()

	// Wait for the GIF stage, the ffmpeg that runs the palette graph
	deadline := time.Now().Add(90 * time.Second)
	converting := false
	for time.Now().Before(deadline) && !converting {
		for _, p := range processesMentioning(t, out) {
			if strings.Contains(p, "ffmpeg") && strings.Contains(p, "palettegen") {
				converting = true
			}
		}
		select {
		case err := <-done:
			t.Fatalf("yt-dlp ended before the GIF stage could be cancelled: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
	}
	if !converting {
		t.Fatal("the GIF stage never started")
	}
	time.Sleep(time.Second)
	cancel()
	cancelled := time.Now()

	time.Sleep(2 * time.Second)
	if left := processesMentioning(t, out); len(left) > 0 {
		t.Errorf("2 s after cancel these still run:\n%s", strings.Join(left, "\n"))
	}
	select {
	case <-done:
		if waited := time.Since(cancelled); waited > 4*time.Second {
			t.Errorf("Run returned %.1f s after cancel, only once the conversion ended", waited.Seconds())
		}
	case <-time.After(60 * time.Second):
		t.Errorf("Run still hadn't returned 60 s after cancel, so the job keeps showing as running")
	}
	time.Sleep(2 * time.Second)
	if left := processesMentioning(t, out); len(left) > 0 {
		t.Errorf("after cancel these still run:\n%s", strings.Join(left, "\n"))
	}
}
