package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"media-tools/internal/tools"
)

// maxSidecarEntries bounds how much of a folder is read looking for a
// companion file (Power of Ten rule 2). A Desktop or Downloads folder can be
// large; past this, only an exact name match is found.
const maxSidecarEntries = 4096

// findSidecar returns the companion file for path with one of exts, such as
// clip.srt for clip.mp4, or "" when there is none. An exact name wins, in
// the order exts are given; then a name with a tag between, such as
// clip.en.srt, which is how yt-dlp saves subtitles.
func findSidecar(path string, exts []string) string {
	if len(exts) == 0 || path == "" {
		return ""
	}
	dir := filepath.Dir(path)
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for _, ext := range exts {
		candidate := filepath.Join(dir, stem+"."+ext)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return taggedSidecar(dir, stem, exts)
}

// taggedSidecar looks for stem.<tag>.<ext> in dir, taking the first by name
// so the choice doesn't change between runs.
func taggedSidecar(dir, stem string, exts []string) string {
	f, err := os.Open(dir)
	if err != nil {
		return ""
	}
	defer f.Close()
	entries, _ := f.ReadDir(maxSidecarEntries)

	prefix := strings.ToLower(stem) + "."
	best := ""
	for _, entry := range entries {
		name := entry.Name()
		lower := strings.ToLower(name)
		if entry.IsDir() || !strings.HasPrefix(lower, prefix) || !hasExt(lower, exts) {
			continue
		}
		// Exactly one tag between the stem and the extension.
		middle := strings.TrimSuffix(lower[len(prefix):], filepath.Ext(lower))
		if middle == "" || strings.Contains(middle, ".") {
			continue
		}
		if best == "" || name < best {
			best = name
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(dir, best)
}

// hasExt reports whether name ends in one of exts.
func hasExt(name string, exts []string) bool {
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	for _, e := range exts {
		if strings.EqualFold(ext, e) {
			return true
		}
	}
	return false
}

// checkFileOptions makes sure every file a tool's options name is there,
// so a missing one is reported now rather than by a failed job.
func checkFileOptions(tool tools.Tool, opts map[string]string) error {
	for _, o := range tool.Options {
		path := opts[o.Key]
		if o.Kind != tools.OptFile || path == "" || !optionShown(o, tool.Options, opts) {
			continue
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return fmt.Errorf("%s can't be found: %s", o.Label, path)
		}
	}
	return nil
}

// optionShown reports whether an option applies, given its When condition:
// a hidden option's leftover value is ignored.
// As on the page, the option it depends on must apply too, since a hidden
// option keeps its value
func optionShown(o tools.Option, all []tools.Option, opts map[string]string) bool {
	for depth := 0; depth < len(all); depth++ {
		key, values, found := strings.Cut(o.When, "=")
		if !found {
			return true
		}
		if !slices.Contains(strings.Split(values, "|"), opts[key]) {
			return false
		}
		parent := slices.IndexFunc(all, func(p tools.Option) bool { return p.Key == key })
		if parent < 0 {
			return true
		}
		o = all[parent]
	}
	return true
}
