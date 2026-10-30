// Package tools declares every media operation the application exposes.
//
// Each tool is data: its inputs, its user-settable options and the function
// that turns those into an ffmpeg argument vector. The UI renders options from
// this declaration, so adding a tool never requires touching the UI layer.
package tools

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// Bounds on the registry (Power of Ten rule 2).
const (
	MaxOptions = 14
	MaxTools   = 128
)

// Category groups tools in the interface.
type Category string

const (
	CatAudio    Category = "audio"
	CatImage    Category = "image"
	CatVideo    Category = "video"
	CatDownload Category = "download"
	CatSubtitle Category = "subtitles"
	CatInspect  Category = "inspect"
)

// InputKind describes what the user must supply before a tool can run.
type InputKind string

const (
	InputFile   InputKind = "file"
	InputFiles  InputKind = "files"
	InputFolder InputKind = "folder"
	InputURL    InputKind = "url"
)

// OptionKind is the widget the UI renders for an option.
type OptionKind string

const (
	OptSelect OptionKind = "select"
	OptNumber OptionKind = "number"
	OptText   OptionKind = "text"
	OptBool   OptionKind = "bool"
	// OptFile is a path to one file the user picks, of the types in Accept.
	// It isn't remembered between runs, since it belongs to one job.
	OptFile OptionKind = "file"
)

// maxPathChars bounds a file option's value.
const maxPathChars = 1024

// Choice is one entry in a select option. Label is the name shown in Title
// Case; Hint is an optional sentence-case note shown faintly beside it.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
}

// Option is a single user-settable parameter of a tool.
type Option struct {
	Key      string     `json:"key"`
	Label    string     `json:"label"`
	Kind     OptionKind `json:"kind"`
	Default  string     `json:"default"`
	Choices  []Choice   `json:"choices,omitempty"`
	Min      float64    `json:"min,omitempty"`
	Max      float64    `json:"max,omitempty"`
	Step     float64    `json:"step,omitempty"`
	Unit     string     `json:"unit,omitempty"`
	Help     string     `json:"help,omitempty"`
	Advanced bool       `json:"advanced,omitempty"`
	// Accept lists the extensions a file option takes.
	Accept []string `json:"accept,omitempty"`
	// When shows the option only while another is set a certain way, written
	// "key=value" or "key=first|second". Hidden options keep their values.
	When string `json:"when,omitempty"`
	// Requires maps a choice's value to something it can't work without,
	// which the interface explains while it is selected: RequireSpeechModel.
	Requires map[string]string `json:"requires,omitempty"`
}

// Encode carries the resolved hardware/quality settings for a run.
type Encode struct {
	Encoder     string // ffmpeg -c:v value
	HWAccel     string // ffmpeg -hwaccel value, empty for software
	RateFlag    string // crf, qp or q
	Quality     int
	Preset      string
	DeviceIndex int
	AudioKbps   int
	UseHardware bool
	SpeechModel string // path to a whisper.cpp model, empty when none is set
}

// Plan is the executable form of a tool invocation.
type Plan struct {
	Args       []string
	OutputPath string
	NeedsProbe bool // duration is required to derive a percentage
	IsDownload bool

	// Passes, when set, are commands run after Args in order, as one job
	// sharing one progress bar: the second pass of a two-pass encode.
	Passes [][]string
	// Files are written before the first command and removed after the
	// last, such as the list the concat demuxer reads.
	Files []File
	// Cleanup names files a command leaves behind that are removed after
	// the last command, such as two-pass statistics logs.
	Cleanup []string
	// Steps names each command for the job board, in order
	Steps []string
	// Staged is where the last command writes, moved to OutputPath once every
	// command has succeeded, so a cancelled job leaves nothing half made there
	Staged string
	// Frames, when set, is how many frames each command handles, for the bar,
	// so the sources aren't probed for a length they don't have
	Frames int
	// Disposal, when set, is written into every frame of the staged GIF once
	// the commands have succeeded, DisposeKeep or DisposeClear
	Disposal int

	// Finish, for a download that's converted afterwards, builds the ffmpeg
	// commands that turn src, the file the download left in its work folder,
	// into the result, and FinishCount is how many it returns
	Finish      func(src string, color SourceColor) (Plan, error)
	FinishCount int
}

// SourceColor is what a source's video says of its colours, for a tool that
// turns it into RGB, zero when it hasn't been probed
type SourceColor struct {
	Space  string // the colour standard it names, empty or "unknown" for none
	Width  int
	Height int
}

