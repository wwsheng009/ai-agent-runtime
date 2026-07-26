package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// StateFileName is the trust/enable registry under the plugins home.
const StateFileName = "state.json"

// PluginState is durable local trust + enable for one installed plugin id.
type PluginState struct {
	ID        string     `json:"id"`
	Path      string     `json:"path,omitempty"`
	Trust     TrustLevel `json:"trust"`
	Enabled   bool       `json:"enabled"`
	UpdatedAt time.Time  `json:"updated_at"`
	Note      string     `json:"note,omitempty"`
}

// StateStore persists local plugin trust marks (no marketplace).
type StateStore struct {
	mu   sync.Mutex
	path string
}

// DefaultStatePath returns ~/.aicli/plugins/state.json (or AICLI_HOME override).
func DefaultStatePath() string {
	return filepath.Join(DefaultPluginsHome(), StateFileName)
}

// DefaultPluginsHome returns the user plugins install root.
// AICLI_HOME/plugins when set; otherwise ~/.aicli/plugins.
func DefaultPluginsHome() string {
	if home := strings.TrimSpace(os.Getenv("AICLI_HOME")); home != "" {
		return filepath.Join(aiclipaths.ExpandUserPath(home), "plugins")
	}
	return aiclipaths.ExpandUserPath(filepath.Join("~", ".aicli", "plugins"))
}

// NewStateStore opens (or creates) a trust state store at path.
// Empty path uses DefaultStatePath().
func NewStateStore(path string) *StateStore {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultStatePath()
	}
	return &StateStore{path: filepath.Clean(path)}
}

// Path returns the state file path.
func (s *StateStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

type stateFile struct {
	Version int                    `json:"version"`
	Plugins map[string]PluginState `json:"plugins"`
}

func (s *StateStore) loadLocked() (*stateFile, error) {
	if s == nil {
		return nil, fmt.Errorf("plugin state store is nil")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &stateFile{Version: 1, Plugins: map[string]PluginState{}}, nil
		}
		return nil, err
	}
	var file stateFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse plugin state: %w", err)
	}
	if file.Plugins == nil {
		file.Plugins = map[string]PluginState{}
	}
	if file.Version == 0 {
		file.Version = 1
	}
	return &file, nil
}

func (s *StateStore) saveLocked(file *stateFile) error {
	if s == nil {
		return fmt.Errorf("plugin state store is nil")
	}
	if file == nil {
		file = &stateFile{Version: 1, Plugins: map[string]PluginState{}}
	}
	if file.Plugins == nil {
		file.Plugins = map[string]PluginState{}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		// Windows may fail rename over existing; remove then rename.
		_ = os.Remove(s.path)
		if err2 := os.Rename(tmp, s.path); err2 != nil {
			return err2
		}
	}
	return nil
}

// Get returns state for id if present.
func (s *StateStore) Get(id string) (PluginState, bool, error) {
	id = normalizePluginID(id)
	if id == "" {
		return PluginState{}, false, fmt.Errorf("plugin id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return PluginState{}, false, err
	}
	st, ok := file.Plugins[id]
	return st, ok, nil
}

// List returns all stored plugin states sorted by id.
func (s *StateStore) List() ([]PluginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	out := make([]PluginState, 0, len(file.Plugins))
	for _, st := range file.Plugins {
		out = append(out, st)
	}
	sortPluginStates(out)
	return out, nil
}

// SetTrust updates trust for id (creates entry if missing).
func (s *StateStore) SetTrust(id string, trust TrustLevel, path string) (PluginState, error) {
	id = normalizePluginID(id)
	if id == "" {
		return PluginState{}, fmt.Errorf("plugin id is required")
	}
	trust = normalizeTrust(trust)
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return PluginState{}, err
	}
	st := file.Plugins[id]
	st.ID = id
	st.Trust = trust
	if p := strings.TrimSpace(path); p != "" {
		st.Path = filepath.Clean(p)
	}
	if st.UpdatedAt.IsZero() && !st.Enabled && st.Trust == TrustUntrusted {
		// first touch: default enabled true once trusted later; keep enabled true for usability
		st.Enabled = true
	}
	if _, exists := file.Plugins[id]; !exists {
		st.Enabled = true
	}
	st.UpdatedAt = time.Now().UTC()
	file.Plugins[id] = st
	if err := s.saveLocked(file); err != nil {
		return PluginState{}, err
	}
	return st, nil
}

// SetEnabled updates the enable flag for id.
func (s *StateStore) SetEnabled(id string, enabled bool, path string) (PluginState, error) {
	id = normalizePluginID(id)
	if id == "" {
		return PluginState{}, fmt.Errorf("plugin id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return PluginState{}, err
	}
	st, ok := file.Plugins[id]
	if !ok {
		st = PluginState{ID: id, Trust: TrustUntrusted, Enabled: enabled}
	}
	st.ID = id
	st.Enabled = enabled
	if p := strings.TrimSpace(path); p != "" {
		st.Path = filepath.Clean(p)
	}
	st.UpdatedAt = time.Now().UTC()
	file.Plugins[id] = st
	if err := s.saveLocked(file); err != nil {
		return PluginState{}, err
	}
	return st, nil
}

// UpsertInstall records an installed plugin path (defaults untrusted, enabled).
func (s *StateStore) UpsertInstall(id, path string, trust TrustLevel) (PluginState, error) {
	id = normalizePluginID(id)
	if id == "" {
		return PluginState{}, fmt.Errorf("plugin id is required")
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return PluginState{}, fmt.Errorf("plugin path is required")
	}
	trust = normalizeTrust(trust)
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return PluginState{}, err
	}
	st, ok := file.Plugins[id]
	if !ok {
		st = PluginState{
			ID:      id,
			Enabled: true,
			Trust:   trust,
		}
	}
	st.ID = id
	st.Path = path
	if trust != "" {
		st.Trust = trust
	}
	if st.Trust == "" {
		st.Trust = TrustUntrusted
	}
	st.UpdatedAt = time.Now().UTC()
	file.Plugins[id] = st
	if err := s.saveLocked(file); err != nil {
		return PluginState{}, err
	}
	return st, nil
}

// Remove deletes state for id.
func (s *StateStore) Remove(id string) error {
	id = normalizePluginID(id)
	if id == "" {
		return fmt.Errorf("plugin id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return err
	}
	delete(file.Plugins, id)
	return s.saveLocked(file)
}

// ApplyToPackage overlays durable trust/enable onto a loaded package.
func (s *StateStore) ApplyToPackage(pkg *Package) error {
	if pkg == nil {
		return fmt.Errorf("plugin package is nil")
	}
	id := normalizePluginID(pkg.Manifest.Name)
	st, ok, err := s.Get(id)
	if err != nil {
		return err
	}
	if !ok {
		// no state: keep package defaults (untrusted unless LoadOptions set trust)
		return nil
	}
	pkg.Trust = normalizeTrust(st.Trust)
	pkg.Enabled = st.Enabled && pkg.Manifest.IsEnabled()
	return nil
}

func normalizePluginID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func sortPluginStates(states []PluginState) {
	for i := 0; i < len(states); i++ {
		for j := i + 1; j < len(states); j++ {
			if states[j].ID < states[i].ID {
				states[i], states[j] = states[j], states[i]
			}
		}
	}
}
