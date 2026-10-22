package tools

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// maxURLLength bounds an accepted download target.
const maxURLLength = 2048

// downloadTools declares every yt-dlp backed operation.
func downloadTools() []Tool {
	return []Tool{
		downloadVideoTool(),
		downloadAudioTool(),
		downloadPlaylistTool(),
	}
}

// downloadVideoTool fetches a single video.
func downloadVideoTool() Tool {
	return Tool{
		ID:          "download.video",
		Icon:        "download",
		Name:        "Download Video",
		Category:    CatDownload,
		Description: "Fetch a single video at a chosen maximum resolution.",
		Input:       InputURL,
		Options: []Option{
			{Key: "quality", Label: "Maximum Quality", Kind: OptSelect, Default: "1080", Choices: []Choice{
				{"best", "Best Available", ""},
				{"2160", "2160p", "4K"},
				{"1440", "1440p", ""},
				{"1080", "1080p", ""},
				{"720", "720p", ""},
				{"480", "480p", ""},
			}},
			{Key: "container", Label: "Container", Kind: OptSelect, Default: "mp4", Choices: []Choice{
				{"mp4", "MP4", "Most compatible"},
				{"mkv", "MKV", "Keeps every track"},
				{"webm", "WebM", "Only where the site offers it"},
				{"mov", "MOV", "For Apple editors"},
				{gifContainer, "GIF", "Silent and looping"},
			}},
			{Key: "span", Label: "GIF Length", Kind: OptSelect, Default: gifClip, When: "container=" + gifContainer,
				Choices: []Choice{
					{gifClip, "Clip", "Between two times"},
					{gifOriginal, "Whole Video", "Can be very large"},
				}, Help: "A whole video as a GIF often runs to hundreds of megabytes."},
			{Key: "start", Label: "Start Time", Kind: OptText, Default: "00:00:00", When: "span=" + gifClip,
				Help: "Format HH:MM:SS, such as 00:01:30."},
			{Key: "end", Label: "End Time", Kind: OptText, Default: "00:00:10", When: "span=" + gifClip,
				Help: "Keep it to a few seconds, since a GIF gets large fast."},
			{Key: "sizing", Label: "GIF Width", Kind: OptSelect, Default: gifSet, When: "container=" + gifContainer,
				Choices: []Choice{
					{gifSet, "Set Width", ""},
					{gifOriginal, "Original", "As downloaded"},
				}, Help: "Original keeps the downloaded video's size, up to the Maximum Quality above."},
			{Key: "width", Label: "Width", Kind: OptNumber, Default: "480", Min: 32, Max: 1920, Step: 2, Unit: "px",
				When: "sizing=" + gifSet, Help: "Height follows automatically to keep the shape."},
			{Key: "pace", Label: "GIF Frame Rate", Kind: OptSelect, Default: gifSet, When: "container=" + gifContainer,
				Choices: []Choice{
					{gifSet, "Set Rate", ""},
					{gifOriginal, "Original", "Up to 50 fps"},
				}, Help: "A GIF can't play faster than 50 fps, so faster video is brought down to that."},
			{Key: "fps", Label: "Frame Rate", Kind: OptNumber, Default: "15", Min: 1, Max: 50, Step: 1, Unit: "fps",
				When: "pace=" + gifSet, Help: "Lower makes a smaller, choppier GIF."},
			{Key: "cookies", Label: "Use Browser Cookies", Kind: OptSelect, Default: "none", Choices: []Choice{
				{"none", "No Cookies", ""},
				{"firefox", "Firefox", ""},
				{"chrome", "Chrome", ""},
				{"edge", "Edge", ""},
				{"brave", "Brave", ""},
			}, Help: "Needed for content that requires you to be signed in."},
			{Key: "subs", Label: "Embed Subtitles", Kind: OptBool, Default: "false", When: "container=" + videoContainers},
			{Key: "sponsorblock", Label: "Skip Sponsor Segments", Kind: OptBool, Default: "false", Advanced: true,
				When: "container=" + videoContainers},
		},
		Build: buildDownloadVideo,
	}
}

