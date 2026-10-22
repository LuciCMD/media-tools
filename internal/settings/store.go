// Package settings persists user preferences between runs of the application.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Bounds on persisted state (Power of Ten rules 2 and 3).
const (
	MaxRecentPaths = 12
	// MaxToolDefaults bounds the remembered tool options. Keys are
	// "tool.option", so the ceiling must cover every option of every tool:
	// the registry allows 128 tools of 12 options each. It was once 32, which
	// silently discarded remembered options after the first few tools.
	MaxToolDefaults = 2048
	MaxLastDirs     = 256
	maxSettingsSize = 1 << 20
	fileMode        = 0o600
	dirMode         = 0o700
)

// Settings is the complete persisted preference set.
type Settings struct {
	Theme           string `json:"theme"`
	Accent          string `json:"accent"`
	BackendID       string `json:"backendId"`
	DeviceIndex     int    `json:"deviceIndex"`
	Quality         int    `json:"quality"`
	Preset          string `json:"preset"`
	AudioBitrateKb  int    `json:"audioBitrateKb"`
	OutputMode      string `json:"outputMode"` // alongside | fixed
	OutputDir       string `json:"outputDir"`
	OverwritePolicy string `json:"overwritePolicy"` // ask | overwrite | suffix
	Concurrency     int    `json:"concurrency"`
	KeepMetadata    bool   `json:"keepMetadata"`
	// NotifyOnDone is kept only so older files still load. It defaulted to
	// on while it did nothing; Notifications replaces it and starts off,
	// because turning notifications on writes to the Windows registry.
	NotifyOnDone  bool              `json:"notifyOnDone"`
	Notifications bool              `json:"notifications"`
	SpeechModel   string            `json:"speechModel"` // path to a whisper.cpp model, "" for none
	LastDirs      map[string]string `json:"lastDirs"`
	RecentFiles   []string          `json:"recentFiles"`
	ToolDefaults  map[string]string `json:"toolDefaults"`
	WindowWidth   int               `json:"windowWidth"`
	WindowHeight  int               `json:"windowHeight"`
}

// Defaults returns the settings applied on a first run.
func Defaults() Settings {
	return Settings{
		Theme:           "slate",
		Accent:          "lavender",
		BackendID:       "",
		DeviceIndex:     0,
		Quality:         20,
		Preset:          "slow",
		AudioBitrateKb:  320,
		OutputMode:      "alongside",
		OutputDir:       "",
		OverwritePolicy: "suffix",
		Concurrency:     2,
		KeepMetadata:    true,
		NotifyOnDone:    true,
		LastDirs:        map[string]string{},
		RecentFiles:     []string{},
		ToolDefaults:    map[string]string{},
		WindowWidth:     1475,
		WindowHeight:    975,
	}
}

// The default window size before 2026-10-20, bumped on load
// The app never saves the window size itself, so this exact pair can only be that default
const oldDefaultWidth, oldDefaultHeight = 1180, 780

// Store owns the on-disk settings file and serialises access to it.
type Store struct {
	mu      sync.RWMutex
	path    string
	current Settings
}

// NewStore loads settings from the per-user config directory, falling back to
// defaults when the file is absent or unreadable.
func NewStore(appName string) (*Store, error) {
	if appName == "" {
		return nil, errors.New("settings: empty application name")
	}
	path, err := configPath(appName)
	if err != nil {
		return nil, err
	}
	return openStore(path), nil
}

// openStore loads the settings file at path, falling back to defaults when it
// is absent or unreadable. Kept separate from NewStore so tests can point it
// at a temporary file instead of the real per-user config directory.
func openStore(path string) *Store {
	store := &Store{path: path, current: Defaults()}
	loaded, err := readFile(path)
	if err == nil {
		store.current = merge(Defaults(), loaded)
	}
	return store
}

// configPath resolves the settings file location for appName.
func configPath(appName string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		base, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("settings: no writable config location: %w", err)
		}
	}
	dir := filepath.Join(base, appName)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("settings: create %s: %w", dir, err)
	}
	return filepath.Join(dir, "settings.json"), nil
}

// readFile decodes the settings file, rejecting oversized input.
func readFile(path string) (Settings, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Settings{}, err
	}
	if info.Size() > maxSettingsSize {
		return Settings{}, errors.New("settings: file exceeds size limit")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return Settings{}, fmt.Errorf("settings: malformed json: %w", err)
	}
	return s, nil
}

// Get returns a copy of the current settings.
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Save validates, stores and persists the supplied settings.
func (s *Store) Save(next Settings) (Settings, error) {
	sanitized := sanitize(next)

	s.mu.Lock()
	s.current = sanitized
	path := s.path
	s.mu.Unlock()

	if err := writeAtomic(path, sanitized); err != nil {
		return sanitized, err
	}
	return sanitized, nil
}

// writeAtomic persists settings via a temp file and rename, so a crash mid-write
// cannot leave a truncated settings file behind.
func writeAtomic(path string, s Settings) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: encode: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, fileMode); err != nil {
		return fmt.Errorf("settings: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best effort cleanup; the write already failed and is reported below.
		_ = os.Remove(tmp)
		return fmt.Errorf("settings: replace %s: %w", path, err)
	}
	return nil
}

