package tools

import (
	"slices"
	"strings"
	"testing"
)

// argsContain reports whether flag appears with the given value.
func argsContain(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// argValue returns the value following flag, or "" when absent.
func argValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// softwareEncode is a CPU encoding profile for tests.
func softwareEncode() Encode {
	return Encode{Encoder: "libx264", RateFlag: "crf", Quality: 20,
		Preset: "slow", AudioKbps: 192, DeviceIndex: -1, SpeechModel: `C:\models\ggml-base.bin`}
}

// defaultInputs gives a tool what its declaration says it needs: a link for
// a download, several clips for a combining tool, and a probed length for a
// tool that needs one.
func defaultInputs(tool Tool, opts map[string]string) Inputs {
	if tool.Input == InputURL {
		return Inputs{Path: "https://example.com/watch?v=abc", Output: `C:\downloads`,
			WorkDir: `C:\downloads\.mediatools-test`, Options: opts}
	}
	in := Inputs{Path: `C:\media\clip.mp4`, Output: `C:\media\out.mp4`, Options: opts}
	if tool.Combine {
		in.Paths = []string{`C:\media\a.mp4`, `C:\media\b.mp4`}
	}
	if tool.NeedsDuration {
		in.DurationS = 120
	}
	return in
}

// hardwareEncode is an NVENC encoding profile for tests.
func hardwareEncode() Encode {
	return Encode{Encoder: "h264_nvenc", HWAccel: "cuda", RateFlag: "qp", Quality: 22,
		Preset: "slow", AudioKbps: 256, DeviceIndex: 1, UseHardware: true}
}

// TestRegistryIsWellFormed checks every declared tool for structural mistakes.
func TestRegistryIsWellFormed(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("registry is empty")
	}

	seen := make(map[string]bool, len(all))
	for _, tool := range all {
		if seen[tool.ID] {
			t.Errorf("duplicate tool ID %q", tool.ID)
		}
		seen[tool.ID] = true

		if tool.Name == "" || tool.Description == "" || tool.Icon == "" {
			t.Errorf("tool %q is missing a name, description or icon", tool.ID)
		}
		if tool.Build == nil {
			t.Errorf("tool %q has no build function", tool.ID)
		}
		if len(tool.Options) > MaxOptions {
			t.Errorf("tool %q declares %d options, over the %d limit",
				tool.ID, len(tool.Options), MaxOptions)
		}
		checkOptions(t, tool)
	}
}

// checkOptions verifies each option's default satisfies its own constraints.
func checkOptions(t *testing.T, tool Tool) {
	t.Helper()
	keys := make(map[string]bool, len(tool.Options))

	for _, opt := range tool.Options {
		if keys[opt.Key] {
			t.Errorf("tool %q has duplicate option key %q", tool.ID, opt.Key)
		}
		keys[opt.Key] = true

		if opt.Kind == OptSelect && len(opt.Choices) == 0 {
			t.Errorf("tool %q option %q is a select with no choices", tool.ID, opt.Key)
		}
		// A blank default is legitimate for free text such as an end
		// timestamp, and for a file, which the user always picks.
		if opt.Default == "" && opt.Kind != OptText && opt.Kind != OptFile {
			t.Errorf("tool %q option %q has no default", tool.ID, opt.Key)
		}
		if opt.Default != "" {
			if _, err := validateOption(opt, opt.Default); err != nil {
				t.Errorf("tool %q option %q default is invalid: %v", tool.ID, opt.Key, err)
			}
		}
		if opt.Kind == OptFile && len(opt.Accept) == 0 {
			t.Errorf("tool %q file option %q accepts no file types", tool.ID, opt.Key)
		}
		checkWhen(t, tool, opt)
		for value := range opt.Requires {
			if !hasChoice(opt, value) {
				t.Errorf("tool %q option %q requires something for %q, which isn't a choice", tool.ID, opt.Key, value)
			}
		}
	}
}

