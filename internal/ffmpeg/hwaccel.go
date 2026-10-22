package ffmpeg

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Bounds for encoder detection (Power of Ten rule 2).
const (
	probeEncodeTimeout = 12 * time.Second
	maxCandidates      = 16
	maxDevices         = 8
)

// Vendor identifies the hardware family behind an encoder.
type Vendor string

const (
	VendorCPU    Vendor = "cpu"
	VendorNVIDIA Vendor = "nvidia"
	VendorIntel  Vendor = "intel"
	VendorAMD    Vendor = "amd"
	VendorApple  Vendor = "apple"
)

// Backend is one usable encode path exposed to the user.
type Backend struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Vendor    Vendor `json:"vendor"`
	Encoder   string `json:"encoder"`  // ffmpeg -c:v value
	HWAccel   string `json:"hwaccel"`  // ffmpeg -hwaccel value, empty for none
	RateFlag  string `json:"rateFlag"` // crf, qp or q
	Codec     string `json:"codec"`    // h264, hevc or av1
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

// Device is a physical accelerator the user can target.
type Device struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Vendor Vendor `json:"vendor"`
}

// candidate describes an encoder to test, before availability is known.
type candidate struct {
	id       string
	label    string
	encoder  string
	hwaccel  string
	rateFlag string
	codec    string
	vendor   Vendor
}

// candidates lists every encode path the application knows how to drive.
func candidates() []candidate {
	return []candidate{
		{"cpu-h264", "CPU - x264", "libx264", "", "crf", "h264", VendorCPU},
		{"cpu-hevc", "CPU - x265", "libx265", "", "crf", "hevc", VendorCPU},
		{"nvidia-h264", "NVIDIA NVENC - H.264", "h264_nvenc", "cuda", "qp", "h264", VendorNVIDIA},
		{"nvidia-hevc", "NVIDIA NVENC - HEVC", "hevc_nvenc", "cuda", "qp", "hevc", VendorNVIDIA},
		{"nvidia-av1", "NVIDIA NVENC - AV1", "av1_nvenc", "cuda", "qp", "av1", VendorNVIDIA},
		{"intel-h264", "Intel Quick Sync - H.264", "h264_qsv", "qsv", "q", "h264", VendorIntel},
		{"intel-hevc", "Intel Quick Sync - HEVC", "hevc_qsv", "qsv", "q", "hevc", VendorIntel},
		{"intel-av1", "Intel Quick Sync - AV1", "av1_qsv", "qsv", "q", "av1", VendorIntel},
		{"amd-h264", "AMD AMF - H.264", "h264_amf", "d3d11va", "q", "h264", VendorAMD},
		{"amd-hevc", "AMD AMF - HEVC", "hevc_amf", "d3d11va", "q", "hevc", VendorAMD},
		{"apple-h264", "Apple VideoToolbox - H.264", "h264_videotoolbox", "videotoolbox", "q", "h264", VendorApple},
		{"apple-hevc", "Apple VideoToolbox - HEVC", "hevc_videotoolbox", "videotoolbox", "q", "hevc", VendorApple},
	}
}

// runDetection returns every encode path, marking which ones this machine can
// actually run.
//
// The build's compiled-in encoder list is deliberately NOT trusted: a full
// ffmpeg build advertises NVENC, QSV, AMF and VAAPI simultaneously regardless
// of the hardware present. Each candidate is therefore verified by running a
// real one-frame encode against a synthetic null source, concurrently.
func runDetection(ctx context.Context) []Backend {
	bins := Locate()
	list := candidates()
	if len(list) > maxCandidates {
		list = list[:maxCandidates]
	}
	results := make([]Backend, len(list))

	if bins.FFmpeg == "" {
		for i, c := range list {
			results[i] = toBackend(c, false, "ffmpeg not found")
		}
		return results
	}

	supported := listedEncoders(ctx, bins.FFmpeg)
	var wg sync.WaitGroup
	for i, c := range list {
		if !supported[c.encoder] {
			results[i] = toBackend(c, false, "not compiled into this ffmpeg build")
			continue
		}
		wg.Add(1)
		go func(slot int, cand candidate) {
			defer wg.Done()
			ok, detail := testEncode(ctx, bins.FFmpeg, cand.encoder)
			results[slot] = toBackend(cand, ok, detail)
		}(i, c)
	}
	wg.Wait()

	sortBackends(results)
	return results
}

// sortBackends puts working backends first, with the CPU fallback ahead of
// other equally-available entries so a default always exists.
func sortBackends(results []Backend) {
	sort.SliceStable(results, func(a, b int) bool {
		if results[a].Available != results[b].Available {
			return results[a].Available
		}
		return false
	})
}

// toBackend materialises a candidate into its user-facing form.
func toBackend(c candidate, available bool, detail string) Backend {
	return Backend{
		ID:        c.id,
		Label:     c.label,
		Vendor:    c.vendor,
		Encoder:   c.encoder,
		HWAccel:   c.hwaccel,
		RateFlag:  c.rateFlag,
		Codec:     c.codec,
		Available: available,
		Detail:    detail,
	}
}

