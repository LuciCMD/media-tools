package ffmpeg

import (
	"strconv"
	"strings"
)

// The lines DownloadProgressArgs has yt-dlp print, one per update
const (
	downloadLinePrefix = "mediatools-dl "
	stageLinePrefix    = "mediatools-pp "
)

// DownloadProgressArgs makes yt-dlp print its progress as plain fields the
// runner can read, since its own [download] lines are written for people
// A missing value prints as NA
func DownloadProgressArgs() []string {
	return []string{
		"--progress-template", "download:" + downloadLinePrefix +
			"%(progress.status)s %(progress.downloaded_bytes)s %(progress.total_bytes)s " +
			"%(progress.total_bytes_estimate)s %(progress.speed)s %(progress.eta)s " +
			"%(info.playlist_autonumber)s %(info.n_entries)s %(info.vcodec)s %(info.acodec)s",
		"--progress-template", "postprocess:" + stageLinePrefix + "%(progress.postprocessor)s %(progress.status)s",
	}
}

// stageNames turns yt-dlp's postprocessor names into what the person sees
var stageNames = map[string]string{
	"Merger":         "Merging video and audio",
	"VideoConvertor": "Converting",
	"VideoRemuxer":   "Changing container",
	"ExtractAudio":   "Converting audio",
	"EmbedThumbnail": "Adding the thumbnail",
	"EmbedSubtitle":  "Adding subtitles",
	"Metadata":       "Adding details",
	"ModifyChapters": "Cutting sponsor segments",
	"SponsorBlock":   "Finding sponsor segments",
	"SplitChapters":  "Splitting chapters",
	// Only ever runs just before the download, for a Reddit preview's file type
	"MetadataParser": "Downloading",
}

// downloadState is what one yt-dlp run has finished so far, since a site
// serving picture and sound separately has them come down one after the
// other, and the bar should keep growing rather than start again
type downloadState struct {
	entry     int64 // the playlist position the parts belong to
	doneBytes int64 // bytes of the entry's parts already finished
}

// applyDownloadLine folds one templated yt-dlp line into p, reporting whether
// the line was one
func applyDownloadLine(p *Progress, st *downloadState, line string) bool {
	switch {
	case strings.HasPrefix(line, downloadLinePrefix):
		applyDownloadFields(p, st, strings.Fields(strings.TrimPrefix(line, downloadLinePrefix)))
		return true
	case strings.HasPrefix(line, stageLinePrefix):
		fields := strings.Fields(strings.TrimPrefix(line, stageLinePrefix))
		// Only a stage starting changes what's shown, its end is the next one starting
		if len(fields) == 2 && fields[1] == "started" {
			p.Stage = stageName(fields[0])
			p.ETASeconds = -1
			p.RateBps = 0
			// In a playlist the entries done still count, alone it's unknown
			if p.Items <= 1 || p.Item < 1 {
				p.Fraction = -1
			}
		}
		return true
	}
	return false
}

// stageName names a postprocessor, calling any it doesn't know Finishing
func stageName(pp string) string {
	if name, ok := stageNames[pp]; ok {
		return name
	}
	return "Finishing"
}

// applyDownloadFields reads status, downloaded, total, estimate, speed, eta,
// playlist position, playlist size, vcodec and acodec, in that order
// The codecs come from the site and go last, so a space in one can only
// spoil the part's label, never the numbers
// The position counts the entries chosen, where playlist_index would count
// the whole playlist and give "item 10 of 6" for items 5 to 10
func applyDownloadFields(p *Progress, st *downloadState, f []string) {
	if len(f) < 10 {
		return
	}
	done := f[0] == "finished"
	got := naInt(f[1])
	total := naInt(f[2])
	if total <= 0 {
		total = naInt(f[3])
	}
	entry := naInt(f[6])
	if entry != st.entry {
		st.entry, st.doneBytes = entry, 0
	}
	// Earlier parts of this entry count towards both sides, so the audio
	// after the video carries on from where the video ended
	p.SizeBytes = st.doneBytes + max(got, 0)
	p.TotalBytes = 0
	if total > 0 {
		p.TotalBytes = st.doneBytes + total
	}
	p.RateBps = max(naFloat(f[4]), 0)
	p.Items = int(max(naInt(f[7]), 0))
	p.Item = int(max(entry, 0))
	if p.Items > 0 {
		p.Item = min(p.Item, p.Items)
	}
	p.Stage = downloadStage(f[8], f[len(f)-1])

	part := -1.0
	switch {
	case done:
		part = 1
		st.doneBytes += max(got, total, 0)
	case total > 0 && got >= 0:
		part = min(float64(p.SizeBytes)/float64(p.TotalBytes), 1)
	}
	p.Fraction = part
	p.ETASeconds = naFloat(f[5])
	if done {
		p.ETASeconds = 0
		p.RateBps = 0
	}
	// Across a playlist the part's time left says nothing about the rest
	if p.Items > 1 && p.Item >= 1 {
		p.ETASeconds = -1
		if part >= 0 {
			p.Fraction = min((float64(p.Item-1)+part)/float64(p.Items), 1)
		}
	}
}

// downloadStage says which part is coming down, since a site that serves
// picture and sound separately has them fetched one after the other
func downloadStage(vcodec, acodec string) string {
	switch {
	case vcodec == "none":
		return "Downloading audio"
	case acodec == "none" && vcodec != "NA":
		return "Downloading video"
	}
	return "Downloading"
}

// naInt reads a whole number yt-dlp printed, -1 for NA or anything else
func naInt(v string) int64 {
	if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 && n < 1e18 {
		return int64(n)
	}
	return -1
}

// naFloat reads a number yt-dlp printed, -1 for NA or anything else
func naFloat(v string) float64 {
	if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 && n < 1e18 {
		return n
	}
	return -1
}
