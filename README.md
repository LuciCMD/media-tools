# Media Tools

A desktop front end for **ffmpeg**, **ffprobe** and **yt-dlp** that gives every conversion a progress
bar you can actually trust.

Written in Go with a Wails interface: one self-contained executable of about 12 MB, with no runtime to
install.

**Author:** LuciCMD — MIT licensed.

---

## Why

Three problems with wrapping ffmpeg in a GUI, and how this handles them.

**Progress that means something.** Most wrappers show a spinner, or a bar that jumps to some arbitrary
point and stops. This probes the source with `ffprobe` first, then reads ffmpeg's `-progress` stream, so
a job reports a real percentage, position against total duration, frame count, encode rate, speed
against realtime, an ETA, and the growing output size. When no duration can be derived it shows an
animated indeterminate bar rather than a number it cannot justify.

**Hardware detection that is not a guess.** A full ffmpeg build advertises NVENC, Quick Sync, AMF *and*
VAAPI at once, whatever hardware is actually present, so checking that list reports GPU support on
machines with none. Here every encode path is verified by running a real one-frame test encode, and the
Settings page shows what worked, what did not, and why.

**Settings that stay put.** Every tool option is remembered per tool. Encoder, GPU device, quality,
preset, output location and overwrite policy are set once and persist.

**Comfortable to use.** Drop files anywhere on a tool, find a tool by typing, and keep an eye on a
batch from the taskbar: overall progress is in the window title. Failed jobs can be retried in one
click, and a desktop notification can say when a long batch is done.

---

## Tools

**Audio** — convert, soften harsh audio, enhance dialogue, reduce noise and mains hum, normalize
loudness (EBU R128), trim silence, extract audio from video, change speed. Enhance Dialogue and Reduce
Noise also work on a video's sound, leaving the picture untouched.

**Video** — convert, compress to a file size (such as a chat app's upload limit), change container
without re-encoding, join clips, change resolution, trim, crop, rotate and flip, convert to GIF, remove
audio, contact sheet, extract frames, colour filter.

**Image** — convert, HEIC to JPG, resize, create icon, strip metadata, watermark, optimise size.

**Subtitles** — transcribe speech into subtitles or a transcript, optionally translated into English;
burn in subtitles from any subtitle file, a track inside the video, or, when there are none, the speech,
transcribed first in the same job; extract subtitles.

**Download** — video, audio and playlists through yt-dlp, with optional browser cookies, subtitle
embedding, SponsorBlock, and a download archive so re-running a playlist fetches only what is new.

**Inspect** — container, duration, bitrate and a per-stream breakdown.

Selecting several files queues one job per file, except for Join Clips, which makes one file from
them in the order listed. Jobs run on a configurable worker pool and can be cancelled individually or
all at once.

---

## Privacy

Everything runs on this computer. The app makes no network requests of its own, with one exception
you start yourself: downloading a speech model for transcription, from Settings. The download tools
fetch only what you ask yt-dlp for.

Speech models come from the [whisper.cpp model repository](https://huggingface.co/ggerganov/whisper.cpp)
on Hugging Face. These are the only addresses the app downloads from, and each file is checked against
the SHA-256 below before it is used; a file that doesn't match is deleted.

| Model | Size | Download | SHA-256 |
|---|---|---|---|
| Tiny | 78 MB | [ggml-tiny.bin](https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin) | `be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21` |
| Base | 148 MB | [ggml-base.bin](https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin) | `60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe` |
| Small | 488 MB | [ggml-small.bin](https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin) | `1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b` |
| Large v3 Turbo | 574 MB | [ggml-large-v3-turbo-q5_0.bin](https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin) | `394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2` |

Hugging Face may redirect a download to its own file storage. You can also fetch a model yourself and
pick it with **Choose Model File** in Settings.

---

## Hardware Acceleration

| Vendor | Encoders |
|---|---|
| NVIDIA NVENC | H.264, HEVC, AV1 |
| Intel Quick Sync | H.264, HEVC, AV1 |
| AMD AMF | H.264, HEVC |
| Apple VideoToolbox | H.264, HEVC |
| Software | x264, x265 |

Whatever the machine supports appears in a dropdown; anything else is greyed out with the reason it
failed. Multiple NVIDIA GPUs are enumerated so a specific device can be targeted. Software encoding is
always available, and the best available encoder is selected automatically on first run.

---

## Install

Download `MediaTools.exe` from the releases page, or build it:

```bash
build.bat
```

Then install what you need on `PATH`:

- **ffmpeg + ffprobe** — required by everything except downloads.
  <https://ffmpeg.org/download.html>
- **yt-dlp** — required by the download tools. `pip install yt-dlp`

Transcription needs ffmpeg 8.0 or later built with whisper, such as the full build from
[gyan.dev](https://www.gyan.dev/ffmpeg/builds/). The app checks and says so if yours can't.

Both can also live in a `bin/`, `ffmpeg/` or `vendor/` folder beside the executable. A missing
dependency produces a warning and disables only the tools that need it; the application still starts.

---

## Build From Source

Go 1.21 or later and the Wails CLI, which `build.bat` installs if it is absent.

```bash
go test -race . ./internal/...
```

```bash
wails dev
```

`build.bat` checks the toolchain, runs the tests, and writes `build\bin\MediaTools.exe`. On Linux or
macOS use `./build.sh`.
