//go:build windows

package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestHuntNestedRunner stands in for the app started inside someone else's
// job, as a launcher or terminal may do, running one cancelled tree and
// writing what happened to MEDIA_TOOLS_NESTED_OUT
func TestHuntNestedRunner(t *testing.T) {
	out := os.Getenv("MEDIA_TOOLS_NESTED_OUT")
	if out == "" {
		return
	}
	// Wait until the test has put us in its job
	goFile := out + ".go"
	for i := 0; i < 400; i++ {
		if _, err := os.Stat(goFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	logs := captureLog(t)
	beat := out + ".beat"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(beat); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	_, err := Run(ctx, huntRequest("tree", beat, "100"), nil)
	took := time.Since(start)
	stopped, before, after := beatStopped(beat)
	_ = os.WriteFile(out, []byte(fmt.Sprintf("err=%v\ntook=%v\nstopped=%v (%s then %s)\nlog=%s",
		err, took.Round(time.Millisecond), stopped, before, after, strings.TrimSpace(logs.String()))), 0o600)
	os.Exit(0)
}

// runNested starts the nested runner inside a fresh job with the given limit
// flags and optional UI restrictions, returning what the runner wrote
func runNested(t *testing.T, flags uint32, ui uint32) string {
	t.Helper()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			// The outer job ends whatever is left when this test closes it
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | flags,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal(err)
	}
	if ui != 0 {
		r := windows.JOBOBJECT_BASIC_UI_RESTRICTIONS{UIRestrictionsClass: ui}
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions,
			uintptr(unsafe.Pointer(&r)), uint32(unsafe.Sizeof(r))); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("MEDIA_TOOLS_HUNT", "1")
	out := filepath.Join(t.TempDir(), "nested")
	runner := exec.Command(os.Args[0], "-test.run=^TestHuntNestedRunner$")
	runner.Env = append(os.Environ(), "MEDIA_TOOLS_NESTED_OUT="+out)
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(runner.Process.Pid))
	if err != nil {
		_ = runner.Process.Kill()
		t.Fatal(err)
	}
	err = windows.AssignProcessToJobObject(job, proc)
	windows.CloseHandle(proc)
	if err != nil {
		_ = runner.Process.Kill()
		t.Fatal(err)
	}
	_ = os.WriteFile(out+".go", nil, 0o600)

	waited := make(chan struct{})
	go func() { _ = runner.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(30 * time.Second):
		_ = runner.Process.Kill()
		<-waited
		t.Fatal("the nested runner didn't finish")
	}
	got, _ := os.ReadFile(out)
	return string(got)
}

// TestHuntNestedInOuterJob checks the tree still works when the app itself
// already sits in a job
func TestHuntNestedInOuterJob(t *testing.T) {
	got := runNested(t, 0, 0)
	t.Logf("inside a plain job:\n%s", got)
	if !strings.Contains(got, "err=context canceled") || !strings.Contains(got, "stopped=true") ||
		strings.Contains(got, "cancel can only stop") {
		t.Errorf("a cancel inside an outer job didn't end the tree:\n%s", got)
	}
}

// TestHuntNestedInSilentBreakawayJob checks the same where the outer job
// lets children leave it silently
func TestHuntNestedInSilentBreakawayJob(t *testing.T) {
	got := runNested(t, windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK, 0)
	t.Logf("inside a silent breakaway job:\n%s", got)
	if !strings.Contains(got, "err=context canceled") || !strings.Contains(got, "stopped=true") ||
		strings.Contains(got, "cancel can only stop") {
		t.Errorf("a cancel inside a silent breakaway job didn't end the tree:\n%s", got)
	}
}

// TestHuntNestedInUIRestrictedJob records what happens when the outer job
// has UI limits, which Windows won't nest under
func TestHuntNestedInUIRestrictedJob(t *testing.T) {
	got := runNested(t, 0, windows.JOB_OBJECT_UILIMIT_HANDLES)
	t.Logf("inside a UI restricted job:\n%s", got)
	if !strings.Contains(got, "err=context canceled") {
		t.Errorf("a cancel inside a UI restricted job didn't report as cancelled:\n%s", got)
	}
}
