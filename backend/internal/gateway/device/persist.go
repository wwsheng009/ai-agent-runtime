package device

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// recordData is the on-disk representation of a device token. Only the SHA-256
// hash is stored (the plaintext can never be recovered from it).
type recordData struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Prefix    string    `json:"prefix"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	Revoked   bool      `json:"revoked"`
}

type fileFormat struct {
	Version int          `json:"version"`
	Tokens  []recordData `json:"tokens"`
}

const fileVersion = 1

// PersistentStore is a Store backed by a JSON file. Writes are atomic
// (temp-file + rename) and best-effort: a persistence failure does not fail the
// in-memory operation.
type PersistentStore struct {
	*Store
	path string
}

// Open loads (or creates) a persistent device store at path.
func Open(path string) (*PersistentStore, error) {
	store := NewStore()
	persistent := &PersistentStore{Store: store, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistent, nil
		}
		return nil, err
	}
	var file fileFormat
	if json.Unmarshal(data, &file) == nil {
		store.load(file.Tokens)
	}
	return persistent, nil
}

// Create issues a token and persists the store.
func (p *PersistentStore) Create(name string) (string, Token, error) {
	token, info, err := p.Store.Create(name)
	if err != nil {
		return "", Token{}, err
	}
	p.save()
	return token, info, nil
}

// Revoke revokes a token and persists the store.
func (p *PersistentStore) Revoke(id string) bool {
	ok := p.Store.Revoke(id)
	if ok {
		p.save()
	}
	return ok
}

func (p *PersistentStore) save() {
	if p == nil || p.path == "" {
		return
	}
	data, err := json.MarshalIndent(fileFormat{Version: fileVersion, Tokens: p.Store.snapshot()}, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(p.path)
	if dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p.path)
}

func (s *Store) snapshot() []recordData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]recordData, 0, len(s.byID))
	for _, rec := range s.byID {
		out = append(out, recordData{
			ID:        rec.ID,
			Name:      rec.Name,
			Prefix:    rec.Prefix,
			Hash:      rec.hash,
			CreatedAt: rec.CreatedAt,
			LastUsed:  rec.LastUsed,
			Revoked:   rec.Revoked,
		})
	}
	return out
}

func (s *Store) load(records []recordData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, data := range records {
		if data.Hash == "" || data.ID == "" {
			continue
		}
		rec := &record{
			Token: Token{
				ID:        data.ID,
				Name:      data.Name,
				Prefix:    data.Prefix,
				CreatedAt: data.CreatedAt,
				LastUsed:  data.LastUsed,
				Revoked:   data.Revoked,
			},
			hash: data.Hash,
		}
		s.byHash[data.Hash] = rec
		s.byID[data.ID] = rec
	}
}
