package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"media-tools/internal/ffmpeg"
)

// fixupSpec is a job whose one command writes staged, then fixup runs and
// the result is kept at final
func fixupSpec(staged, final string, mode []string, fixup func() error) Spec {
	return Spec{
		Tool: "t", Binary: os.Args[0], Output: final,
		Prepare: func(context.Context, ffmpeg.Totals) (Commands, error) {
			return Commands{Args: mode, Fixup: fixup, Keep: &Move{From: staged, To: final}}, nil
		},
	}
}

// failureOf is the error of a job that failed, or ""
func failureOf(c *collector) string {
	for _, u := range c.snapshot() {
		if u.State == StateFailed {
			return u.Error
		}
	}
	return ""
}

// TestFixupRunsBetweenTheCommandsAndKeep checks the fix-up sees the staged
// result before it's moved, and the job succeeds with it in place
func TestFixupRunsBetweenTheCommandsAndKeep(t *testing.T) {
	q, c, _, work, final := thenSetup(t, StateSucceeded)
	staged := filepath.Join(work, "staged")
	ran := false
	_, err := q.Submit(fixupSpec(staged, final, thenHelper("touch", staged), func() error {
		ran = true
		if _, err := os.Stat(staged); err != nil {
			return errors.New("the fix-up ran before the command wrote its result")
		}
		if _, err := os.Stat(final); err == nil {
			return errors.New("the fix-up ran after the result was moved")
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)
	if failed := failureOf(c); failed != "" {
		t.Fatalf("the job failed: %s", failed)
	}
	if !ran {
		t.Error("the fix-up never ran")
	}
	if _, err := os.Stat(final); err != nil {
		t.Errorf("the result wasn't moved into place: %v", err)
	}
}

// TestFixupFailureFailsTheJob checks a failed fix-up fails the job with its
// reason and leaves nothing where the result belongs
func TestFixupFailureFailsTheJob(t *testing.T) {
	q, c, _, work, final := thenSetup(t, StateFailed)
	staged := filepath.Join(work, "staged")
	_, err := q.Submit(fixupSpec(staged, final, thenHelper("touch", staged), func() error {
		return errors.New("frames unfixable")
	}))
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)
	if failed := failureOf(c); !strings.Contains(failed, "frames unfixable") {
		t.Errorf("the job's error is %q, want the fix-up's", failed)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Errorf("a result is in place after the fix-up failed: %v", err)
	}
}

// TestFixupIsSkippedWhenACommandFails checks nothing is fixed up after a
// command has failed
func TestFixupIsSkippedWhenACommandFails(t *testing.T) {
	q, c, _, work, final := thenSetup(t, StateFailed)
	ran := false
	_, err := q.Submit(fixupSpec(filepath.Join(work, "staged"), final, thenHelper("fail"), func() error {
		ran = true
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 30*time.Second)
	if ran {
		t.Error("the fix-up ran after the command failed")
	}
}

// TestFixupRunsInTheNextStage checks a stage Then builds gets its fix-up too
func TestFixupRunsInTheNextStage(t *testing.T) {
	q, c, bins, work, final := thenSetup(t, StateSucceeded)
	ran := false
	_, err := q.Submit(Spec{
		Tool: "t", Binary: os.Args[0], Output: filepath.Dir(final), WorkDir: work,
		Args: thenHelper("touch", filepath.Join(work, "video.mp4")), ThenCount: 1,
		Then: func(ctx context.Context) (Stage, error) {
			stage := encodeStage(bins, work, final, 1, false)
			stage.Fixup = func() error {
				ran = true
				return nil
			}
			return stage, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.await(t, 60*time.Second)
	if failed := failureOf(c); failed != "" {
		t.Fatalf("the job failed: %s", failed)
	}
	if !ran {
		t.Error("the next stage's fix-up never ran")
	}
}
