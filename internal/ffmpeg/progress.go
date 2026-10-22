package ffmpeg

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
)

// Bounds on progress parsing (Power of Ten rule 2).
const (
	maxProgressLines = 2_000_000
	maxLineBytes     = 64 * 1024
)

// Progress is one sampled state of a running conversion.
type Progress struct {
	Fraction   float64 `json:"fraction"`   // 0..1, -1 when indeterminate
	OutTimeS   float64 `json:"outTimeSec"` // position within the output
	TotalS     float64 `json:"totalSec"`   // known duration, 0 when unknown
	Frame      int64   `json:"frame"`
	TotalFrame int64   `json:"totalFrames"`
	FPS        float64 `json:"fps"`
	Speed      float64 `json:"speed"` // playback multiples, e.g. 12.4x
	BitrateKb  float64 `json:"bitrateKb"`
	SizeBytes  int64   `json:"sizeBytes"`
	ETASeconds float64 `json:"etaSeconds"` // -1 when not derivable
	Done       bool    `json:"done"`
	Pass       int     `json:"pass"`   // 1-based pass of a multi-pass job, 0 otherwise
	Passes     int     `json:"passes"` // how many passes the job has

	// Downloads only, from yt-dlp, where SizeBytes is what's come down so far
	Stage      string  `json:"stage"`       // what yt-dlp is doing, "" for ffmpeg
	TotalBytes int64   `json:"totalBytes"`  // the part's size, 0 when unknown
	RateBps    float64 `json:"bytesPerSec"` // download speed
	Item       int     `json:"item"`        // 1-based entry of a playlist, 0 otherwise
	Items      int     `json:"items"`       // entries in the playlist
}

// ParseProgress reads ffmpeg's `-progress` key=value stream and invokes emit
// for every completed progress block, returning when the stream closes
// yt-dlp's templated lines from DownloadProgressArgs are read too, each one
// emitted as it comes
func ParseProgress(r io.Reader, total Totals, emit func(Progress)) error {
	if r == nil || emit == nil {
		return nil
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), maxLineBytes)

	cur := Progress{Fraction: -1, ETASeconds: -1, TotalS: total.DurationS, TotalFrame: total.Frames}
	var dl downloadState
	lines := 0

	for scanner.Scan() {
		lines++
		if lines > maxProgressLines {
			break
		}
		if applyDownloadLine(&cur, &dl, scanner.Text()) {
			emit(cur)
			continue
		}
		key, value, ok := splitKV(scanner.Text())
		if !ok {
			continue
		}
		applyField(&cur, key, value)

		// ffmpeg terminates each block with a `progress=` line.
		if key != "progress" {
			continue
		}
		cur.Done = value == "end"
		finalize(&cur, total)
		emit(cur)
		if cur.Done {
			break
		}
	}
	return scanner.Err()
}

// Totals carries the known extent of the work, used to derive a percentage.
type Totals struct {
	DurationS float64
	Frames    int64
}

// splitKV parses one `key=value` progress line.
func splitKV(line string) (string, string, bool) {
	idx := strings.IndexByte(line, '=')
	if idx <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:idx])
	value := strings.TrimSpace(line[idx+1:])
	if key == "" || value == "" || value == "N/A" {
		return "", "", false
	}
	return key, value, true
}

// applyField folds a single progress key into the accumulating sample.
func applyField(p *Progress, key, value string) {
	switch key {
	case "out_time_us", "out_time_ms":
		// Both keys are microseconds in practice; ffmpeg mislabels out_time_ms.
		if v, err := strconv.ParseFloat(value, 64); err == nil && v >= 0 {
			p.OutTimeS = v / 1_000_000
		}
	case "out_time":
		if v := parseTimecode(value); v >= 0 {
			p.OutTimeS = v
		}
	case "frame":
		if v, err := strconv.ParseInt(value, 10, 64); err == nil && v >= 0 {
			p.Frame = v
		}
	case "fps":
		if v, err := strconv.ParseFloat(value, 64); err == nil && v >= 0 {
			p.FPS = v
		}
	case "total_size":
		if v, err := strconv.ParseInt(value, 10, 64); err == nil && v >= 0 {
			p.SizeBytes = v
		}
	case "bitrate":
		p.BitrateKb = parseBitrate(value)
	case "speed":
		p.Speed = parseSpeed(value)
	}
}

// finalize derives the completion fraction and ETA from the sampled counters.
func finalize(p *Progress, total Totals) {
	if p.Done {
		p.Fraction = 1
		p.ETASeconds = 0
		return
	}

	switch {
	case total.DurationS > 0 && p.OutTimeS > 0:
		p.Fraction = p.OutTimeS / total.DurationS
	case total.Frames > 0 && p.Frame > 0:
		p.Fraction = float64(p.Frame) / float64(total.Frames)
	default:
		p.Fraction = -1
	}

	if p.Fraction > 1 {
		p.Fraction = 1
	}
	if p.Fraction < 0 {
		p.ETASeconds = -1
		return
	}
	p.ETASeconds = estimateETA(*p, total)
}

// estimateETA converts remaining work into wall-clock seconds.
func estimateETA(p Progress, total Totals) float64 {
	if total.DurationS > 0 && p.Speed > 0 {
		remaining := total.DurationS - p.OutTimeS
		if remaining <= 0 {
			return 0
		}
		return remaining / p.Speed
	}
	if total.Frames > 0 && p.FPS > 0 {
		remaining := float64(total.Frames - p.Frame)
		if remaining <= 0 {
			return 0
		}
		return remaining / p.FPS
	}
	return -1
}

// parseTimecode converts `HH:MM:SS.ffff` into seconds, or -1 when malformed.
func parseTimecode(v string) float64 {
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return -1
	}
	h, err1 := strconv.ParseFloat(parts[0], 64)
	m, err2 := strconv.ParseFloat(parts[1], 64)
	s, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return -1
	}
	return h*3600 + m*60 + s
}

// parseSpeed reads ffmpeg's `12.4x` speed field.
func parseSpeed(v string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "x"), 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// parseBitrate reads ffmpeg's `1234.5kbits/s` bitrate field.
func parseBitrate(v string) float64 {
	trimmed := strings.TrimSuffix(strings.TrimSpace(v), "bits/s")
	trimmed = strings.TrimSuffix(trimmed, "k")
	f, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// FormatDuration renders seconds as a compact H:MM:SS / M:SS string.
func FormatDuration(seconds float64) string {
	if seconds < 0 {
		return "--:--"
	}
	d := time.Duration(seconds * float64(time.Second))
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return strconv.Itoa(h) + ":" + pad(m) + ":" + pad(s)
	}
	return strconv.Itoa(m) + ":" + pad(s)
}

func pad(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}
