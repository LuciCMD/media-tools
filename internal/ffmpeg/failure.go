package ffmpeg

import (
	"regexp"
	"strings"
)

// maxReasonChars bounds the reason shown for a failed job.
const maxReasonChars = 220

// trailers begin the lines ffmpeg prints after the real error, which only say
// that something failed. "Error opening output files: Operation not
// permitted" follows a filter that couldn't start, for one, and names
// neither the filter nor why.
var trailers = []string{
	"Error opening output file",
	"Error opening input file",
	"Error initializing filter",
	"Error initializing complex filter",
	"Error while filtering",
	"Error while processing the decoded data",
	"Error reinitializing filters",
	"Conversion failed!",
	"Nothing was written into output file",
	"Terminating thread",
	"Exiting normally",
}

// explanations turn causes ffmpeg words for its developers into ones a user
// can act on. Matched by substring, first match wins.
var explanations = []struct{ cause, reason string }{
	{"Unable to locate subtitle stream",
		"the video has no subtitle track to use, and no subtitle file with its name was found beside it"},
	{"No such filter: 'whisper'",
		"this ffmpeg can't transcribe speech: version 8.0 or later, built with whisper, is needed"},
	{"No space left on device", "the disk is full"},
	{"Permission denied", "the file or folder can't be written; it may be read-only or open in another program"},
}

// component matches the "[Parsed_subtitles_0 @ 000001cb865ce8c0] " prefix
// ffmpeg puts before a component's message.
var component = regexp.MustCompile(`^\[[^\]]+ @ (0x)?[0-9a-fA-F]+\]\s*`)

// failureReason picks the line of ffmpeg's output that says why it failed:
// the last line that isn't a generic trailer, in plain words where a known
// cause allows. When only trailers are there, the last of them is used.
func failureReason(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	fallback := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := component.ReplaceAllString(strings.TrimSpace(lines[i]), "")
		if line == "" || strings.HasPrefix(line, "frame=") || strings.HasPrefix(line, "size=") {
			continue
		}
		if fallback == "" {
			fallback = line
		}
		if !isTrailer(line) {
			return explain(line)
		}
	}
	if fallback == "" {
		return "the command failed with no diagnostic output"
	}
	return explain(fallback)
}

// isTrailer reports whether line only says that something failed.
func isTrailer(line string) bool {
	for _, t := range trailers {
		if strings.HasPrefix(line, t) {
			return true
		}
	}
	return false
}

// explain replaces a known cause with plain words, keeping the result short.
func explain(line string) string {
	for _, e := range explanations {
		if strings.Contains(line, e.cause) {
			return e.reason
		}
	}
	if len(line) > maxReasonChars {
		line = line[:maxReasonChars] + "..."
	}
	return line
}
