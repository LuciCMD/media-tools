package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"media-tools/internal/ffmpeg"
)

// fixtures are generated media files every tool can be run against.
type fixtures struct {
	dir   string
	video string // 3 s, with sound and a text subtitle track
	clip  string // a second video, for joining
	audio string // 3 s of tone and noise
	image string
}

// needFFmpeg skips a test when ffmpeg or ffprobe is missing.
func needFFmpeg(t *testing.T) ffmpeg.Binaries {
	t.Helper()
	bins := ffmpeg.Locate()
	if bins.FFmpeg == "" || bins.FFprobe == "" {
		t.Skip("ffmpeg is not installed")
	}
	return bins
}

// run executes ffmpeg and fails the test with its output on error.
func run(t *testing.T, bin string, args ...string) {
	t.Helper()
	out, err := exec.Command(bin, append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %s\n%v\n%s", strings.Join(args, " "), err, out)
	}
}

// makeFixtures generates the source files. Names carry an apostrophe, a
// comma and brackets, which filtergraphs treat as structural.
func makeFixtures(t *testing.T, bin string) fixtures {
	t.Helper()
	dir := t.TempDir()
	f := fixtures{
		dir:   dir,
		video: filepath.Join(dir, "Ocean's [cut], one.mp4"),
		clip:  filepath.Join(dir, "second clip.mp4"),
		audio: filepath.Join(dir, "voice note.wav"),
		image: filepath.Join(dir, "photo.png"),
	}
	subs := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(subs, []byte("1\n00:00:00,000 --> 00:00:02,500\nHello there\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{f.video, f.clip} {
		run(t, bin, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=3",
			"-f", "lavfi", "-i", "sine=f=440:d=3", "-i", subs,
			"-map", "0", "-map", "1", "-map", "2",
			"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-c:s", "mov_text", "-y", v)
	}
	run(t, bin, "-f", "lavfi", "-i", "sine=f=300:d=3", "-f", "lavfi", "-i", "anoisesrc=d=3:a=0.05",
		"-filter_complex", "[0][1]amix=inputs=2", "-ac", "2", "-y", f.audio)
	run(t, bin, "-f", "lavfi", "-i", "testsrc2=s=320x240:d=1", "-frames:v", "1", "-y", f.image)
	return f
}

// sourceFor picks the fixture a tool accepts.
func sourceFor(tool Tool, f fixtures) string {
	accepts := func(path string) bool {
		ext := strings.TrimPrefix(filepath.Ext(path), ".")
		for _, a := range tool.Accept {
			if a == ext {
				return true
			}
		}
		return false
	}
	for _, candidate := range []string{f.video, f.audio, f.image} {
		if accepts(candidate) {
			return candidate
		}
	}
	return ""
}

// TestEveryToolRunsOnRealMedia runs every tool that works on local files,
// with its default options, through real ffmpeg, and checks it wrote
// something. Most tools had only ever been checked by the arguments they
// build; this runs them.
func TestEveryToolRunsOnRealMedia(t *testing.T) {
	bins := needFFmpeg(t)
	f := makeFixtures(t, bins.FFmpeg)
	out := t.TempDir()

	for _, tool := range All() {
		tool := tool
		source := sourceFor(tool, f)
		if tool.Input == InputURL || tool.Requires != "" || source == "" {
			continue // downloads need the network; transcription needs a model
		}
		t.Run(tool.ID, func(t *testing.T) {
			opts, err := tool.Validate(nil)
			if err != nil {
				t.Fatalf("defaults: %v", err)
			}
			in := Inputs{Path: source, Paths: []string{source}, Options: opts, DurationS: 3,
				SubtitleTracks: subtitleTracks(t, source)}
			if tool.Combine {
				in.Paths = []string{f.video, f.clip}
				if source == f.image {
					in.Paths = []string{f.image, f.image}
				}
			}
			in.Output = filepath.Join(out, strings.ReplaceAll(tool.ID, ".", "_")+"."+tool.ExtensionFor(opts, source))
			if tool.ID == "video.frames" {
				frames := filepath.Join(out, "frames")
				if err := os.MkdirAll(frames, 0o700); err != nil {
					t.Fatal(err)
				}
				in.Output = filepath.Join(frames, "frame_%05d.png")
			}
			runPlan(t, bins.FFmpeg, tool, in)
			checkOutput(t, in.Output)
		})
	}
}

// encoderWorks reports whether ffmpeg can open encoder on this machine.
func encoderWorks(bin, encoder string) bool {
	return exec.Command(bin, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=d=1",
		"-c:v", encoder, "-f", "null", "-").Run() == nil
}

// subtitleTracks counts the subtitle tracks in path, as the app does before
// building a tool that needs them.
func subtitleTracks(t *testing.T, path string) int {
	t.Helper()
	info, err := ffmpeg.Probe(t.Context(), path)
	if err != nil {
		t.Fatalf("probe %s: %v", filepath.Base(path), err)
	}
	n := 0
	for _, s := range info.Streams {
		if s.Type == "subtitle" {
			n++
		}
	}
	return n
}

// runPlan builds a tool's plan for the CPU and runs every pass of it.
func runPlan(t *testing.T, bin string, tool Tool, in Inputs) Plan {
	t.Helper()
	return runPlanWith(t, bin, tool, in, softwareEncode())
}

// runPlanWith builds a tool's plan for enc and runs every pass of it.
func runPlanWith(t *testing.T, bin string, tool Tool, in Inputs, enc Encode) Plan {
	t.Helper()
	plan, err := tool.Build(in, enc)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, file := range plan.Files {
		if err := os.WriteFile(file.Path, []byte(file.Content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range plan.Commands() {
		run(t, bin, args...)
	}
	// Moved into place as the queue does
	if plan.Staged != "" {
		if err := os.Rename(plan.Staged, plan.OutputPath); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range append(filesOf(plan), plan.Cleanup...) {
		_ = os.Remove(path)
	}
	return plan
}

// filesOf lists a plan's temporary file paths.
func filesOf(p Plan) []string {
	out := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		out = append(out, f.Path)
	}
	return out
}

// checkOutput fails when path, or the first frame of a sequence, is missing
// or empty.
func checkOutput(t *testing.T, path string) {
	t.Helper()
	if strings.Contains(path, "%05d") {
		path = strings.ReplaceAll(path, "%05d", "00001")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no output: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("the output is empty")
	}
}

// TestCompressToSizeLandsUnderTarget encodes a clip that would be far over
// the target, by both methods, and checks each result fits.
func TestCompressToSizeLandsUnderTarget(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "busy.mp4")
	// Moving noise is the hardest content to compress, so this is the worst case.
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=1280x720:r=30:d=12,noise=alls=25:allf=t",
		"-f", "lavfi", "-i", "sine=d=12", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "18",
		"-c:a", "aac", "-shortest", "-y", source)

	tool, err := Get("video.compress")
	if err != nil {
		t.Fatal(err)
	}
	const targetMB = 2
	type attempt struct {
		method string
		enc    Encode
	}
	attempts := []attempt{{"accurate", softwareEncode()}, {"fast", softwareEncode()}}
	if encoderWorks(bins.FFmpeg, "h264_nvenc") {
		nvenc := hardwareEncode()
		nvenc.DeviceIndex = 0
		attempts = append(attempts, attempt{"fast-nvenc", nvenc})
	}
	for _, a := range attempts {
		method := a.method
		opts, _ := tool.Validate(map[string]string{"size": strconv.Itoa(targetMB),
			"method": strings.TrimSuffix(method, "-nvenc")})
		in := Inputs{Path: source, Output: filepath.Join(dir, method+".mp4"), Options: opts, DurationS: 12}
		plan := runPlanWith(t, bins.FFmpeg, tool, in, a.enc)

		info, err := os.Stat(in.Output)
		if err != nil {
			t.Fatalf("%s: no output: %v", method, err)
		}
		limit := int64(targetMB * 1_000_000)
		t.Logf("%s: %d bytes of a %d-byte target (%.0f%%)", method, info.Size(), limit,
			100*float64(info.Size())/float64(limit))
		if info.Size() > limit {
			t.Errorf("%s: %d bytes, over the %d-byte target", method, info.Size(), limit)
		}
		if info.Size() < limit/3 {
			t.Errorf("%s: %d bytes, wasting most of the %d-byte target", method, info.Size(), limit)
		}
		for _, leftover := range plan.Cleanup {
			if _, err := os.Stat(leftover); err == nil {
				t.Errorf("%s: %s was left behind", method, filepath.Base(leftover))
			}
		}
	}
}

// TestJoinCopyKeepsEveryClip checks a stream-copy join is as long as its
// clips together.
func TestJoinCopyKeepsEveryClip(t *testing.T) {
	bins := needFFmpeg(t)
	f := makeFixtures(t, bins.FFmpeg)
	tool, _ := Get("video.join")
	opts, _ := tool.Validate(nil)
	in := Inputs{Path: f.video, Paths: []string{f.video, f.clip}, Options: opts,
		Output: filepath.Join(f.dir, "joined.mp4")}
	runPlan(t, bins.FFmpeg, tool, in)

	info, err := ffmpeg.Probe(t.Context(), in.Output)
	if err != nil {
		t.Fatal(err)
	}
	if info.DurationS < 5.5 || info.DurationS > 6.5 {
		t.Errorf("joined length %.2fs, want about 6s", info.DurationS)
	}
}

// TestSoundToolsKeepTheirPicture checks the clean-up tools, given a video,
// write a video with its picture untouched.
func TestSoundToolsKeepTheirPicture(t *testing.T) {
	bins := needFFmpeg(t)
	f := makeFixtures(t, bins.FFmpeg)
	for _, id := range []string{"audio.denoise", "audio.dialogue"} {
		tool, _ := Get(id)
		opts, _ := tool.Validate(map[string]string{"format": "mp3"})
		ext := tool.ExtensionFor(opts, f.video)
		if ext != "mp4" {
			t.Errorf("%s: a video would be written as .%s", id, ext)
			continue
		}
		in := Inputs{Path: f.video, Options: opts, Output: filepath.Join(f.dir, id+".mp4")}
		runPlan(t, bins.FFmpeg, tool, in)
		info, err := ffmpeg.Probe(t.Context(), in.Output)
		if err != nil {
			t.Fatal(err)
		}
		if !hasStream(info, "video", "h264") || !hasStream(info, "audio", "") {
			t.Errorf("%s: output streams %+v, want the original H.264 picture and sound", id, info.Streams)
		}
	}
}

// hasStream reports whether info has a stream of kind, and codec if given.
func hasStream(info ffmpeg.MediaInfo, kind, codec string) bool {
	for _, s := range info.Streams {
		if s.Type == kind && (codec == "" || s.Codec == codec) {
			return true
		}
	}
	return false
}

// TestWatermarkTakesAwkwardText runs a watermark whose text holds every
// character drawtext or the filtergraph treats as structural.
func TestWatermarkTakesAwkwardText(t *testing.T) {
	bins := needFFmpeg(t)
	f := makeFixtures(t, bins.FFmpeg)
	tool, _ := Get("image.watermark")
	opts, err := tool.Validate(map[string]string{"text": `Eli's: [draft], 100% ; a\b`})
	if err != nil {
		t.Fatal(err)
	}
	in := Inputs{Path: f.image, Options: opts, Output: filepath.Join(f.dir, "marked.png")}
	runPlan(t, bins.FFmpeg, tool, in)
	checkOutput(t, in.Output)
}

// TestTranscribeFilterParses runs Transcribe with a fake model at a path
// holding every filtergraph special character. ffmpeg must get as far as
// loading the model, which proves the path and output reached the filter
// intact; only the model itself may be refused.
func TestTranscribeFilterParses(t *testing.T) {
	bins := needFFmpeg(t)
	f := makeFixtures(t, bins.FFmpeg)
	model := filepath.Join(f.dir, "Eli's [model], v1.bin")
	if err := os.WriteFile(model, []byte("not a model"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, _ := Get("subtitles.transcribe")
	opts, _ := tool.Validate(nil)
	enc := softwareEncode()
	enc.SpeechModel = model
	in := Inputs{Path: f.audio, Options: opts, Output: filepath.Join(f.dir, "Ocean's [notes], 1.srt")}
	plan, err := tool.Build(in, enc)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command(bins.FFmpeg, append([]string{"-hide_banner", "-nostdin"}, plan.Args...)...).CombinedOutput()
	// Every option is parsed before the model loads, so reaching the load
	// with the exact path shows both paths survived the escaping.
	if !strings.Contains(string(out), "from model: "+model) {
		t.Errorf("ffmpeg never tried to load the model at its real path:\n%s", out)
	}
}

// TestBurnInUsesASubtitleFile burns a subtitle file beside a video that has
// no track of its own, as Transcribe to Subtitles leaves it, and checks that
// asking for the missing track fails with a reason a person can act on.
func TestBurnInUsesASubtitleFile(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "Ocean's [cut], one.mp4")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=2", "-f", "lavfi", "-i", "sine=d=2",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", "-y", video)
	subs := filepath.Join(dir, "Ocean's [cut], one.srt")
	if err := os.WriteFile(subs, []byte("1\n00:00:00,000 --> 00:00:02,000\nHello there\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, _ := Get("video.burnsubs")

	opts, _ := tool.Validate(nil)
	in := Inputs{Path: video, Sidecar: subs, Options: opts, Output: filepath.Join(dir, "burned.mp4")}
	runPlan(t, bins.FFmpeg, tool, in)
	checkOutput(t, in.Output)

	// Before probing, the count is unknown and ffmpeg itself must refuse.
	opts, _ = tool.Validate(map[string]string{"from": "video"})
	in = Inputs{Path: video, Sidecar: subs, Options: opts, Output: filepath.Join(dir, "none.mp4"),
		SubtitleTracks: UnknownCount}
	plan, err := tool.Build(in, softwareEncode())
	if err != nil {
		t.Fatal(err)
	}
	_, err = ffmpeg.Run(t.Context(), ffmpeg.Request{Binary: bins.FFmpeg, Args: plan.Args}, nil)
	if err == nil || !strings.Contains(err.Error(), "no subtitle track") {
		t.Errorf("a video without a track failed with %v, want a plain reason", err)
	}
}

// TestBurnInWritesSubtitlesFromSpeech checks that a video with no subtitles
// anywhere is transcribed first, into a temporary file the burn pass reads
// and the job removes, and that the transcription pass gets as far as
// loading the model.
func TestBurnInWritesSubtitlesFromSpeech(t *testing.T) {
	bins := needFFmpeg(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "no subs.mp4")
	run(t, bins.FFmpeg, "-f", "lavfi", "-i", "testsrc2=s=320x240:r=30:d=1", "-f", "lavfi", "-i", "sine=d=1",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", "-y", video)
	model := filepath.Join(dir, "Eli's [model].bin")
	if err := os.WriteFile(model, []byte("not a model"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, _ := Get("video.burnsubs")
	opts, _ := tool.Validate(nil)
	enc := softwareEncode()
	enc.SpeechModel = model
	in := Inputs{Path: video, Options: opts, Output: filepath.Join(dir, "out.mp4"), SubtitleTracks: 0}

	plan, err := tool.Build(in, enc)
	if err != nil {
		t.Fatal(err)
	}
	speech := in.Output + speechSuffix
	if len(plan.Passes) != 1 || len(plan.Cleanup) != 1 || plan.Cleanup[0] != speech {
		t.Fatalf("want a transcription pass, then the burn, then %s removed; got %+v", speech, plan)
	}
	if !strings.Contains(strings.Join(plan.Passes[0], " "), "subtitles=") {
		t.Errorf("the second pass doesn't burn the subtitles: %v", plan.Passes[0])
	}
	out, _ := exec.Command(bins.FFmpeg, append([]string{"-hide_banner", "-nostdin"}, plan.Args...)...).CombinedOutput()
	if !strings.Contains(string(out), "from model: "+model) {
		t.Errorf("the transcription pass never tried to load the model:\n%s", out)
	}

	// With no model either, it says what would help.
	enc.SpeechModel = ""
	if _, err := tool.Build(in, enc); err == nil || !strings.Contains(err.Error(), "speech model") {
		t.Errorf("no subtitles and no model gave %v, want a pointer to a speech model", err)
	}
}