// buildDownloadVideo renders a yt-dlp invocation for one video.
func buildDownloadVideo(in Inputs, enc Encode) (Plan, error) {
	if err := ValidateURL(in.Path); err != nil {
		return Plan{}, err
	}
	container := optStr(in.Options, "container", "mp4")
	quality := optStr(in.Options, "quality", "1080")
	template := strings.TrimSuffix(in.Output, "/") + "/%(title).150B [%(id)s].%(ext)s"

	var args []string
	if container == gifContainer {
		gif, err := gifDownloadArgs(in.Options, quality)
		if err != nil {
			return Plan{}, err
		}
		args = append(args, gif...)
	} else {
		// Merging alone only applies when video and audio come separately,
		// so the remux turns a single-file download into the chosen container too
		args = append(args,
			"-f", formatSelector(quality, container),
			"--merge-output-format", container,
			"--remux-video", container)
		// yt-dlp refuses to embed a thumbnail in WebM and fails the job
		if container != "webm" {
			args = append(args, "--embed-thumbnail")
		}
	}
	args = append(args,
		"--embed-metadata",
		"--no-playlist",
		"--restrict-filenames",
		"--newline",
		"-o", template)
	args = append(args, cookieArgs(optStr(in.Options, "cookies", "none"))...)
	// Both stay set while hidden for a GIF, which can't hold subtitles and is cut by its own times
	if container != gifContainer && optBool(in.Options, "subs") {
		args = append(args, "--embed-subs", "--sub-langs", "en.*")
	}
	if container != gifContainer && optBool(in.Options, "sponsorblock") {
		args = append(args, "--sponsorblock-remove", "sponsor,selfpromo")
	}
	link, extra := siteLink(in.Path, container)
	args = append(args, extra...)
	args = append(args, link)

	return Plan{Args: args, OutputPath: in.Output, IsDownload: true}, nil
}

// gifContainer is the Download Video choice that saves a silent GIF
const gifContainer = "gif"

// videoContainers are the Download Video choices that keep sound, in When form
const videoContainers = "mp4|mkv|webm|mov"

// The GIF's length, width and frame rate choices
const (
	gifClip     = "clip"
	gifSet      = "set"
	gifOriginal = "original"
)

// gifMaxRate is the fastest a GIF plays, its frame delays being in hundredths
// of a second, and browsers slow a 1/100 s delay to 1/10 s
const gifMaxRate = 50

// gifDownloadArgs fetches the chosen section, or the whole video, and recodes it
// with the same palette graph as Convert to GIF, dropping audio and subtitles
// the GIF can't hold
func gifDownloadArgs(opts map[string]string, quality string) ([]string, error) {
	var section string
	seconds := 0.0 // unknown, the whole video
	if optStr(opts, "span", gifClip) == gifClip {
		var err error
		section, seconds, err = clipSection(optStr(opts, "start", "00:00:00"), optStr(opts, "end", "00:00:10"))
		if err != nil {
			return nil, err
		}
	}
	stages := gifDownloadStages(opts)
	filter := gifFramePaletteGraph(stages, 128, "sierra2_4a")
	if gifOnePaletteFits(opts, seconds) {
		filter = gifPaletteGraph(stages, 128, "sierra2_4a")
	}

	// Video only where the site has it, since the sound is thrown away, and
	// H.264 first since the exact cut re-encodes in the source's own codec
	selector := fmt.Sprintf("bv[height<=%s][vcodec^=avc1]/bv[height<=%s]/bv*[height<=%s]/b[height<=%s]/b",
		quality, quality, quality, quality)
	if quality == "best" {
		selector = "bv[vcodec^=avc1]/bv/bv*/b"
	}
	args := []string{
		"-f", selector,
		"--recode-video", gifContainer,
		// The conversion writes under the final name, so a cancelled GIF
		// leaves a short one that yt-dlp would skip as already there, and a
		// new clip of the same video would come back as the old one
		"--force-overwrites",
		// yt-dlp splits this like a shell, so the double quotes keep the
		// graph's own single quotes for ffmpeg
		"--postprocessor-args", `VideoConvertor:-an -sn -vf "` + filter + `" -loop 0`,
	}
	if section != "" {
		// Exact cuts, since a cut on the nearest keyframe can start seconds early
		args = append(args, "--download-sections", section, "--force-keyframes-at-cuts")
	}
	return args, nil
}

// gifDownloadStages sets the frame rate and width before the palette, keeping
// the video's own where the person chose Original
// Whole numbers, since the validator lets 480.5 or 1e3 through
func gifDownloadStages(opts map[string]string) string {
	stage := fmt.Sprintf("fps='min(source_fps,%d)'", gifMaxRate)
	if optStr(opts, "pace", gifSet) == gifSet {
		stage = fmt.Sprintf("fps=%d", int(optFloat(opts, "fps", 15)))
	}
	if optStr(opts, "sizing", gifSet) == gifSet {
		stage += fmt.Sprintf(",scale=%d:-1:flags=lanczos", int(optFloat(opts, "width", 480)))
	}
	return stage
}

