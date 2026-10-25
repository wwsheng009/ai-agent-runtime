// Package appdevice stores native app push tokens. It is deliberately separate
// from internal/gateway/device: agw_* tokens authenticate agent tunnels, while
// these records only address notifications for the owner's app installations.
package appdevice

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// TokenMaxLen bounds provider tokens (APNs/FCM/UnifiedPush can be long).
	TokenMaxLen = 4096
	// LabelMaxLen bounds the user-facing device label.
	LabelMaxLen = 128
)

// ErrNotFound is returned when an App device id is unknown.
var ErrNotFound = errors.New("app device not found")

// Device is the persisted App push registration. Token is a capability and is
// never returned by the public API; use Public() for responses.
type Device struct {
	ID        string    `json:"id"`
	Platform  string    `json:"platform"`
	Token     string    `json:"token"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// PublicDevice is the non-secret projection returned to the console.
type PublicDevice struct {
	ID        string    `json:"id"`
	Platform  string    `json:"platform"`
	Label     string    `json:"label,omitempty"`
	TokenHint string    `json:"token_hint"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// Public returns the safe projection of a registration.
func (d Device) Public() PublicDevice {
	return PublicDevice{
		ID:        d.ID,
		Platform:  d.Platform,
		Label:     d.Label,
		TokenHint: tokenHint(d.Token),
		CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt,
		LastSeen:  d.LastSeen,
	}
}

type fileFormat struct {
	Version int      `json:"version"`
	Devices []Device `json:"devices"`
}

const fileVersion = 1

// Store is the in-memory App device registry, optionally backed by a JSON file.
type Store struct {
	mu      sync.Mutex
	byToken map[string]*Device
	byID    map[string]*Device
	path    string
	now     func() time.Time
}

// NewStore creates an empty in-memory store.
func NewStore() *Store {
	return &Store{
		byToken: make(map[string]*Device),
		byID:    make(map[string]*Device),
		now:     time.Now,
	}
}

// Open loads (or creates) a persistent store at path.
func Open(path string) (*Store, error) {
	store := NewStore()
	store.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	var file fileFormat
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("appdevice: decode %s: %w", path, err)
	}
	for _, record := range file.Devices {
		platform, err := normalizePlatform(record.Platform)
		if err != nil || strings.TrimSpace(record.Token) == "" || strings.TrimSpace(record.ID) == "" {
			continue
		}
		record.Platform = platform
		record.Token = strings.TrimSpace(record.Token)
		record.Label = strings.TrimSpace(record.Label)
		if record.CreatedAt.IsZero() {
			record.CreatedAt = store.now().UTC()
		}
		if record.UpdatedAt.IsZero() {
			record.UpdatedAt = record.CreatedAt
		}
		if _, exists := store.byToken[record.Token]; exists {
			continue
		}
		if _, exists := store.byID[record.ID]; exists {
			continue
		}
		saved := record
		store.byToken[saved.Token] = &saved
		store.byID[saved.ID] = &saved
	}
	return store, nil
}

// Register upserts a device by push token. The token identifies the physical
// app installation; re-registering rotates label/platform but keeps its ID.
func (s *Store) Register(platform, token, label string) (PublicDevice, bool, error) {
	platform, err := normalizePlatform(platform)
	if err != nil {
		return PublicDevice{}, false, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return PublicDevice{}, false, errors.New("appdevice: token is required")
	}
	if len(token) > TokenMaxLen {
		return PublicDevice{}, false, fmt.Errorf("appdevice: token exceeds %d bytes", TokenMaxLen)
	}
	label = strings.TrimSpace(label)
	if len(label) > LabelMaxLen {
		return PublicDevice{}, false, fmt.Errorf("appdevice: label exceeds %d bytes", LabelMaxLen)
	}

	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byToken[token]; ok {
		existing.Platform = platform
		existing.Label = label
		existing.UpdatedAt = now
		existing.LastSeen = now
		if err := s.saveLocked(); err != nil {
			return PublicDevice{}, false, err
		}
		return existing.Public(), false, nil
	}

	id, err := newID()
	if err != nil {
		return PublicDevice{}, false, err
	}
	device := &Device{
		ID:        id,
		Platform:  platform,
		Token:     token,
		Label:     label,
		CreatedAt: now,
		UpdatedAt: now,
		LastSeen:  now,
	}
	s.byToken[token] = device
	s.byID[id] = device
	if err := s.saveLocked(); err != nil {
		return PublicDevice{}, false, err
	}
	return device.Public(), true, nil
}

// List returns full records for notification senders. Callers must not expose
// Token through an API; use PublicList for HTTP responses.
func (s *Store) List() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.byID))
	for _, device := range s.byID {
		out = append(out, *device)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PublicList returns the non-secret projection used by the console API.
func (s *Store) PublicList() []PublicDevice {
	devices := s.List()
	out := make([]PublicDevice, 0, len(devices))
	for _, device := range devices {
		out = append(out, device.Public())
	}
	return out
}

// Delete removes a registration by id. It reports whether the id existed.
func (s *Store) Delete(id string) bool {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.byID[id]
	if !ok {
		return false
	}
	delete(s.byID, id)
	delete(s.byToken, device.Token)
	_ = s.saveLocked()
	return true
}

// Count returns the number of registered App devices.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	devices := make([]Device, 0, len(s.byID))
	for _, device := range s.byID {
		devices = append(devices, *device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	data, err := json.MarshalIndent(fileFormat{Version: fileVersion, Devices: devices}, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func normalizePlatform(platform string) (string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	switch platform {
	case "ios", "android", "windows", "macos", "linux":
		return platform, nil
	default:
		return "", fmt.Errorf("appdevice: unsupported platform %q", platform)
	}
}

func tokenHint(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 10 {
		return "…"
	}
	return token[:6] + "…" + token[len(token)-4:]
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
