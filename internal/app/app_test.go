package app

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestApp builds an App with only the state the output logic needs.
func newTestApp() *App {
	return &App{claimed: make(map[string]bool, 8)}
}

// TestClaimOutputSeparatesLiveJobs checks that two jobs resolving to the same
// name get different files, since every builder passes -y.
func TestClaimOutputSeparatesLiveJobs(t *testing.T) {
	a := newTestApp()
	target := filepath.Join(t.TempDir(), "clip.mp4")

	first, err := a.claimOutput(target, "suffix")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second, err := a.claimOutput(target, "suffix")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if first == second {
		t.Fatalf("both jobs were given %s", first)
	}
	if filepath.Base(second) != "clip (1).mp4" {
		t.Errorf("second claim = %s, want clip (1).mp4", filepath.Base(second))
	}
}

// TestClaimOutputOverwriteStillAvoidsLiveJobs checks the overwrite policy
// replaces finished files but never a file another job is still writing.
func TestClaimOutputOverwriteStillAvoidsLiveJobs(t *testing.T) {
	a := newTestApp()
	target := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	first, err := a.claimOutput(target, "overwrite")
	if err != nil || first != target {
		t.Fatalf("overwrite claim = %s, %v; want the existing path", first, err)
	}
	second, err := a.claimOutput(target, "overwrite")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second == target {
		t.Error("overwrite policy handed a live job's path to a second job")
	}
}

// TestReleaseOutputFreesThePath checks a finished job's path can be reused.
func TestReleaseOutputFreesThePath(t *testing.T) {
	a := newTestApp()
	target := filepath.Join(t.TempDir(), "clip.mp4")

	first, err := a.claimOutput(target, "overwrite")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	a.releaseOutput(first)
	again, err := a.claimOutput(target, "overwrite")
	if err != nil || again != target {
		t.Errorf("after release got %s, %v; want %s", again, err, target)
	}
}

// TestClaimOutputIgnoresCase checks that names differing only in case are
// treated as one file, as they are on Windows and macOS.
func TestClaimOutputIgnoresCase(t *testing.T) {
	a := newTestApp()
	dir := t.TempDir()

	if _, err := a.claimOutput(filepath.Join(dir, "Clip.mp4"), "suffix"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	got, err := a.claimOutput(filepath.Join(dir, "clip.MP4"), "suffix")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if filepath.Base(got) == "clip.MP4" {
		t.Error("a name differing only in case was treated as free")
	}
}

// TestClaimOutputSkipsExistingFiles checks the suffix policy on disk.
func TestClaimOutputSkipsExistingFiles(t *testing.T) {
	a := newTestApp()
	target := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := a.claimOutput(target, "suffix")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if filepath.Base(got) != "clip (1).mp4" {
		t.Errorf("claim = %s, want clip (1).mp4", filepath.Base(got))
	}
}

// TestRetryDownloadOverwrites checks a retried download redoes the work
// rather than keeping what the cancelled run left under the final name,
// and asks only once however often it's retried
func TestRetryDownloadOverwrites(t *testing.T) {
	args := []string{"-f", "b", "https://example.com/v"}
	once := retryDownloadArgs(args)
	if once[0] != "--force-overwrites" || len(once) != len(args)+1 {
		t.Fatalf("retry args %v, want --force-overwrites first", once)
	}
	if again := retryDownloadArgs(once); len(again) != len(once) {
		t.Errorf("a second retry added it again: %v", again)
	}
	if args[0] != "-f" {
		t.Errorf("the original args were changed: %v", args)
	}
}
