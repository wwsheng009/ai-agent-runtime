package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// PathToURI converts an absolute filesystem path to a file:// URI.
func PathToURI(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	abs := path
	if resolved, err := filepath.Abs(path); err == nil {
		abs = resolved
	}
	slash := filepath.ToSlash(abs)
	if strings.HasPrefix(slash, "//") {
		// UNC path: file://server/share/...
		return (&url.URL{Scheme: "file", Path: slash}).String()
	}
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	return canonicalURI((&url.URL{Scheme: "file", Path: slash}).String())
}

// canonicalURI normalizes a file:// URI to the exact shape PathToURI emits, so
// that document/diagnostic lookups match no matter how a server spells the URI
// (drive-letter case, percent-encoded colon, backslash separators). Servers
// are free to report `file:///c%3A/...` while the client tracks
// `file:///C:/...`; without normalization publishDiagnostics would land under
// a key nobody ever reads.
func canonicalURI(uri string) string {
	parsed, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || !strings.EqualFold(parsed.Scheme, "file") || parsed.Path == "" {
		return uri
	}
	path := parsed.Path
	if runtime.GOOS == "windows" {
		if strings.Contains(path, `\`) {
			path = filepath.ToSlash(path)
		}
		trimmed := strings.TrimPrefix(path, "/")
		if len(trimmed) >= 2 && trimmed[1] == ':' {
			trimmed = strings.ToUpper(trimmed[:1]) + trimmed[1:]
			path = "/" + trimmed
		}
	}
	return (&url.URL{Scheme: "file", Host: parsed.Host, Path: path}).String()
}

// URIToPath converts a file:// URI back to a native filesystem path.
func URIToPath(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return uri
	}
	path := parsed.Path
	if runtime.GOOS == "windows" {
		path = strings.TrimPrefix(path, "/")
		if len(path) >= 2 && path[1] == ':' {
			// drive letter form already fine
		} else if strings.HasPrefix(parsed.Host, "") && parsed.Host != "" {
			path = "//" + parsed.Host + "/" + strings.TrimPrefix(path, "/")
		}
	}
	return filepath.FromSlash(path)
}