// gifOnePaletteBudget bounds frames times width squared for one palette over
// the whole GIF, about 400 MB for 16:9 video, since palettegen holds every
// frame until it has seen the last
const gifOnePaletteBudget = 160_000_000

// gifOnePaletteFits reports whether a GIF is a clip small enough for one
// palette, which is sharper and smaller than a palette per frame
// Anything at its original size or rate, or of unknown length, isn't
func gifOnePaletteFits(opts map[string]string, seconds float64) bool {
	if seconds <= 0 || optStr(opts, "sizing", gifSet) != gifSet || optStr(opts, "pace", gifSet) != gifSet {
		return false
	}
	width := optFloat(opts, "width", 480)
	frames := seconds * optFloat(opts, "fps", 15)
	return frames*width*width <= gifOnePaletteBudget
}

// clipSection renders a yt-dlp time range and its length in seconds, always
// closed since a blank end falls back to its default, and Whole Video is a
// separate choice
func clipSection(start, end string) (string, float64, error) {
	from, err := clipSeconds(start)
	if err != nil {
		return "", 0, err
	}
	to, err := clipSeconds(end)
	if err != nil {
		return "", 0, err
	}
	if to <= from {
		return "", 0, errors.New("the end time must come after the start time")
	}
	return "*" + start + "-" + end, to - from, nil
}

// clipTime is the shape yt-dlp reads the way it looks, since it takes a
// seconds field past two digits as a fraction, so 00:00:020 is 0.02 s,
// and refuses a lone dot or fractional minutes
var clipTime = regexp.MustCompile(`^[0-9]+:[0-9]{1,2}:[0-9]{1,2}(\.[0-9]+)?$`)

// clipSeconds reads HH:MM:SS[.mmm] as seconds, allowing only that shape so a
// value can't become a negative time or a chapter name for yt-dlp either
func clipSeconds(v string) (float64, error) {
	if !clipTime.MatchString(v) {
		return 0, fmt.Errorf("timestamp %q must look like HH:MM:SS, such as 00:01:30", v)
	}
	parts := strings.Split(v, ":")
	total := 0.0
	for _, p := range parts {
		n, _ := strconv.ParseFloat(p, 64)
		total = total*60 + n
	}
	return total, nil
}

// formatSelector builds a yt-dlp format expression for a height cap.
func formatSelector(quality, container string) string {
	if container == "webm" {
		return webmSelector(quality)
	}
	if container == "mov" {
		return movSelector(quality)
	}
	if quality == "best" {
		return "bv*+ba/b"
	}
	if container == "mp4" {
		return fmt.Sprintf(
			"bv*[height<=%s][ext=mp4]+ba[ext=m4a]/bv*[height<=%s]+ba/b[height<=%s]/b",
			quality, quality, quality)
	}
	return fmt.Sprintf("bv*[height<=%s]+ba/b[height<=%s]/b", quality, quality)
}

// movSelector takes H.264 and AAC, since MOV can't hold the VP9, AV1 and Opus
// that sites such as YouTube serve at most sizes
// The MP4 fallback is for sites that don't name their codecs
func movSelector(quality string) string {
	const fallback = "/bv*[ext=mp4]+ba[ext=m4a]/b[ext=mp4]"
	if quality == "best" {
		return "bv*[vcodec^=avc1]+ba[acodec^=mp4a]/b[vcodec^=avc1]" + fallback
	}
	return fmt.Sprintf(
		"bv*[height<=%s][vcodec^=avc1]+ba[acodec^=mp4a]/b[height<=%s][vcodec^=avc1]/bv*[vcodec^=avc1]+ba[acodec^=mp4a]/b[vcodec^=avc1]",
		quality, quality) + fallback
}

// webmSelector takes WebM streams only, since H.264 and AAC can't go in WebM
// without a slow re-encode, so a site without WebM fails with a clear error
func webmSelector(quality string) string {
	if quality == "best" {
		return "bv*[ext=webm]+ba[ext=webm]/b[ext=webm]"
	}
	return fmt.Sprintf(
		"bv*[height<=%s][ext=webm]+ba[ext=webm]/b[height<=%s][ext=webm]/bv*[ext=webm]+ba[ext=webm]/b[ext=webm]",
		quality, quality)
}