// File is a small text file a plan needs on disk while it runs.
type File struct {
	Path    string
	Content string
}

// Commands returns every argument vector of the plan, in order.
func (p Plan) Commands() [][]string {
	out := make([][]string, 0, 1+len(p.Passes))
	out = append(out, p.Args)
	return append(out, p.Passes...)
}

// BuildFunc turns validated inputs into an argument vector.
type BuildFunc func(in Inputs, enc Encode) (Plan, error)

// Inputs is the fully-resolved input set for one invocation.
type Inputs struct {
	Path    string            // source file, folder or URL
	Paths   []string          // every source, in order, for a tool that combines files
	Output  string            // resolved destination path
	Options map[string]string // option key -> raw value
	// WorkDir is an empty folder of the download's own, for one that's
	// converted after it lands, and is removed once the job ends
	WorkDir string
	// Color describes the source's video, for a tool declared with
	// NeedsColor or NeedsStreams, and is zero until it's probed or if that failed
	Color SourceColor
	// DurationS is the source's duration, probed just before the job runs,
	// for a tool declared with NeedsDuration. It is zero otherwise.
	DurationS float64
	// Sidecar is a file beside the source with the same name and one of the
	// tool's Sidecars extensions, such as clip.srt for clip.mp4, or "" when
	// there is none. The caller looks; this package never touches the disk.
	Sidecar string
	// SubtitleTracks counts the source's subtitle tracks, for a tool declared
	// with NeedsStreams, or is UnknownCount while that isn't known yet.
	SubtitleTracks int
}

// UnknownCount marks a count that hasn't been measured, such as the tracks
// in a source before it is probed.
const UnknownCount = -1

// Tool is one operation exposed in the interface.
type Tool struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Icon        string    `json:"icon"` // a name from the frontend's icon set
	Category    Category  `json:"category"`
	Description string    `json:"description"`
	Input       InputKind `json:"input"`
	Accept      []string  `json:"accept"`
	OutputExt   string    `json:"outputExt"`
	Suffix      string    `json:"suffix"`
	UsesEncode  bool      `json:"usesEncode"`
	// Combine makes one job from every selected file, in the order shown,
	// rather than one job per file.
	Combine bool `json:"combine"`
	// SortByName puts files added together in name order when every name
	// holds a number, so frame2 comes before frame10, and they can still be moved
	SortByName bool `json:"sortByName,omitempty"`
	// NeedsDuration defers Build until the source has been probed, for a tool
	// whose command depends on the length, such as a size target.
	NeedsDuration bool `json:"needsDuration"`
	// NeedsStreams, like NeedsDuration, defers Build until the source has
	// been probed, for a tool that depends on which tracks it has.
	NeedsStreams bool `json:"needsStreams,omitempty"`
	// NeedsColor defers Build the same way for a tool that wants the source's
	// colours, which a failed probe only leaves unchecked
	NeedsColor bool `json:"needsColor,omitempty"`
	// NeedsSize defers Build the same way for a tool that needs the first
	// source's picture size, and the job fails when it can't be read
	NeedsSize bool `json:"needsSize,omitempty"`
	// VideoKeepsFormat makes a video input keep its own container whatever
	// the format option says; the option applies to audio inputs only.
	VideoKeepsFormat bool `json:"videoKeepsFormat,omitempty"`
	// Requires names something the tool cannot run without, which the
	// interface explains and links to: RequireSpeechModel.
	Requires string `json:"requires,omitempty"`
	// Sidecars are the extensions of companion files, such as subtitles, to
	// look for beside the input. The first found arrives as Inputs.Sidecar.
	Sidecars []string  `json:"-"`
	Options  []Option  `json:"options"`
	Build    BuildFunc `json:"-"`
}

// ExtensionFor decides the output file extension for one input: the tool's
// fixed extension, else its format option, else the input's own. "keep" and
// "copy" mean the input's own, and so does any format for a video when the
// tool keeps videos as videos.
func (t Tool) ExtensionFor(opts map[string]string, input string) string {
	if t.OutputExt != "" {
		return t.OutputExt
	}
	own := strings.TrimPrefix(strings.ToLower(filepath.Ext(input)), ".")
	if t.VideoKeepsFormat && isVideoExt(own) {
		return own
	}
	format := strings.TrimSpace(opts["format"])
	if format == "" || format == "keep" || format == "copy" {
		return own
	}
	return format
}