// listedEncoders returns the encoder names this ffmpeg build was compiled with.
func listedEncoders(ctx context.Context, ffmpegPath string) map[string]bool {
	runCtx, cancel := context.WithTimeout(ctx, probeEncodeTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ffmpegPath, "-hide_banner", "-encoders")
	hideWindow(cmd)
	out, err := cmd.Output()
	found := make(map[string]bool, 64)
	if err != nil {
		return found
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		found[fields[1]] = true
	}
	return found
}

// HasFilter reports whether this ffmpeg build includes the named filter.
// Some filters, such as whisper, exist only in recent or fuller builds.
func HasFilter(ctx context.Context, ffmpegPath, name string) bool {
	if ffmpegPath == "" {
		return false
	}
	runCtx, cancel := context.WithTimeout(ctx, probeEncodeTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ffmpegPath, "-hide_banner", "-filters")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			return true
		}
	}
	return false
}

// testEncode verifies an encoder by encoding a tiny synthetic clip with it.
func testEncode(ctx context.Context, ffmpegPath, encoder string) (bool, string) {
	runCtx, cancel := context.WithTimeout(ctx, probeEncodeTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "nullsrc=s=256x256:d=0.1",
		"-c:v", encoder, "-frames:v", "1",
		"-f", "null", "-",
	)
	hideWindow(cmd)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, "verified by test encode"
	}
	return false, summarizeFailure(string(out))
}

// summarizeFailure condenses ffmpeg stderr into a single readable reason.
func summarizeFailure(out string) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return "hardware not present or driver unavailable"
	}
	lines := strings.Split(trimmed, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 160 {
		last = last[:160] + "..."
	}
	return last
}

// ListDevices enumerates targetable accelerators, always including the CPU.
func ListDevices(ctx context.Context, backends []Backend) []Device {
	devices := []Device{{Index: -1, Name: "CPU (Software)", Vendor: VendorCPU}}
	if hasVendor(backends, VendorNVIDIA) {
		devices = append(devices, nvidiaDevices(ctx)...)
	}
	if hasVendor(backends, VendorIntel) {
		devices = append(devices, Device{Index: 0, Name: "Intel Integrated GPU", Vendor: VendorIntel})
	}
	if hasVendor(backends, VendorAMD) {
		devices = append(devices, Device{Index: 0, Name: "AMD GPU", Vendor: VendorAMD})
	}
	if len(devices) > maxDevices {
		devices = devices[:maxDevices]
	}
	return devices
}

// hasVendor reports whether any available backend belongs to vendor.
func hasVendor(backends []Backend, vendor Vendor) bool {
	for _, b := range backends {
		if b.Vendor == vendor && b.Available {
			return true
		}
	}
	return false
}

// nvidiaDevices queries nvidia-smi for the installed NVIDIA adapters.
func nvidiaDevices(ctx context.Context) []Device {
	runCtx, cancel := context.WithTimeout(ctx, probeEncodeTimeout)
	defer cancel()

	fallback := []Device{{Index: 0, Name: "NVIDIA GPU", Vendor: VendorNVIDIA}}
	smi, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return fallback
	}
	cmd := exec.CommandContext(runCtx, smi,
		"--query-gpu=index,name", "--format=csv,noheader")
	hideWindow(cmd)

	out, err := cmd.Output()
	if err != nil {
		return fallback
	}
	return parseSMI(string(out))
}

// parseSMI converts nvidia-smi CSV rows into devices, bounded by maxDevices.
func parseSMI(out string) []Device {
	devices := make([]Device, 0, maxDevices)
	for _, line := range strings.Split(out, "\n") {
		if len(devices) >= maxDevices {
			break
		}
		parts := strings.SplitN(strings.TrimSpace(line), ",", 2)
		if len(parts) != 2 {
			continue
		}
		devices = append(devices, Device{
			Index:  int(parseInt(parts[0])),
			Name:   strings.TrimSpace(parts[1]),
			Vendor: VendorNVIDIA,
		})
	}
	if len(devices) == 0 {
		devices = append(devices, Device{Index: 0, Name: "NVIDIA GPU", Vendor: VendorNVIDIA})
	}
	return devices
}

// FindBackend returns the backend with the given ID, or false when absent.
func FindBackend(backends []Backend, id string) (Backend, bool) {
	for _, b := range backends {
		if b.ID == id {
			return b, true
		}
	}
	return Backend{}, false
}

// DefaultBackend picks the best available backend, preferring hardware.
func DefaultBackend(backends []Backend) Backend {
	var cpu Backend
	var found bool
	for _, b := range backends {
		if !b.Available {
			continue
		}
		if b.Vendor != VendorCPU && b.Codec == "h264" {
			return b
		}
		if b.Vendor == VendorCPU && b.Codec == "h264" && !found {
			cpu, found = b, true
		}
	}
	if found {
		return cpu
	}
	return Backend{ID: "cpu-h264", Label: "CPU - x264", Vendor: VendorCPU,
		Encoder: "libx264", RateFlag: "crf", Codec: "h264", Available: true}
}
