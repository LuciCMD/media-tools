package speech

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeModel returns content and a Model describing it truthfully.
func fakeModel(content string) Model {
	sum := sha256.Sum256([]byte(content))
	return Model{ID: "fake", File: "ggml-fake.bin", SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
}

// serve answers every request with body.
func serve(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDownloadVerifiesAndInstalls checks a good download lands in place,
// with progress reported and no .part file left.
func TestDownloadVerifiesAndInstalls(t *testing.T) {
	content := strings.Repeat("lmgg-model-bytes-", 40000)
	m := fakeModel(content)
	dir := t.TempDir()

	var last Progress
	path, err := download(context.Background(), serve(t, content).URL, m, dir, func(p Progress) { last = p })
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if _, ok := Installed(dir, m); !ok {
		t.Error("the model isn't reported as installed")
	}
	if last.Received != m.SizeBytes || last.Total != m.SizeBytes {
		t.Errorf("final progress %d/%d, want %d/%d", last.Received, last.Total, m.SizeBytes, m.SizeBytes)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("the .part file was left behind")
	}
}

// TestDownloadRejectsWrongChecksum checks an altered file is discarded.
func TestDownloadRejectsWrongChecksum(t *testing.T) {
	m := fakeModel("the real model")
	dir := t.TempDir()
	_, err := download(context.Background(), serve(t, "a fake modelx").URL, m, dir, nil)
	if err == nil {
		t.Fatal("a file with the wrong checksum was accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("rejected download left %d files behind", len(entries))
	}
}

// TestDownloadRejectsWrongSize checks short and oversized replies fail.
func TestDownloadRejectsWrongSize(t *testing.T) {
	m := fakeModel("exactly this")
	for _, body := range []string{"short", "exactly this and then a lot more besides"} {
		if _, err := download(context.Background(), serve(t, body).URL, m, t.TempDir(), nil); err == nil {
			t.Errorf("a %d-byte reply for a %d-byte model was accepted", len(body), m.SizeBytes)
		}
	}
}

// TestDownloadStopsWhenCancelled checks cancellation is honoured.
func TestDownloadStopsWhenCancelled(t *testing.T) {
	content := strings.Repeat("x", 1<<20)
	m := fakeModel(content)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := download(ctx, serve(t, content).URL, m, t.TempDir(), nil); err == nil {
		t.Error("a cancelled download succeeded")
	}
}

// TestCatalogIsComplete checks every offered model can be verified.
func TestCatalogIsComplete(t *testing.T) {
	for _, m := range Catalog() {
		if m.ID == "" || m.Name == "" || !strings.HasSuffix(m.File, ".bin") || m.SizeBytes <= 0 || len(m.SHA256) != 64 {
			t.Errorf("model %+v is missing something needed to download and verify it", m)
		}
		if _, ok := Find(m.ID); !ok {
			t.Errorf("Find(%q) failed", m.ID)
		}
	}
}

// TestLooksLikeModel checks the magic-number test on chosen files.
func TestLooksLikeModel(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.bin")
	bad := filepath.Join(dir, "bad.bin")
	other := filepath.Join(dir, "model.txt")
	for path, body := range map[string]string{good: "lmgg....", bad: "PK\x03\x04zip", other: "lmgg"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := LooksLikeModel(good); err != nil {
		t.Errorf("a GGML file was rejected: %v", err)
	}
	if LooksLikeModel(bad) == nil || LooksLikeModel(other) == nil {
		t.Error("a file that isn't a model was accepted")
	}
}

// TestReadmeListsEveryModel checks the README shows the exact address and
// checksum of every model the app can download, so what users are told
// can't drift from what the app does.
func TestReadmeListsEveryModel(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range Catalog() {
		for _, want := range []string{baseURL + m.File, m.SHA256} {
			if !strings.Contains(string(readme), want) {
				t.Errorf("README.md doesn't list %s for the %s model", want, m.Name)
			}
		}
	}
}