// RequireSpeechModel marks a tool that needs a whisper.cpp model.
const RequireSpeechModel = "speech-model"

// ordered holds every declared tool in declaration order, and registry the
// same tools keyed by ID.
var ordered, registry = buildRegistry()

// buildRegistry assembles the full tool set from the per-category files.
func buildRegistry() ([]Tool, map[string]Tool) {
	all := make([]Tool, 0, MaxTools)
	all = append(all, audioTools()...)
	all = append(all, imageTools()...)
	all = append(all, videoTools()...)
	all = append(all, subtitleTools()...)
	all = append(all, downloadTools()...)
	if len(all) > MaxTools {
		all = all[:MaxTools]
	}

	byID := make(map[string]Tool, len(all))
	for _, t := range all {
		byID[t.ID] = t
	}
	return all, byID
}

// All returns every tool in declaration order. Each category file lists its
// tools most-used first, Convert leading, and the interface keeps that order
// rather than sorting alphabetically.
func All() []Tool {
	out := make([]Tool, len(ordered))
	copy(out, ordered)
	return out
}

// Get returns the tool with the given ID.
func Get(id string) (Tool, error) {
	t, ok := registry[id]
	if !ok {
		return Tool{}, fmt.Errorf("tools: unknown tool %q", id)
	}
	return t, nil
}

// Defaults returns the default option values for a tool.
func (t Tool) Defaults() map[string]string {
	out := make(map[string]string, len(t.Options))
	for _, o := range t.Options {
		out[o.Key] = o.Default
	}
	return out
}

// Validate checks supplied options against the tool declaration, filling in
// defaults for anything omitted and rejecting out-of-range values.
func (t Tool) Validate(opts map[string]string) (map[string]string, error) {
	if len(t.Options) > MaxOptions {
		return nil, fmt.Errorf("tools: %q declares more than %d options", t.ID, MaxOptions)
	}
	out := make(map[string]string, len(t.Options))

	for _, o := range t.Options {
		raw, present := opts[o.Key]
		if !present || strings.TrimSpace(raw) == "" {
			out[o.Key] = o.Default
			continue
		}
		value, err := validateOption(o, strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		out[o.Key] = value
	}
	return out, nil
}

// validateOption enforces one option's declared constraints.
func validateOption(o Option, raw string) (string, error) {
	switch o.Kind {
	case OptNumber:
		return validateNumber(o, raw)
	case OptSelect:
		return validateChoice(o, raw)
	case OptBool:
		if raw != "true" && raw != "false" {
			return "", fmt.Errorf("option %q must be true or false", o.Label)
		}
		return raw, nil
	case OptText:
		return validateText(o, raw)
	case OptFile:
		return validateFile(o, raw)
	}
	return "", fmt.Errorf("option %q has an unknown kind", o.Label)
}

// validateNumber checks a numeric option against its declared range.
func validateNumber(o Option, raw string) (string, error) {
	v, err := strconv.ParseFloat(raw, 64)
	// NaN fails every comparison, so it would pass the range check below
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return "", fmt.Errorf("option %q must be a number", o.Label)
	}
	if o.Max > o.Min && (v < o.Min || v > o.Max) {
		return "", fmt.Errorf("option %q must be between %g and %g", o.Label, o.Min, o.Max)
	}
	return raw, nil
}

// validateChoice checks a select option against its declared choices.
func validateChoice(o Option, raw string) (string, error) {
	for _, c := range o.Choices {
		if c.Value == raw {
			return raw, nil
		}
	}
	return "", fmt.Errorf("option %q does not accept %q", o.Label, raw)
}

// validateText rejects text that could alter the meaning of a filtergraph.
// validateFile checks a file option names a full path of an accepted type.
// Whether the file exists is for the caller to check; this package never
// touches the disk.
func validateFile(o Option, raw string) (string, error) {
	if len(raw) > maxPathChars || strings.ContainsAny(raw, "\n\r\x00") {
		return "", fmt.Errorf("option %q isn't a usable file path", o.Label)
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("option %q needs a full file path", o.Label)
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(raw)), ".")
	for _, a := range o.Accept {
		if ext == a {
			return raw, nil
		}
	}
	return "", fmt.Errorf("%s must be a %s file", o.Label, strings.ToUpper(strings.Join(o.Accept, ", ")))
}