// checkWhen verifies a When condition names a select of the same tool and
// only values it offers, so an option can't be hidden for good by a typo.
func checkWhen(t *testing.T, tool Tool, opt Option) {
	t.Helper()
	if opt.When == "" {
		return
	}
	key, values, found := strings.Cut(opt.When, "=")
	var target *Option
	for i := range tool.Options {
		if tool.Options[i].Key == key && tool.Options[i].Kind == OptSelect {
			target = &tool.Options[i]
		}
	}
	if !found || target == nil {
		t.Errorf("tool %q option %q: When %q doesn't name a select option", tool.ID, opt.Key, opt.When)
		return
	}
	for _, v := range strings.Split(values, "|") {
		if !hasChoice(*target, v) {
			t.Errorf("tool %q option %q: When names %q, which %q doesn't offer", tool.ID, opt.Key, v, key)
		}
	}
}

// hasChoice reports whether a select offers value.
func hasChoice(opt Option, value string) bool {
	for _, c := range opt.Choices {
		if c.Value == value {
			return true
		}
	}
	return false
}

// TestEveryToolBuildsWithDefaults runs each tool's builder with its declared
// defaults, catching builders that cannot produce a command at all.
func TestEveryToolBuildsWithDefaults(t *testing.T) {
	for _, tool := range All() {
		opts, err := tool.Validate(nil)
		if err != nil {
			t.Errorf("tool %q rejected its own defaults: %v", tool.ID, err)
			continue
		}

		plan, err := tool.Build(defaultInputs(tool, opts), softwareEncode())
		if err != nil {
			t.Errorf("tool %q failed to build with defaults: %v", tool.ID, err)
			continue
		}
		if len(plan.Args) == 0 {
			t.Errorf("tool %q produced an empty argument vector", tool.ID)
		}
	}
}

// TestSoftenMatchesBatchPresets verifies the ported filtergraph keeps the
// coefficients from the original audio_soften.bat script.
func TestSoftenMatchesBatchPresets(t *testing.T) {
	tool, err := Get("audio.soften")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	cases := []struct{ preset, makeup, marker string }{
		{"gentle", "volume=1.12", "threshold=0.10:ratio=2.5"},
		{"balanced", "volume=1.26", "threshold=0.06:ratio=4"},
		{"strong", "volume=1.41", "threshold=0.045:ratio=6"},
	}

	for _, c := range cases {
		opts, err := tool.Validate(map[string]string{"preset": c.preset})
		if err != nil {
			t.Fatalf("Validate(%s): %v", c.preset, err)
		}
		plan, err := tool.Build(Inputs{Path: "in.wav", Output: "out.ogg", Options: opts}, softwareEncode())
		if err != nil {
			t.Fatalf("Build(%s): %v", c.preset, err)
		}

		graph := argValue(plan.Args, "-filter_complex")
		if !strings.Contains(graph, c.makeup) {
			t.Errorf("preset %s: filtergraph missing %q", c.preset, c.makeup)
		}
		if !strings.Contains(graph, c.marker) {
			t.Errorf("preset %s: filtergraph missing %q", c.preset, c.marker)
		}
		if !strings.Contains(graph, "acrossover=split=2000 6000:order=8th") {
			t.Errorf("preset %s: filtergraph missing the crossover stage", c.preset)
		}
		if !argsContain(plan.Args, "-map", "[out]") {
			t.Errorf("preset %s: output label is not mapped", c.preset)
		}
	}
}

// TestSoftenRejectsOutOfRangeCeiling checks the limiter bound is enforced.
func TestSoftenRejectsOutOfRangeCeiling(t *testing.T) {
	tool, _ := Get("audio.soften")
	if _, err := tool.Validate(map[string]string{"limit": "3.0"}); err == nil {
		t.Error("expected an error for a ceiling above 1.0")
	}
}

