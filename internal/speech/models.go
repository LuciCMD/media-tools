// Package speech manages the whisper.cpp models that ffmpeg's whisper filter
// needs to transcribe speech. It is the only part of the application that
// makes a network request, and only when the user asks for a model.
package speech

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bounds on a download (Power of Ten rule 2).
const (
	baseURL        = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"
	headerTimeout  = 30 * time.Second
	progressEvery  = 250 * time.Millisecond
	copyBufferSize = 256 << 10
	maxRedirects   = 5
)

// Model is one downloadable whisper.cpp model. Size and SHA256 are the values
// Hugging Face publishes for the file, checked after every download so a
// truncated or altered file is never used.
type Model struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Hint      string `json:"hint"`
	File      string `json:"file"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"-"`
}

// catalog lists the models offered, smallest first. Sizes and checksums were
// read from huggingface.co/api/models/ggerganov/whisper.cpp on 2026-09-26.
var catalog = []Model{
	{ID: "tiny", Name: "Tiny", Hint: "Fastest, rough on accents and noise", File: "ggml-tiny.bin",
		SizeBytes: 77691713, SHA256: "be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21"},
	{ID: "base", Name: "Base", Hint: "Quick, good for clear speech", File: "ggml-base.bin",
		SizeBytes: 147951465, SHA256: "60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe"},
	{ID: "small", Name: "Small", Hint: "Slower, noticeably more accurate", File: "ggml-small.bin",
		SizeBytes: 487601967, SHA256: "1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b"},
	{ID: "turbo", Name: "Large v3 Turbo", Hint: "Most accurate, and the best at translating", File: "ggml-large-v3-turbo-q5_0.bin",
		SizeBytes: 574041195, SHA256: "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2"},
}

// Catalog returns the models on offer.
func Catalog() []Model {
	out := make([]Model, len(catalog))
	copy(out, catalog)
	return out
}

// Find returns the model with the given ID.
func Find(id string) (Model, bool) {
	for _, m := range catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// Installed reports whether m is already downloaded, complete, into dir.
func Installed(dir string, m Model) (string, bool) {
	path := filepath.Join(dir, m.File)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() != m.SizeBytes {
		return path, false
	}
	return path, true
}

// Progress reports how far a download has got.
type Progress struct {
	ID       string `json:"id"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
}

// Download fetches m into dir and returns the path of the verified file.
// It writes to a .part file and renames it only once the size and SHA-256
// match, so an interrupted or corrupted download never looks installed.
func Download(ctx context.Context, m Model, dir string, onProgress func(Progress)) (string, error) {
	return download(ctx, baseURL+m.File, m, dir, onProgress)
}

// download is Download with the address injectable, for tests.
func download(ctx context.Context, url string, m Model, dir string, onProgress func(Progress)) (string, error) {
	if m.File == "" || m.SizeBytes <= 0 || len(m.SHA256) != 64 {
		return "", errors.New("speech: that model has no published size or checksum")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("couldn't create %s: %w", dir, err)
	}
	final := filepath.Join(dir, m.File)
	part := final + ".part"

	body, err := open(ctx, url, m)
	if err != nil {
		return "", err
	}
	defer body.Close()

	sum, err := save(ctx, body, part, m, onProgress)
	if err != nil {
		// A partial file is useless; it is removed rather than resumed.
		_ = os.Remove(part)
		return "", err
	}
	if sum != m.SHA256 {
		_ = os.Remove(part)
		return "", errors.New("the downloaded model didn't match its published checksum, so it was discarded")
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("couldn't finish saving the model: %w", err)
	}
	return final, nil
}

// open starts the request and checks the response before anything is saved.
func open(ctx context.Context, url string, m Model) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: headerTimeout},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("couldn't reach the model server: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("the model server answered %s", resp.Status)
	}
	if resp.ContentLength > 0 && resp.ContentLength != m.SizeBytes {
		resp.Body.Close()
		return nil, fmt.Errorf("the server offered %d bytes where %d were expected", resp.ContentLength, m.SizeBytes)
	}
	return resp.Body, nil
}

// save copies body into path, hashing as it goes, and never reads more than
// the model's published size plus one byte, so an oversized reply is caught
// without filling the disk.
func save(ctx context.Context, body io.Reader, path string, m Model, onProgress func(Progress)) (string, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("couldn't save the model: %w", err)
	}
	defer f.Close()

	hash := sha256.New()
	limited := io.LimitReader(body, m.SizeBytes+1)
	buf := make([]byte, copyBufferSize)
	var received int64
	last := time.Time{}

	// Bounded: each read makes progress and the reader stops after
	// SizeBytes+1 bytes.
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, readErr := limited.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return "", fmt.Errorf("couldn't save the model: %w", err)
			}
			hash.Write(buf[:n])
			received += int64(n)
			if onProgress != nil && time.Since(last) >= progressEvery {
				last = time.Now()
				onProgress(Progress{ID: m.ID, Received: received, Total: m.SizeBytes})
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("the download stopped: %w", readErr)
		}
	}
	if received != m.SizeBytes {
		return "", fmt.Errorf("the download stopped at %d of %d bytes", received, m.SizeBytes)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("couldn't save the model: %w", err)
	}
	if onProgress != nil {
		onProgress(Progress{ID: m.ID, Received: received, Total: m.SizeBytes})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// LooksLikeModel is a light check on a model file the user chose themselves:
// whisper.cpp models are GGML files, which start with a known magic number.
func LooksLikeModel(path string) error {
	if !strings.EqualFold(filepath.Ext(path), ".bin") {
		return errors.New("a whisper.cpp model is a .bin file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return errors.New("that file is too short to be a model")
	}
	// The magic 0x67676d6c ("ggml") is written as a little-endian integer, so
	// it reads "lmgg" on disk; the byte order is accepted either way round.
	if string(magic) != "lmgg" && string(magic) != "ggml" {
		return errors.New("that file isn't a whisper.cpp model")
	}
	return nil
}
