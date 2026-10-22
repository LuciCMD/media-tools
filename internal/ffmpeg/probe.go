package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Bounds for probing (Power of Ten rule 2: every loop is statically bounded).
const (
	probeTimeout  = 20 * time.Second
	maxStreams    = 64
	maxDurationS  = 60 * 60 * 24 * 7 // one week of media is the accepted ceiling
	maxProbeBytes = 8 << 20
)

// MediaInfo is the subset of ffprobe output the application acts on.
type MediaInfo struct {
	Path       string   `json:"path"`
	Format     string   `json:"format"`
	DurationS  float64  `json:"durationSeconds"`
	SizeBytes  int64    `json:"sizeBytes"`
	BitRate    int64    `json:"bitRate"`
	TotalFrame int64    `json:"totalFrames"`
	Streams    []Stream `json:"streams"`
}

// Stream describes one elementary stream inside a container.
type Stream struct {
	Index      int    `json:"index"`
	Type       string `json:"type"`
	Codec      string `json:"codec"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	FrameRate  string `json:"frameRate,omitempty"`
	SampleRate string `json:"sampleRate,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Language   string `json:"language,omitempty"`
	BitRate    int64  `json:"bitRate,omitempty"`
	// ColorSpace is the colour standard a video names, empty or "unknown" when it names none
	ColorSpace string `json:"colorSpace,omitempty"`
}

// rawProbe mirrors the ffprobe JSON schema for decoding only.
type rawProbe struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		Index         int    `json:"index"`
		CodecType     string `json:"codec_type"`
		CodecName     string `json:"codec_name"`
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		ColorSpace    string `json:"color_space"`
		AvgFrameRate  string `json:"avg_frame_rate"`
		RFrameRate    string `json:"r_frame_rate"`
		SampleRate    string `json:"sample_rate"`
		Channels      int    `json:"channels"`
		BitRate       string `json:"bit_rate"`
		NbFrames      string `json:"nb_frames"`
		DurationTicks string `json:"duration"`
		Tags          struct {
			Language string `json:"language"`
		} `json:"tags"`
	} `json:"streams"`
}

// Probe inspects path with ffprobe and returns its structural metadata.
func Probe(ctx context.Context, path string) (MediaInfo, error) {
	if strings.TrimSpace(path) == "" {
		return MediaInfo{}, errors.New("probe: empty path")
	}
	bins := Locate()
	if bins.FFprobe == "" {
		return MediaInfo{}, ErrNotFound
	}

	runCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bins.FFprobe,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	hideWindow(cmd)

	out, err := cmd.Output()
	if err != nil {
		return MediaInfo{}, fmt.Errorf("probe %q: %w", path, err)
	}
	if len(out) > maxProbeBytes {
		return MediaInfo{}, errors.New("probe: ffprobe output exceeded size limit")
	}
	return decodeProbe(path, out)
}

// decodeProbe converts raw ffprobe JSON into a validated MediaInfo.
func decodeProbe(path string, out []byte) (MediaInfo, error) {
	var raw rawProbe
	if err := json.Unmarshal(out, &raw); err != nil {
		return MediaInfo{}, fmt.Errorf("probe: malformed ffprobe json: %w", err)
	}

	info := MediaInfo{
		Path:      path,
		Format:    raw.Format.FormatName,
		DurationS: parseFloat(raw.Format.Duration),
		SizeBytes: parseInt(raw.Format.Size),
		BitRate:   parseInt(raw.Format.BitRate),
	}
	if info.DurationS < 0 || info.DurationS > maxDurationS {
		info.DurationS = 0
	}

	limit := len(raw.Streams)
	if limit > maxStreams {
		limit = maxStreams
	}
	info.Streams = make([]Stream, 0, limit)
	for i := 0; i < limit; i++ {
		s := raw.Streams[i]
		info.Streams = append(info.Streams, Stream{
			Index:      s.Index,
			Type:       s.CodecType,
			Codec:      s.CodecName,
			Width:      s.Width,
			Height:     s.Height,
			ColorSpace: s.ColorSpace,
			FrameRate:  normalizeRate(s.AvgFrameRate, s.RFrameRate),
			SampleRate: s.SampleRate,
			Channels:   s.Channels,
			Language:   s.Tags.Language,
			BitRate:    parseInt(s.BitRate),
		})
		if s.CodecType == "video" && info.TotalFrame == 0 {
			info.TotalFrame = estimateFrames(s.NbFrames, s.AvgFrameRate, info.DurationS)
		}
	}
	return info, nil
}

// estimateFrames prefers the declared frame count and falls back to fps*duration.
func estimateFrames(nbFrames, rate string, durationS float64) int64 {
	if n := parseInt(nbFrames); n > 0 {
		return n
	}
	fps := ParseRate(rate)
	if fps <= 0 || durationS <= 0 {
		return 0
	}
	return int64(fps * durationS)
}

// ParseRate converts an ffprobe "num/den" rational into frames per second.
func ParseRate(rate string) float64 {
	parts := strings.SplitN(strings.TrimSpace(rate), "/", 2)
	if len(parts) != 2 {
		return parseFloat(rate)
	}
	num := parseFloat(parts[0])
	den := parseFloat(parts[1])
	if den == 0 {
		return 0
	}
	return num / den
}

// normalizeRate picks the first usable of the two reported frame rates.
func normalizeRate(avg, r string) string {
	if ParseRate(avg) > 0 {
		return avg
	}
	return r
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func parseInt(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