// TestHardwareEncodeArgs verifies the NVENC path emits constant-QP flags and
// targets the selected device.
func TestHardwareEncodeArgs(t *testing.T) {
	args := videoEncodeArgs(hardwareEncode())

	if !argsContain(args, "-c:v", "h264_nvenc") {
		t.Error("expected the nvenc encoder to be selected")
	}
	if !argsContain(args, "-rc", "constqp") || !argsContain(args, "-qp", "22") {
		t.Error("expected constant-QP rate control for nvenc")
	}
	if !argsContain(args, "-gpu", "1") {
		t.Error("expected the selected GPU device index to be passed")
	}
	if !argsContain(args, "-preset", "p6") {
		t.Errorf("expected the slow preset to map to p6, got %v", args)
	}
}

// TestSoftwareEncodeArgs verifies the x264 path emits CRF rather than QP.
func TestSoftwareEncodeArgs(t *testing.T) {
	args := videoEncodeArgs(softwareEncode())

	if !argsContain(args, "-crf", "20") {
		t.Error("expected CRF rate control for libx264")
	}
	if argValue(args, "-gpu") != "" {
		t.Error("software encoding must not pass a GPU device index")
	}
	if !argsContain(args, "-preset", "slow") {
		t.Error("expected the x264 preset to pass through unchanged")
	}
}

// TestDecodeArgsOnlyWhenHardware checks that hwaccel flags are software-safe.
func TestDecodeArgsOnlyWhenHardware(t *testing.T) {
	if got := decodeArgs(softwareEncode()); len(got) != 0 {
		t.Errorf("software profile produced decode args %v", got)
	}
	hw := decodeArgs(hardwareEncode())
	if !argsContain(hw, "-hwaccel", "cuda") {
		t.Error("expected cuda hwaccel for the nvenc profile")
	}
	if !argsContain(hw, "-hwaccel_device", "1") {
		t.Error("expected the hwaccel device index to be passed")
	}
}

// TestValidateURLRejectsNonHTTP is the guard against schemes such as file: and
// javascript: reaching yt-dlp.
func TestValidateURLRejectsNonHTTP(t *testing.T) {
	bad := []string{
		"", "   ",
		"file:///C:/Windows/System32/config/SAM",
		"javascript:alert(1)",
		"data:text/html,<script>",
		"ftp://example.com/x",
		"-oevil.txt",
		"https://",
	}
	for _, raw := range bad {
		if err := ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) accepted an unsafe value", raw)
		}
	}

	good := []string{
		"https://example.com/watch?v=abc",
		"http://localhost:8080/media",
	}
	for _, raw := range good {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("ValidateURL(%q) rejected a valid URL: %v", raw, err)
		}
	}
}

// TestPlaylistRangeRejectsInjection checks that the range field cannot smuggle
// an extra yt-dlp argument.
func TestPlaylistRangeRejectsInjection(t *testing.T) {
	bad := []string{"1 --exec rm", "1;whoami", "--version", "1|2"}
	for _, raw := range bad {
		if err := validatePlaylistRange(raw); err == nil {
			t.Errorf("validatePlaylistRange(%q) accepted an unsafe value", raw)
		}
	}
	for _, raw := range []string{"1-10", "1,3,7", "2:8"} {
		if err := validatePlaylistRange(raw); err != nil {
			t.Errorf("validatePlaylistRange(%q) rejected a valid range: %v", raw, err)
		}
	}
}

