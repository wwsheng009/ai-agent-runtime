package ratelimit

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

// BanEntry describes an active ban.
type BanEntry struct {
	IP    string    `json:"ip"`
	Until time.Time `json:"until"`
}

// BanList is an in-memory penalty ban list: repeated low-false-positive signals
// (route denials, auth failures) within a window auto-ban the source IP for a
// TTL. It never bans an allowlisted key (the caller simply does not record it).
//
// Design: docs/gateway/09-edge-hardening.md §5.2.
type BanList struct {
	mu        sync.Mutex
	threshold int
	window    time.Duration
	banTTL    time.Duration
	now       func() time.Time
	hits      map[string][]time.Time
	bans      map[string]time.Time
}

// NewBanList creates a ban list. A non-positive threshold disables auto-ban.
func NewBanList(threshold int, window, banTTL time.Duration) *BanList {
	if window <= 0 {
		window = time.Minute
	}
	if banTTL <= 0 {
		banTTL = 10 * time.Minute
	}
	return &BanList{
		threshold: threshold,
		window:    window,
		banTTL:    banTTL,
		now:       time.Now,
		hits:      make(map[string][]time.Time),
		bans:      make(map[string]time.Time),
	}
}

// Enabled reports whether auto-ban is on.
func (b *BanList) Enabled() bool { return b != nil && b.threshold > 0 }

// Record notes a penalty for ip. It returns true when ip is banned as a result.
func (b *BanList) Record(ip string) bool {
	if !b.Enabled() || ip == "" {
		return false
	}
	now := b.now()
	cutoff := now.Add(-b.window)
	b.mu.Lock()
	defer b.mu.Unlock()
	if until, ok := b.bans[ip]; ok && now.Before(until) {
		return true
	}
	kept := b.hits[ip][:0]
	for _, t := range b.hits[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	b.hits[ip] = kept
	if len(kept) >= b.threshold {
		b.bans[ip] = now.Add(b.banTTL)
		delete(b.hits, ip)
		return true
	}
	return false
}

// IsBanned reports whether ip is currently banned.
func (b *BanList) IsBanned(ip string) bool {
	if b == nil || ip == "" {
		return false
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.bans[ip]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(b.bans, ip)
		return false
	}
	return true
}

// Ban manually bans ip for ttl (ttl <= 0 uses the configured ban TTL).
func (b *BanList) Ban(ip string, ttl time.Duration) {
	if b == nil || ip == "" {
		return
	}
	if ttl <= 0 {
		ttl = b.banTTL
	}
	b.mu.Lock()
	b.bans[ip] = b.now().Add(ttl)
	b.mu.Unlock()
}

// Unban removes a ban. It reports whether a ban existed.
func (b *BanList) Unban(ip string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.bans[ip]
	delete(b.bans, ip)
	delete(b.hits, ip)
	return ok
}

// Entries returns active bans, sorted by IP.
func (b *BanList) Entries() []BanEntry {
	if b == nil {
		return nil
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]BanEntry, 0, len(b.bans))
	for ip, until := range b.bans {
		if now.After(until) {
			delete(b.bans, ip)
			continue
		}
		out = append(out, BanEntry{IP: ip, Until: until})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// Middleware rejects banned clients with 403.
func (b *BanList) Middleware(keyFn func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ""
		if keyFn != nil {
			key = keyFn(r)
		}
		if b.IsBanned(key) {
			http.Error(w, "banned", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
