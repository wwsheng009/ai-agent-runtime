package push

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Subscription is a browser Web Push subscription.
type Subscription struct {
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Store keeps push subscriptions, persisted as JSON at path.
type Store struct {
	mu   sync.Mutex
	path string
	subs map[string]Subscription
}

// OpenStore loads subscriptions from path, creating an empty store if absent.
// path "" is memory-only (tests).
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, subs: make(map[string]Subscription)}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var list []Subscription
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, sub := range list {
		if sub.Endpoint != "" {
			s.subs[sub.Endpoint] = sub
		}
	}
	return s, nil
}

// Add upserts a subscription and persists.
func (s *Store) Add(sub Subscription) error {
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub.Endpoint] = sub
	return s.persistLocked()
}

// Remove deletes a subscription by endpoint, reporting whether it existed.
func (s *Store) Remove(endpoint string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[endpoint]; !ok {
		return false
	}
	delete(s.subs, endpoint)
	_ = s.persistLocked()
	return true
}

// List returns subscriptions ordered by creation time (newest first).
func (s *Store) List() []Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Count returns the number of stored subscriptions.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if dir := filepath.Dir(s.path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	list := make([]Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		list = append(list, sub)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