func validateText(o Option, raw string) (string, error) {
	if len(raw) > 256 {
		return "", fmt.Errorf("option %q is too long", o.Label)
	}
	if strings.ContainsAny(raw, "\n\r\x00") {
		return "", fmt.Errorf("option %q contains an illegal character", o.Label)
	}
	return raw, nil
}

// optInt reads a validated numeric option, falling back to def.
func optInt(opts map[string]string, key string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(opts[key]))
	if err != nil {
		return def
	}
	return v
}

// optFloat reads a validated numeric option, falling back to def.
func optFloat(opts map[string]string, key string, def float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(opts[key]), 64)
	if err != nil {
		return def
	}
	return v
}

// optStr reads a validated option, falling back to def.
func optStr(opts map[string]string, key, def string) string {
	v := strings.TrimSpace(opts[key])
	if v == "" {
		return def
	}
	return v
}

// optBool reads a validated boolean option.
func optBool(opts map[string]string, key string) bool {
	return strings.TrimSpace(opts[key]) == "true"
}

// videoEncodeArgs renders the shared video encoder flags for a run.
func videoEncodeArgs(enc Encode) []string {
	args := []string{"-c:v", enc.Encoder}

	switch enc.RateFlag {
	case "crf":
		args = append(args, "-crf", strconv.Itoa(enc.Quality), "-preset", enc.Preset)
	case "qp":
		args = append(args, "-rc", "constqp", "-qp", strconv.Itoa(enc.Quality),
			"-preset", nvencPreset(enc.Preset))
	case "q":
		args = append(args, "-global_quality", strconv.Itoa(enc.Quality))
	}

	if enc.UseHardware && enc.DeviceIndex >= 0 && strings.Contains(enc.Encoder, "nvenc") {
		args = append(args, "-gpu", strconv.Itoa(enc.DeviceIndex))
	}
	return append(args, "-pix_fmt", "yuv420p")
}

// nvencPreset maps x264-style preset names onto NVENC's own scale.
func nvencPreset(preset string) string {
	switch preset {
	case "ultrafast", "superfast", "veryfast":
		return "p1"
	case "faster", "fast":
		return "p3"
	case "medium":
		return "p4"
	case "slow":
		return "p6"
	case "slower", "veryslow":
		return "p7"
	}
	return "p5"
}

// decodeArgs renders hardware-decode flags, when the backend supports them.
func decodeArgs(enc Encode) []string {
	if !enc.UseHardware || enc.HWAccel == "" {
		return nil
	}
	args := []string{"-hwaccel", enc.HWAccel}
	if enc.DeviceIndex >= 0 && enc.HWAccel == "cuda" {
		args = append(args, "-hwaccel_device", strconv.Itoa(enc.DeviceIndex))
	}
	return args
}

// audioArgs renders the shared audio encoder flags.
func audioArgs(enc Encode) []string {
	return []string{"-c:a", "aac", "-b:a", strconv.Itoa(enc.AudioKbps) + "k"}
}

// escapeFilterValue makes any text safe as one option value inside a
// filtergraph, such as a file path. Two levels of escaping apply: the
// filter's own option parser treats \ ' and : as structural, and the graph
// parser around it treats \ ' [ ] , and ; as structural. Quoting alone
// fails on a name containing an apostrophe, so both levels are escaped
// explicitly; "Ocean's Eleven.mkv" and "a [b], c;.srt" reach the filter
// exactly as written.
func escapeFilterValue(value string) string {
	optionLevel := strings.NewReplacer(`\`, `\\`, `'`, `\'`, `:`, `\:`).Replace(value)
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`, `[`, `\[`, `]`, `\]`,
		`,`, `\,`, `;`, `\;`).Replace(optionLevel)
}

// requireInput rejects an empty input path.
func requireInput(in Inputs) error {
	if strings.TrimSpace(in.Path) == "" {
		return errors.New("no input selected")
	}
	if strings.TrimSpace(in.Output) == "" {
		return errors.New("no output path resolved")
	}
	return nil
}

// qualityOption is the standard quality control shared by encoding tools.
func qualityOption() Option {
	return Option{
		Key: "quality", Label: "Quality", Kind: OptNumber,
		Default: "20", Min: 0, Max: 51, Step: 1,
		Help: "Lower is better quality and a larger file. 18 is visually lossless, 23 is a good default.",
	}
}