// redditMediaHosts serve Reddit's uploaded files, but redirect a request that
// asks for a web page, as yt-dlp's does, to Reddit's media viewer, which no
// extractor reads
var redditMediaHosts = []string{"i.redd.it", "preview.redd.it", "external-preview.redd.it"}

// siteLink returns the link yt-dlp should fetch and any arguments its site
// needs for container, swapping a Reddit media viewer link for the file it shows
func siteLink(link, container string) (string, []string) {
	file := strings.TrimSpace(link)
	u, err := url.Parse(file)
	if err != nil {
		return link, nil
	}
	if host := strings.ToLower(u.Hostname()); (host == "reddit.com" || strings.HasSuffix(host, ".reddit.com")) && u.Path == "/media" {
		inner := u.Query().Get("url")
		if iu, err := url.Parse(inner); err == nil && ValidateURL(inner) == nil &&
			slices.Contains(redditMediaHosts, strings.ToLower(iu.Hostname())) {
			file, u = inner, iu
		}
	}
	// Only format=mp4 serves a video, the rest are pictures and real GIFs yt-dlp can't use
	if !slices.Contains(redditMediaHosts, strings.ToLower(u.Hostname())) ||
		!strings.EqualFold(u.Query().Get("format"), "mp4") {
		return link, nil
	}
	// The MP4 keeps its .gif name and yt-dlp takes the type from the link, so
	// a GIF download skipped converting it
	extra := []string{"--add-header", "Accept:*/*", "--parse-metadata", "video:mp4:%(ext)s"}
	// Reddit serves one file, so any format is that file, where MOV's ext=mp4
	// found nothing under the .gif name
	// WebM can't hold its H.264, so WebM's own choice fails before downloading
	if container != "webm" {
		extra = append(extra, "-f", "b")
	}
	return file, extra
}

// cookieArgs renders the browser cookie flag, when one is requested.
func cookieArgs(browser string) []string {
	if browser == "none" || browser == "" {
		return nil
	}
	return []string{"--cookies-from-browser", browser}
}

// downloadAudioTool fetches audio only.
func downloadAudioTool() Tool {
	return Tool{
		ID:          "download.audio",
		Icon:        "note-down",
		Name:        "Download Audio",
		Category:    CatDownload,
		Description: "Fetch the audio track only, converted to your preferred format.",
		Input:       InputURL,
		Options: []Option{
			{Key: "format", Label: "Audio Format", Kind: OptSelect, Default: "mp3", Choices: []Choice{
				{"mp3", "MP3", ""},
				{"flac", "FLAC", "Lossless"},
				{"opus", "Opus", "Most efficient"},
				{"m4a", "M4A / AAC", ""},
				{"wav", "WAV", ""},
			}},
			{Key: "quality", Label: "Quality", Kind: OptSelect, Default: "0", Choices: []Choice{
				{"0", "Best", ""},
				{"5", "Medium", ""},
				{"9", "Small", ""},
			}},
			{Key: "cookies", Label: "Use Browser Cookies", Kind: OptSelect, Default: "none", Choices: []Choice{
				{"none", "No Cookies", ""},
				{"firefox", "Firefox", ""},
				{"chrome", "Chrome", ""},
				{"edge", "Edge", ""},
				{"brave", "Brave", ""},
			}},
			{Key: "split", Label: "Split by Chapters", Kind: OptBool, Default: "false", Advanced: true},
		},
		Build: buildDownloadAudio,
	}
}

// buildDownloadAudio renders a yt-dlp audio extraction.
func buildDownloadAudio(in Inputs, enc Encode) (Plan, error) {
	if err := ValidateURL(in.Path); err != nil {
		return Plan{}, err
	}
	template := strings.TrimSuffix(in.Output, "/") + "/%(title).150B [%(id)s].%(ext)s"

	args := []string{
		"-f", "ba/b",
		"--extract-audio",
		"--audio-format", optStr(in.Options, "format", "mp3"),
		"--audio-quality", optStr(in.Options, "quality", "0"),
		"--embed-metadata", "--embed-thumbnail",
		"--no-playlist",
		"--restrict-filenames",
		"--newline",
		"-o", template,
	}
	args = append(args, cookieArgs(optStr(in.Options, "cookies", "none"))...)
	if optBool(in.Options, "split") {
		args = append(args, "--split-chapters")
	}
	link, extra := siteLink(in.Path, "")
	args = append(args, extra...)
	args = append(args, link)

	return Plan{Args: args, OutputPath: in.Output, IsDownload: true}, nil
}

