package mcpmount

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

// TokenPrefix is the human-recognisable prefix of a mount token. It is distinct
// from the device token prefix ("agw_") so a leaked string can be classified
// from the value alone.
const TokenPrefix = "amt_"

// Token is the public (non-secret) projection of a mount token. The plaintext
// is returned exactly once by Create and never stored.
type Token struct {
	ID string `json:"id"`
	// Name identifies the calling party (for example "laptop-ci").
	Name string `json:"name,omitempty"`
	// Prefix is a short fingerprint for display/diagnostics, never enough to
	// reconstruct the secret.
	Prefix string `json:"prefix"`
	// Mounts is the exact set of slugs this token may call. It is never a
	// wildcard: one compromised caller must not reach every mount.
	Mounts    []string  `json:"mounts"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Revoked   bool      `json:"revoked"`
}

// Allows reports whether the token is scoped to slug.
func (t Token) Allows(slug string) bool {
	for _, allowed := range t.Mounts {
		if allowed == slug {
			return true
		}
	}
	return false
}

type tokenRecord struct {
	Token
	hash string
}

// TokenStore holds mount tokens keyed by the SHA-256 hash of the full value.
type TokenStore struct {
	mu     sync.RWMutex
	byHash map[string]*tokenRecord
	byID   map[string]*tokenRecord
	now    func() time.Time
}

// NewTokenStore creates an empty token store.
func NewTokenStore() *TokenStore {
	return &TokenStore{
		byHash: make(map[string]*tokenRecord),
		byID:   make(map[string]*tokenRecord),
		now:    time.Now,
	}
}

// Create issues a mount token scoped to mounts. ttl <= 0 means "no expiry"
// (callers should normally pass a finite TTL). The plaintext is returned once.
func (s *TokenStore) Create(name string, mounts []string, ttl time.Duration) (string, Token, error) {
	scoped, err := normalizeScope(mounts)
	if err != nil {
		return "", Token{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, err
	}
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)

	now := s.now().UTC()
	rec := &tokenRecord{
		Token: Token{
			ID:        hex.EncodeToString(idBytes),
			Name:      strings.TrimSpace(name),
			Prefix:    tokenPrefix(token),
			Mounts:    scoped,
			CreatedAt: now,
		},
		hash: hashToken(token),
	}
	if ttl > 0 {
		rec.ExpiresAt = now.Add(ttl)
	}

	s.mu.Lock()
	s.byHash[rec.hash] = rec
	s.byID[rec.ID] = rec
	s.mu.Unlock()
	return token, rec.Token, nil
}

// Authenticate verifies a token, refreshes LastUsed and rejects revoked or
// expired values.
func (s *TokenStore) Authenticate(token string) (Token, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Token{}, false
	}
	hash := hashToken(token)
	now := s.now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byHash[hash]
	if !ok || rec.Revoked {
		return Token{}, false
	}
	if !rec.ExpiresAt.IsZero() && now.After(rec.ExpiresAt) {
		return Token{}, false
	}
	rec.LastUsed = now
	return copyToken(rec.Token), true
}

// Revoke marks a token revoked. Revocation takes effect on the next request;
// already in-flight streams are not interrupted (they are bounded by the mount
// timeout).
func (s *TokenStore) Revoke(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[strings.TrimSpace(id)]
	if !ok {
		return false
	}
	rec.Revoked = true
	return true
}

// Delete removes a token record entirely. Only revoked tokens can be deleted:
// deleting a live token would silently un-authenticate a caller with no audit
// trail of a revocation, so the flow is always revoke first, then delete.
func (s *TokenStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[strings.TrimSpace(id)]
	if !ok || !rec.Revoked {
		return false
	}
	delete(s.byID, rec.ID)
	delete(s.byHash, rec.hash)
	return true
}

// List returns every token projection, sorted by id for stable output.
func (s *TokenStore) List() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Token, 0, len(s.byID))
	for _, rec := range s.byID {
		out = append(out, copyToken(rec.Token))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns one token projection by id. Revoked and expired records are
// included: the management API needs them to decide whether a delete may purge
// the record.
func (s *TokenStore) Get(id string) (Token, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.byID[strings.TrimSpace(id)]
	if !ok {
		return Token{}, false
	}
	return copyToken(rec.Token), true
}

// ListForMount returns every token scoped to slug, newest first. Revoked and
// expired records stay visible so the console can show history and offer the
// revoke-then-purge flow; the mount card's live count uses CountForMount.
func (s *TokenStore) ListForMount(slug string) []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Token, 0)
	for _, rec := range s.byID {
		if rec.Token.Allows(slug) {
			out = append(out, copyToken(rec.Token))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Count returns the number of token records, including revoked ones.
func (s *TokenStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// CountForMount returns how many live (not revoked, not expired) tokens are
// scoped to slug.
func (s *TokenStore) CountForMount(slug string) int {
	now := s.now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, rec := range s.byID {
		if rec.Revoked || (!rec.ExpiresAt.IsZero() && now.After(rec.ExpiresAt)) {
			continue
		}
		if rec.Token.Allows(slug) {
			count++
		}
	}
	return count
}

func copyToken(token Token) Token {
	out := token
	out.Mounts = append([]string(nil), token.Mounts...)
	return out
}

func normalizeScope(mounts []string) ([]string, error) {
	seen := make(map[string]bool, len(mounts))
	out := make([]string, 0, len(mounts))
	for _, raw := range mounts {
		slug := strings.TrimSpace(raw)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("mount token needs at least one mount slug")
	}
	sort.Strings(out)
	return out, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenPrefix keeps enough of the value to tell tokens apart in an operator UI
// without exposing a usable portion of the secret.
func tokenPrefix(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 12 {
		return token
	}
	return token[:12]
}
