// Package device implements the gateway's per-device registration tokens: a
// token is issued once (only its SHA-256 hash is retained), verified on tunnel
// upgrade and revocable. The store is in-memory for now.
//
// Design: docs/gateway/04-security.md §2.2.
package device

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// TokenPrefix is the human-recognizable prefix of a device token.
const TokenPrefix = "agw_"

// Token is the public (non-secret) projection of a device token.
type Token struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	Revoked   bool      `json:"revoked"`
}

type record struct {
	Token
	hash string
}

// Store holds device tokens keyed by the SHA-256 hash of the full token.
type Store struct {
	mu     sync.RWMutex
	byHash map[string]*record
	byID   map[string]*record
	now    func() time.Time
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{
		byHash: make(map[string]*record),
		byID:   make(map[string]*record),
		now:    time.Now,
	}
}

// Create issues a new device token. The plaintext is returned exactly once and
// never stored.
func (s *Store) Create(name string) (string, Token, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, err
	}
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)

	rec := &record{
		Token: Token{
			ID:        hex.EncodeToString(idBytes),
			Name:      strings.TrimSpace(name),
			Prefix:    tokenPrefix(token),
			CreatedAt: s.now().UTC(),
		},
		hash: hashToken(token),
	}
	s.mu.Lock()
	s.byHash[rec.hash] = rec
	s.byID[rec.ID] = rec
	s.mu.Unlock()
	return token, rec.Token, nil
}

// Authenticate verifies a token and refreshes its LastUsed. Revoked or unknown
// tokens fail.
func (s *Store) Authenticate(token string) (Token, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Token{}, false
	}
	hash := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byHash[hash]
	if !ok || rec.Revoked {
		return Token{}, false
	}
	rec.LastUsed = s.now().UTC()
	return rec.Token, true
}

// Revoke marks a token revoked. It reports whether the id existed.
func (s *Store) Revoke(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return false
	}
	rec.Revoked = true
	return true
}

// Delete removes a token record entirely. Only revoked tokens can be deleted —
// deleting a live token would silently un-authenticate an agent with no audit
// trail of a revocation, so the flow is always revoke first, then delete.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok || !rec.Revoked {
		return false
	}
	hash := rec.hash
	delete(s.byID, id)
	delete(s.byHash, hash)
	return true
}

// List returns all tokens (never the secret), sorted by id.
func (s *Store) List() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Token, 0, len(s.byID))
	for _, rec := range s.byID {
		out = append(out, rec.Token)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Count returns the number of tokens (including revoked).
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func tokenPrefix(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 8 {
		return token
	}
	return token[:8]
}

// ErrNotFound is returned by Revoke helpers when the id is unknown.
var ErrNotFound = fmt.Errorf("device token not found")
