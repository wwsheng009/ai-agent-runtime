// Package mcpmount implements the gateway side of the MCP mount namespace: the
// slug derivation rules, the mount registry and the mount-scoped bearer token
// store (docs/gateway/16-mcp-mount-tunnel.md).
//
// A mount is one locally hosted MCP endpoint published through the reverse
// tunnel. The public path is /m/{slug}/{local-path} (or /{slug}/{local-path}
// with the opt-in root style); the node keeps listening on loopback only and
// the gateway never learns the node's local MCP credential.
package mcpmount

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	pathpkg "path"
	"regexp"
	"strings"
)

const (
	// SlugLength is the fixed length of a derived public path segment. Five
	// bytes of SHA-256 encode to exactly eight base32 characters.
	SlugLength = 8

	// slugVersion tags the derivation input. Changing the algorithm must also
	// change this tag: reusing it after a change would silently recycle a URL
	// that clients already have in their configuration.
	slugVersion = "aicli-mount-v1"

	// PathStyleNamespaced serves mounts under /m/{slug}/..., which cannot
	// collide with the console, /api, /gw, /n, /s, /hooks or /metrics.
	PathStyleNamespaced = "m"
	// PathStyleRoot serves mounts under /{slug}/.... Opt-in: the slug then
	// shares the root namespace with the console's static files (see
	// ValidateSlug).
	PathStyleRoot = "root"
)

// slugPattern is deliberately narrower than "anything URL-safe": operators read
// these paths out loud, paste them into issue reports and use them as log keys.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,31}$`)

// reservedRootSegments are first path segments that already belong to the
// gateway itself. A root-style slug must never shadow them.
var reservedRootSegments = map[string]bool{
	"api":     true,
	"gw":      true,
	"n":       true,
	"s":       true,
	"m":       true,
	"hooks":   true,
	"metrics": true,
	"healthz": true,
	"static":  true,
	"console": true,
}

// reservedConsoleFiles are the console's embedded assets. Root-style slugs must
// not collide with them or the SPA fallback would mask the mount.
var reservedConsoleFiles = map[string]bool{
	"index.html":           true,
	"app.js":               true,
	"style.css":            true,
	"sw.js":                true,
	"manifest.webmanifest": true,
	"favicon.ico":          true,
}

// DeriveSlug returns the stable public path segment for one mount.
//
// identity is the node's persisted mount identity (not the process node id,
// which is deliberately regenerated on every start), and mountID is the mount's
// id within that node. The same pair always yields the same slug, so a restart,
// an upgrade or a gateway failover never changes a client's URL.
func DeriveSlug(identity, mountID string) string {
	sum := sha256.Sum256([]byte(slugVersion + "\x00" + strings.TrimSpace(identity) + "\x00" + strings.TrimSpace(mountID)))
	// Base32hex keeps the alphabet ordered and unambiguous; lowercasing makes
	// the path safe to type. Truncating to five bytes is what fixes the length.
	encoded := base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:5])
	return strings.ToLower(encoded)
}

// ValidateSlug reports whether a slug may be published. It rejects shapes that
// would break routing or shadow the gateway's own endpoints.
func ValidateSlug(slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return fmt.Errorf("mount slug is empty")
	}
	if !LooksLikeSlug(slug) {
		return fmt.Errorf("mount slug %q must match %s (2-32 chars, lowercase letters, digits and '-')", slug, slugPattern)
	}
	if IsReservedRootSegment(slug) {
		return fmt.Errorf("mount slug %q is reserved by the gateway or the console", slug)
	}
	return nil
}

// LooksLikeSlug reports whether value has the slug shape, without checking
// reserved names. The root-style router uses it to decide that a path belongs to
// the mount namespace instead of the console SPA fallback.
func LooksLikeSlug(value string) bool {
	return slugPattern.MatchString(strings.TrimSpace(value))
}

// IsReservedRootSegment reports whether the first path segment is already owned
// by the gateway or by a console asset.
func IsReservedRootSegment(value string) bool {
	value = strings.TrimSpace(value)
	return reservedRootSegments[value] || reservedConsoleFiles[value]
}

// NormalizeLocalPath canonicalizes the local path a mount exposes. The path is
// what the node's own server already serves (for example "/mcp"); the gateway
// strips its own namespace prefix before forwarding.
func NormalizeLocalPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("mount local path is empty")
	}
	if strings.ContainsAny(value, "?#") {
		return "", fmt.Errorf("mount local path %q must not contain a query or fragment", value)
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", fmt.Errorf("mount local path %q must not contain '..'", value)
		}
	}
	return pathpkg.Clean(value), nil
}

// PublicPath renders the gateway-visible path for one mount request. localPath
// keeps the node's own path shape, so a mount whose endpoint is "/foo/mcp"
// publishes "/m/{slug}/foo/mcp".
func PublicPath(style, slug, localPath string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	local, err := NormalizeLocalPath(localPath)
	if err != nil {
		return "", err
	}
	if NormalizePathStyle(style) == PathStyleRoot {
		return "/" + slug + local, nil
	}
	return "/m/" + slug + local, nil
}

// PublicURL renders the absolute public endpoint shown in REGISTER_ACK and the
// console. An empty baseURL yields an empty string: the caller decides whether
// that is a configuration error (PublicBaseURL is optional today).
func PublicURL(baseURL, style, slug, localPath string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	path, err := PublicPath(style, slug, localPath)
	if err != nil {
		return ""
	}
	return baseURL + path
}

// NormalizePathStyle clamps unknown values to the safe default.
func NormalizePathStyle(style string) string {
	if strings.EqualFold(strings.TrimSpace(style), PathStyleRoot) {
		return PathStyleRoot
	}
	return PathStyleNamespaced
}

// ReservedRootSegments returns the first path segments a root-style slug must
// not shadow. The console's SPA fallback uses the same list.
func ReservedRootSegments() []string {
	out := make([]string, 0, len(reservedRootSegments))
	for segment := range reservedRootSegments {
		out = append(out, segment)
	}
	return out
}