// TestTrimRejectsBadTimecode covers the trim tool's timestamp validation.
func TestTrimRejectsBadTimecode(t *testing.T) {
	tool, _ := Get("video.trim")
	opts, err := tool.Validate(map[string]string{"start": "not-a-time"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, err := tool.Build(Inputs{Path: "a.mp4", Output: "b.mp4", Options: opts}, softwareEncode()); err == nil {
		t.Error("expected an error for a malformed start timestamp")
	}
}

// TestTrimCopyModeAvoidsReencode verifies the fast path really copies streams.
func TestTrimCopyModeAvoidsReencode(t *testing.T) {
	tool, _ := Get("video.trim")
	opts, _ := tool.Validate(map[string]string{
		"start": "00:00:10", "end": "00:00:20", "mode": "copy",
	})
	plan, err := tool.Build(Inputs{Path: "a.mp4", Output: "b.mp4", Options: opts}, softwareEncode())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !argsContain(plan.Args, "-c", "copy") {
		t.Error("copy mode should pass -c copy")
	}
	if !argsContain(plan.Args, "-ss", "00:00:10") || !argsContain(plan.Args, "-to", "00:00:20") {
		t.Error("copy mode should pass both timestamps")
	}
}

// TestChainTempoStaysInRange checks the atempo chain covers the whole slider,
// since a single atempo filter only spans 0.5x to 2.0x.
func TestChainTempoStaysInRange(t *testing.T) {
	for _, factor := range []float64{0.5, 0.75, 1.0, 1.5, 2.0, 3.0, 4.0} {
		chain := chainTempo(factor)
		if chain == "" {
			t.Fatalf("chainTempo(%v) produced nothing", factor)
		}
		if factor > 2.0 && !strings.Contains(chain, "atempo=2.0") {
			t.Errorf("chainTempo(%v) = %q, expected a doubling stage", factor, chain)
		}
	}
}

// TestWatermarkTextCannotAddOptions checks the watermark text cannot break
// out of the drawtext filter and add further options.
func TestWatermarkTextCannotAddOptions(t *testing.T) {
	tool, _ := Get("image.watermark")
	opts, _ := tool.Validate(map[string]string{"text": "evil:x=0:fontcolor=red"})
	plan, err := tool.Build(Inputs{Path: "a.png", Output: "b.png", Options: opts}, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	filter := plan.Args[3]
	start := strings.Index(filter, ":text=") + len(":text=")
	end := strings.Index(filter, ":expansion=none")
	if start < len(":text=") || end < start {
		t.Fatalf("no text option in %s", filter)
	}
	value := filter[start:end]
	for i := 0; i < len(value); i++ {
		if value[i] == ':' && (i == 0 || value[i-1] != '\\') {
			t.Errorf("an unescaped colon lets the text add options: %s", value)
			break
		}
	}
}

// TestValidateRejectsUnknownChoice checks select options are constrained.
func TestValidateRejectsUnknownChoice(t *testing.T) {
	tool, _ := Get("audio.convert")
	if _, err := tool.Validate(map[string]string{"format": "exe"}); err == nil {
		t.Error("expected an error for a value outside the declared choices")
	}
}

// TestValidateFillsDefaults checks omitted options fall back to their defaults.
func TestValidateFillsDefaults(t *testing.T) {
	tool, _ := Get("audio.convert")
	opts, err := tool.Validate(map[string]string{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if opts["format"] != "mp3" {
		t.Errorf("format default = %q, want mp3", opts["format"])
	}
}

// TestValidateRejectsOutOfRangeNumber checks numeric bounds are enforced.
func TestValidateRejectsOutOfRangeNumber(t *testing.T) {
	tool, _ := Get("video.convert")
	if _, err := tool.Validate(map[string]string{"quality": "99"}); err == nil {
		t.Error("expected an error for a quality above the declared maximum")
	}
}

// TestImageQualityScaleInverts checks the 1-100 slider is mapped onto the
// inverted 2-31 scale ffmpeg uses for MJPEG.
func TestImageQualityScaleInverts(t *testing.T) {
	best := imageCodecArgs("jpg", 100)
	worst := imageCodecArgs("jpg", 1)

	if argValue(best, "-q:v") != "2" {
		t.Errorf("quality 100 mapped to %q, want 2", argValue(best, "-q:v"))
	}
	if argValue(worst, "-q:v") != "31" {
		t.Errorf("quality 1 mapped to %q, want 31", argValue(worst, "-q:v"))
	}
}

// TestGIFBuildsAPaletteThenTheGIF checks Convert to GIF makes its palette
// first and draws with it second, removing it after, where one command with
// split held every frame in memory until palettegen saw the last
func TestGIFBuildsAPaletteThenTheGIF(t *testing.T) {
	tool, _ := Get("video.gif")
	opts, _ := tool.Validate(nil)
	plan, err := tool.Build(Inputs{Path: "a.mp4", Output: "a.gif", Options: opts}, softwareEncode())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(plan.Passes) != 1 || len(plan.Steps) != 2 {
		t.Fatalf("want two named commands, got %d passes after the first and steps %q", len(plan.Passes), plan.Steps)
	}
	gen := argValue(plan.Args, "-vf")
	if !strings.Contains(gen, "palettegen=max_colors=128:stats_mode=diff") || strings.Contains(gen, "split") ||
		plan.Args[len(plan.Args)-1] != "a.gif.mediatools-palette.png" {
		t.Errorf("first command %q", plan.Args)
	}
	use := plan.Passes[0]
	if !argsContain(use, "-i", "a.gif.mediatools-palette.png") || !strings.Contains(argValue(use, "-lavfi"), "paletteuse=dither=sierra2_4a") ||
		use[len(use)-1] != "a.gif" {
		t.Errorf("second command %q", use)
	}
	if !slices.Equal(plan.Cleanup, []string{"a.gif.mediatools-palette.png"}) {
		t.Errorf("cleanup %q, want the palette", plan.Cleanup)
	}
}

// minorWords stay lowercase inside a Title Case title, per the author's rule.
var minorWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true, "nor": true,
	"of": true, "for": true, "to": true, "in": true, "on": true, "at": true, "by": true,
	"per": true, "as": true, "vs": true, "via": true, "from": true, "with": true,
	"into": true, "over": true,
}

// lowercaseUnits are units that keep their own casing inside a title.
var lowercaseUnits = map[string]bool{"kbps": true, "fps": true, "px": true, "x": true}

// titleCaseProblem returns the first word of title breaking Title Case, or "".
func titleCaseProblem(title string) string {
	words := strings.Fields(title)
	for i, word := range words {
		edge := i == 0 || i == len(words)-1
		for _, part := range strings.Split(word, "-") {
			if part == "" || lowercaseUnits[part] || minorWords[part] && !edge {
				continue
			}
			// Words with a digit or a capital anywhere keep their own casing:
			// x264, kHz, WebP.
			if strings.ToLower(part) != part || strings.ContainsAny(part, "0123456789") {
				continue
			}
			if first := part[0]; first >= 'a' && first <= 'z' {
				return word
			}
		}
	}
	return ""
}

// TestTitlesAreTitleCase checks every tool name, option label and choice label
// against the author's Title Case rule, so a new tool cannot slip back into
// sentence case. Descriptions, help and choice hints are sentence case and
// are not checked.
func TestTitlesAreTitleCase(t *testing.T) {
	for _, tool := range All() {
		if w := titleCaseProblem(tool.Name); w != "" {
			t.Errorf("tool name %q: %q should be capitalised", tool.Name, w)
		}
		for _, opt := range tool.Options {
			if w := titleCaseProblem(opt.Label); w != "" {
				t.Errorf("%s option %q: %q should be capitalised", tool.ID, opt.Label, w)
			}
			for _, c := range opt.Choices {
				if w := titleCaseProblem(c.Label); w != "" {
					t.Errorf("%s choice %q: %q should be capitalised", tool.ID, c.Label, w)
				}
			}
		}
	}
}

// TestTitleCaseChecker checks the checker itself on known cases.
func TestTitleCaseChecker(t *testing.T) {
	good := []string{"Extract Audio from Video", "Fit To", "128 kbps", "16 x 16",
		"Re-Encode to AAC", "CPU - x264", "1 per Second", "Burn In Subtitles", "44.1 kHz"}
	for _, title := range good {
		if w := titleCaseProblem(title); w != "" {
			t.Errorf("%q flagged %q, want accepted", title, w)
		}
	}
	bad := []string{"Soften harsh audio", "Output format", "Fit to"}
	for _, title := range bad {
		if titleCaseProblem(title) == "" {
			t.Errorf("%q accepted, want flagged", title)
		}
	}
}
