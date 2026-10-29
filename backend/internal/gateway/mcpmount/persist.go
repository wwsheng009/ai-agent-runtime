package mcpmount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// File names under the gateway data directory. Mounts and their tokens are
// separate files on purpose: the mount table is rebuildable routing state while
// the token table is credential state that cannot be reconstructed.
const (
	RegistryFileName = "mounts.json"
	TokensFileName   = "mount-tokens.json"
)

const fileVersion = 1

// ---------------------------------------------------------------- tokens ----

type tokenFileData struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Prefix    string    `json:"prefix"`
	Mounts    []string  `json:"mounts"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Revoked   bool      `json:"revoked"`
}

type tokenFileFormat struct {
	Version int             `json:"version"`
	Tokens  []tokenFileData `json:"tokens"`
}

// PersistentTokenStore is a TokenStore backed by a JSON file. Writes are atomic
// (temp file + rename) and best-effort: a persistence failure never fails the
// in-memory operation, matching the device token store.
type PersistentTokenStore struct {
	*TokenStore
	path string
}

// OpenTokenStore loads (or creates) a persistent mount token store at path.
func OpenTokenStore(path string) (*PersistentTokenStore, error) {
	store := NewTokenStore()
	persistent := &PersistentTokenStore{TokenStore: store, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistent, nil
		}
		return nil, err
	}
	var file tokenFileFormat
	if json.Unmarshal(data, &file) == nil {
		store.load(file.Tokens)
	}
	return persistent, nil
}

// Create issues a token and persists the store.
func (p *PersistentTokenStore) Create(name string, mounts []string, ttl time.Duration) (string, Token, error) {
	token, info, err := p.TokenStore.Create(name, mounts, ttl)
	if err != nil {
		return "", Token{}, err
	}
	p.save()
	return token, info, nil
}

// Revoke revokes a token and persists the store.
func (p *PersistentTokenStore) Revoke(id string) bool {
	ok := p.TokenStore.Revoke(id)
	if ok {
		p.save()
	}
	return ok
}

// Delete removes a revoked token and persists the store.
func (p *PersistentTokenStore) Delete(id string) bool {
	ok := p.TokenStore.Delete(id)
	if ok {
		p.save()
	}
	return ok
}

func (p *PersistentTokenStore) save() {
	if p == nil || p.path == "" {
		return
	}
	data, err := json.MarshalIndent(tokenFileFormat{Version: fileVersion, Tokens: p.TokenStore.snapshot()}, "", "  ")
	if err != nil {
		return
	}
	writeFileAtomic(p.path, data)
}

func (s *TokenStore) snapshot() []tokenFileData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]tokenFileData, 0, len(s.byID))
	for _, rec := range s.byID {
		out = append(out, tokenFileData{
			ID:        rec.ID,
			Name:      rec.Name,
			Prefix:    rec.Prefix,
			Mounts:    append([]string(nil), rec.Mounts...),
			Hash:      rec.hash,
			CreatedAt: rec.CreatedAt,
			LastUsed:  rec.LastUsed,
			ExpiresAt: rec.ExpiresAt,
			Revoked:   rec.Revoked,
		})
	}
	return out
}

func (s *TokenStore) load(records []tokenFileData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, data := range records {
		if data.ID == "" || data.Hash == "" {
			continue
		}
		rec := &tokenRecord{
			Token: Token{
				ID:        data.ID,
				Name:      data.Name,
				Prefix:    data.Prefix,
				Mounts:    append([]string(nil), data.Mounts...),
				CreatedAt: data.CreatedAt,
				LastUsed:  data.LastUsed,
				ExpiresAt: data.ExpiresAt,
				Revoked:   data.Revoked,
			},
			hash: data.Hash,
		}
		s.byHash[data.Hash] = rec
		s.byID[data.ID] = rec
	}
}

// -------------------------------------------------------------- registry ----

type registryFileFormat struct {
	Version int       `json:"version"`
	Mounts  []*Record `json:"mounts"`
}

// PersistentRegistry is a Registry backed by a JSON file. Saving is explicit
// (callers debounce it, like the tunnel registry snapshot) because Heartbeats
// are free to touch the registry without paying for a disk write.
type PersistentRegistry struct {
	*Registry
	path string
}

// OpenRegistry loads (or creates) a persistent mount registry at path. Restored
// mounts are marked stale: the URLs stay reserved, but nothing is served until
// the owning node reconnects.
func OpenRegistry(path string) (*PersistentRegistry, error) {
	registry := NewRegistry()
	persistent := &PersistentRegistry{Registry: registry, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistent, nil
		}
		return nil, err
	}
	var file registryFileFormat
	if json.Unmarshal(data, &file) == nil {
		registry.Restore(file.Mounts)
	}
	return persistent, nil
}

// Save writes the current snapshot. Failures are best-effort for the same reason
// as the tunnel snapshot: the table is rebuildable from REGISTER.
func (p *PersistentRegistry) Save() error {
	if p == nil || p.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(registryFileFormat{Version: fileVersion, Mounts: p.Registry.List()}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p.path, data)
}

// writeFileAtomic writes a 0600 file via temp+rename so a crash mid-write cannot
// leave a truncated credential file behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
