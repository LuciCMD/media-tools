package app

import (
	"path/filepath"
	"strings"
	"testing"

	"media-tools/internal/tools"
)

// TestCheckFileOptionsFindsMissingFiles checks a chosen file that isn't
// there is reported before a job is queued, while a hidden option's
// leftover value is ignored.
func TestCheckFileOptionsFindsMissingFiles(t *testing.T) {
	tool, err := tools.Get("video.burnsubs")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	present := touch(t, dir, "subs.srt")
	missing := filepath.Join(dir, "gone.srt")

	if err := checkFileOptions(tool, map[string]string{"from": "file", "subfile": present}); err != nil {
		t.Errorf("a file that exists was refused: %v", err)
	}
	err = checkFileOptions(tool, map[string]string{"from": "file", "subfile": missing})
	if err == nil || !strings.Contains(err.Error(), "can't be found") {
		t.Errorf("a missing file gave %v, want it reported", err)
	}
	if err := checkFileOptions(tool, map[string]string{"from": "auto", "subfile": missing}); err != nil {
		t.Errorf("a hidden option's value was checked: %v", err)
	}
}

// TestCheckFileOptionsFollowsWhenChains checks a file option is skipped when
// the option it depends on is itself hidden, as the page hides it
func TestCheckFileOptionsFollowsWhenChains(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.srt")
	tool := tools.Tool{Options: []tools.Option{
		{Key: "mode", Kind: tools.OptSelect, Choices: []tools.Choice{{Value: "a"}, {Value: "b"}}},
		{Key: "source", Kind: tools.OptSelect, When: "mode=b", Choices: []tools.Choice{{Value: "file"}}},
		{Key: "path", Kind: tools.OptFile, When: "source=file", Accept: []string{"srt"}},
	}}
	if err := checkFileOptions(tool, map[string]string{"mode": "a", "source": "file", "path": missing}); err != nil {
		t.Errorf("a file under a hidden option was checked: %v", err)
	}
	if err := checkFileOptions(tool, map[string]string{"mode": "b", "source": "file", "path": missing}); err == nil {
		t.Error("a missing file under shown options wasn't reported")
	}
}
