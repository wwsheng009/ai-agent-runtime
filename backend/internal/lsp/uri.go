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
	return (&url.URL{Scheme: "file", Path: slash}).String()
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