// downloadPlaylistTool fetches every entry in a playlist.
func downloadPlaylistTool() Tool {
	return Tool{
		ID:          "download.playlist",
		Icon:        "list-down",
		Name:        "Download Playlist",
		Category:    CatDownload,
		Description: "Fetch a whole playlist into its own folder, skipping entries that fail.",
		Input:       InputURL,
		Options: []Option{
			{Key: "kind", Label: "Download", Kind: OptSelect, Default: "video", Choices: []Choice{
				{"video", "Video", ""},
				{"audio", "Audio Only", ""},
			}},
			{Key: "quality", Label: "Maximum Quality", Kind: OptSelect, Default: "1080", Choices: []Choice{
				{"best", "Best Available", ""},
				{"1080", "1080p", ""},
				{"720", "720p", ""},
				{"480", "480p", ""},
			}},
			{Key: "range", Label: "Items", Kind: OptText, Default: "",
				Help:     "Blank for all. Otherwise a range such as 1-10 or a list such as 1,3,7.",
				Advanced: true},
			{Key: "cookies", Label: "Use Browser Cookies", Kind: OptSelect, Default: "none", Choices: []Choice{
				{"none", "No Cookies", ""},
				{"firefox", "Firefox", ""},
				{"chrome", "Chrome", ""},
				{"edge", "Edge", ""},
			}},
			{Key: "archive", Label: "Skip Already Downloaded", Kind: OptBool, Default: "true",
				Help: "Keeps a record so re-running only fetches new entries."},
		},
		Build: buildDownloadPlaylist,
	}
}

// buildDownloadPlaylist renders a yt-dlp playlist fetch.
func buildDownloadPlaylist(in Inputs, enc Encode) (Plan, error) {
	if err := ValidateURL(in.Path); err != nil {
		return Plan{}, err
	}
	root := strings.TrimSuffix(in.Output, "/")
	template := root + "/%(playlist_title).100B/%(playlist_index)03d - %(title).120B.%(ext)s"

	args := []string{
		"--yes-playlist",
		"--ignore-errors", "--no-abort-on-error",
		"--embed-metadata", "--embed-thumbnail",
		"--restrict-filenames",
		"--newline",
		"-o", template,
	}
	args = append(args, playlistKindArgs(in.Options)...)
	args = append(args, cookieArgs(optStr(in.Options, "cookies", "none"))...)

	if items := strings.TrimSpace(in.Options["range"]); items != "" {
		if err := validatePlaylistRange(items); err != nil {
			return Plan{}, err
		}
		args = append(args, "--playlist-items", items)
	}
	if optBool(in.Options, "archive") {
		args = append(args, "--download-archive", root+"/.download-archive.txt")
	}
	link, extra := siteLink(in.Path, "")
	args = append(args, extra...)
	args = append(args, link)

	return Plan{Args: args, OutputPath: in.Output, IsDownload: true}, nil
}

// playlistKindArgs selects between the video and audio-only paths.
func playlistKindArgs(opts map[string]string) []string {
	if optStr(opts, "kind", "video") == "audio" {
		return []string{"-f", "ba/b", "--extract-audio", "--audio-format", "mp3", "--audio-quality", "0"}
	}
	return []string{
		"-f", formatSelector(optStr(opts, "quality", "1080"), "mp4"),
		"--merge-output-format", "mp4",
	}
}

// validatePlaylistRange accepts only digits, commas, hyphens and colons, so the
// value cannot introduce a second yt-dlp argument.
func validatePlaylistRange(items string) error {
	if len(items) > 128 {
		return errors.New("playlist item range is too long")
	}
	for _, r := range items {
		isDigit := r >= '0' && r <= '9'
		if !isDigit && r != ',' && r != '-' && r != ':' {
			return fmt.Errorf("playlist item range may only contain digits, commas and hyphens")
		}
	}
	return nil
}

// ValidateURL accepts only http and https targets of a sane length.
//
// The parsed scheme is checked rather than pattern-matched, so schemes such as
// file:, javascript: or data: cannot reach yt-dlp.
func ValidateURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("enter a URL to download")
	}
	if len(trimmed) > maxURLLength {
		return errors.New("that URL is too long")
	}
	if strings.HasPrefix(trimmed, "-") {
		return errors.New("a URL cannot begin with a hyphen")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("that does not look like a valid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("only http and https URLs are supported")
	}
	if parsed.Host == "" {
		return errors.New("that URL is missing a host name")
	}
	return nil
}
