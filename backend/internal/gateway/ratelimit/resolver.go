package ratelimit

import (
	"net"
	"net/http"
	"strings"
)

// Resolver resolves the effective client IP. X-Forwarded-For / X-Real-IP are
// honored only when the request comes from a trusted proxy, so an untrusted
// client cannot spoof its IP to bypass rate limiting or bans
// (docs/gateway/09-edge-hardening.md §4).
type Resolver struct {
	trusted []*net.IPNet
}

// NewResolver builds a resolver from trusted proxy CIDRs or bare IPs. Invalid
// entries are ignored.
func NewResolver(trusted []string) *Resolver {
	resolver := &Resolver{}
	for _, entry := range trusted {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				resolver.trusted = append(resolver.trusted, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
				continue
			}
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			resolver.trusted = append(resolver.trusted, network)
		}
	}
	return resolver
}

func (r *Resolver) trustedRemote(ip net.IP) bool {
	if r == nil || ip == nil {
		return false
	}
	for _, network := range r.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the effective client IP: the socket peer unless it is a
// trusted proxy and a valid forwarded header is present.
func (r *Resolver) ClientIP(req *http.Request) string {
	if req == nil {
		return ""
	}
	remoteHost, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		remoteHost = req.RemoteAddr
	}
	remoteIP := net.ParseIP(remoteHost)
	if !r.trustedRemote(remoteIP) {
		return remoteHost
	}
	if xff := strings.TrimSpace(req.Header.Get("X-Forwarded-For")); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if net.ParseIP(first) != nil {
			return first
		}
	}
	if xri := strings.TrimSpace(req.Header.Get("X-Real-IP")); xri != "" {
		if net.ParseIP(xri) != nil {
			return xri
		}
	}
	return remoteHost
}
