package ffmpeg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxDownloadEntries bounds how much of a work folder FindDownload reads
const maxDownloadEntries = 256

// FindDownload returns the file yt-dlp left in dir, a folder the download had
// to itself, skipping its partial and fragment files, and taking the largest
// should there be more than one
func FindDownload(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("couldn't read the download's folder: %w", err)
	}
	best, size := "", int64(-1)
	for i := 0; i < len(entries) && i < maxDownloadEntries; i++ {
		e := entries[i]
		if !e.Type().IsRegular() || unfinishedDownload(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() <= size {
			continue
		}
		best, size = filepath.Join(dir, e.Name()), info.Size()
	}
	if best == "" {
		return "", errors.New("the download finished but left no file to convert")
	}
	return best, nil
}

// unfinishedDownload reports names yt-dlp gives work in progress, matched at
// the end only, since a title may hold .temp. or .part-frag itself
func unfinishedDownload(name string) bool {
	lower := strings.ToLower(name)
	stem := strings.TrimSuffix(lower, filepath.Ext(lower))
	return strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".ytdl") ||
		partFragment.MatchString(lower) || strings.HasSuffix(stem, ".temp")
}

// partFragment is a fragment of a download in pieces, such as x.mp4.part-Frag12
var partFragment = regexp.MustCompile(`\.part-frag[0-9]+$`)
