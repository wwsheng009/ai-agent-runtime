package lsp

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathToURIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	uri := PathToURI(path)
	if !strings.HasPrefix(uri, "file://") {
		t.Fatalf("PathToURI(%q) = %q, want file:// prefix", path, uri)
	}
	if got := URIToPath(uri); !samePath(got, path) {
		t.Fatalf("URIToPath(%q) = %q, want %q", uri, got, path)
	}
}

func TestPathToURIEscapesSpaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "with space", "a b.go")
	uri := PathToURI(path)
	if strings.Contains(uri, " ") {
		t.Fatalf("URI must percent-encode spaces: %q", uri)
	}
	if got := URIToPath(uri); !samePath(got, path) {
		t.Fatalf("URIToPath round trip = %q, want %q", got, path)
	}
}

func TestURIToPathKeepsInvalidInputReadable(t *testing.T) {
	if got := URIToPath("not-a-uri"); got != "not-a-uri" {
		t.Fatalf("URIToPath(invalid) = %q, want passthrough", got)
	}
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