// RememberDir records the last-used directory for a given picker key.
func (s *Store) RememberDir(key, dir string) {
	if key == "" || dir == "" {
		return
	}
	s.mu.Lock()
	if s.current.LastDirs == nil {
		s.current.LastDirs = map[string]string{}
	}
	s.current.LastDirs[key] = dir
	snapshot := s.current
	path := s.path
	s.mu.Unlock()

	// A failed convenience write must not interrupt the user's work.
	_ = writeAtomic(path, snapshot)
}

// RememberSelection records a picker's directory and the chosen file together,
// so one selection costs one write rather than two.
func (s *Store) RememberSelection(key, dir, file string) {
	if key == "" || dir == "" || file == "" {
		return
	}
	s.mu.Lock()
	if s.current.LastDirs == nil {
		s.current.LastDirs = map[string]string{}
	}
	s.current.LastDirs[key] = dir
	s.current.RecentFiles = pushBounded(s.current.RecentFiles, file, MaxRecentPaths)
	snapshot := sanitize(s.current)
	s.current = snapshot
	path := s.path
	s.mu.Unlock()

	// A failed convenience write must not interrupt the user's work.
	_ = writeAtomic(path, snapshot)
}

// LastDir returns the remembered directory for key, or "" when unset.
func (s *Store) LastDir(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current.LastDirs == nil {
		return ""
	}
	return s.current.LastDirs[key]
}

// AddRecent pushes path onto the bounded most-recently-used list.
func (s *Store) AddRecent(path string) {
	if path == "" {
		return
	}
	s.mu.Lock()
	s.current.RecentFiles = pushBounded(s.current.RecentFiles, path, MaxRecentPaths)
	snapshot := s.current
	file := s.path
	s.mu.Unlock()

	_ = writeAtomic(file, snapshot)
}

// pushBounded prepends value, removes duplicates and caps the list length.
func pushBounded(list []string, value string, limit int) []string {
	out := make([]string, 0, limit)
	out = append(out, value)
	for i := 0; i < len(list) && len(out) < limit; i++ {
		if list[i] != value {
			out = append(out, list[i])
		}
	}
	return out
}

// Path reports where settings are persisted, for display in the UI.
func (s *Store) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

// merge fills zero-valued fields of loaded with the corresponding defaults.
func merge(base, loaded Settings) Settings {
	out := loaded
	if out.Theme == "" {
		out.Theme = base.Theme
	}
	if out.Accent == "" {
		out.Accent = base.Accent
	}
	if out.Preset == "" {
		out.Preset = base.Preset
	}
	if out.OutputMode == "" {
		out.OutputMode = base.OutputMode
	}
	if out.OverwritePolicy == "" {
		out.OverwritePolicy = base.OverwritePolicy
	}
	if out.Quality == 0 {
		out.Quality = base.Quality
	}
	if out.AudioBitrateKb == 0 {
		out.AudioBitrateKb = base.AudioBitrateKb
	}
	if out.Concurrency == 0 {
		out.Concurrency = base.Concurrency
	}
	if out.WindowWidth == 0 {
		out.WindowWidth = base.WindowWidth
	}
	if out.WindowHeight == 0 {
		out.WindowHeight = base.WindowHeight
	}
	if out.WindowWidth == oldDefaultWidth && out.WindowHeight == oldDefaultHeight {
		out.WindowWidth, out.WindowHeight = base.WindowWidth, base.WindowHeight
	}
	if out.LastDirs == nil {
		out.LastDirs = map[string]string{}
	}
	if out.ToolDefaults == nil {
		out.ToolDefaults = map[string]string{}
	}
	if out.RecentFiles == nil {
		out.RecentFiles = []string{}
	}
	return sanitize(out)
}

// sanitize clamps every field to its documented range.
func sanitize(s Settings) Settings {
	s.Quality = clamp(s.Quality, 0, 51)
	s.AudioBitrateKb = clamp(s.AudioBitrateKb, 32, 512)
	s.Concurrency = clamp(s.Concurrency, 1, 8)
	s.DeviceIndex = clamp(s.DeviceIndex, -1, 16)
	s.WindowWidth = clamp(s.WindowWidth, 900, 5120)
	s.WindowHeight = clamp(s.WindowHeight, 600, 2880)

	if len(s.RecentFiles) > MaxRecentPaths {
		s.RecentFiles = s.RecentFiles[:MaxRecentPaths]
	}
	if len(s.ToolDefaults) > MaxToolDefaults {
		s.ToolDefaults = trimMap(s.ToolDefaults, MaxToolDefaults)
	}
	if len(s.LastDirs) > MaxLastDirs {
		s.LastDirs = trimMap(s.LastDirs, MaxLastDirs)
	}
	if s.OutputMode != "fixed" {
		s.OutputMode = "alongside"
	}
	return s
}

// trimMap returns at most limit entries from m, keeping the lowest keys in
// sorted order. Go randomises map iteration, so taking whichever entries
// came first would drop a different set on every save.
func trimMap(m map[string]string, limit int) map[string]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
