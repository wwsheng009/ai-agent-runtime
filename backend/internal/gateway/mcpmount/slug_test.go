package mcpmount

import (
	"strings"
	"testing"
)

func TestDeriveSlugIsStableAndShaped(t *testing.T) {
	first := DeriveSlug("node-identity", "prod-readonly")
	if first != DeriveSlug("node-identity", "prod-readonly") {
		t.Fatalf("DeriveSlug is not deterministic")
	}
	if got := len(first); got != SlugLength {
		t.Fatalf("DeriveSlug length = %d, want %d (%q)", got, SlugLength, first)
	}
	for _, r := range first {
		if (r < '0' || r > '9') && (r < 'a' || r > 'v') {
			t.Fatalf("DeriveSlug %q contains %q outside [0-9a-v]", first, r)
		}
	}
	if first == DeriveSlug("node-identity", "prod-standard") {
		t.Fatalf("different mount ids produced the same slug %q", first)
	}
	if first == DeriveSlug("other-identity", "prod-readonly") {
		t.Fatalf("different identities produced the same slug %q", first)
	}
	// Whitespace is not part of the identity.
	if first != DeriveSlug("  node-identity  ", " prod-readonly ") {
		t.Fatalf("DeriveSlug must trim its inputs")
	}
}

func TestValidateSlug(t *testing.T) {
	valid := []string{"k7f3qz2a", "team-api", "a0", "z9z9z9z9", strings.Repeat("a", 32)}
	for _, slug := range valid {
		if err := ValidateSlug(slug); err != nil {
			t.Fatalf("ValidateSlug(%q) = %v, want nil", slug, err)
		}
	}
	invalid := []string{
		"",
		"a",    // too short
		"-abc", // leading dash
		"Abc",  // uppercase
		"ab_c", // underscore
		"a b",  // space
		"api",  // reserved namespace
		"gw",   // reserved namespace
		"m",    // reserved namespace
		"index.html",
		"sw.js",
		strings.Repeat("a", 33), // too long
	}
	for _, slug := range invalid {
		if err := ValidateSlug(slug); err == nil {
			t.Fatalf("ValidateSlug(%q) = nil, want error", slug)
		}
	}
}

func TestNormalizeLocalPath(t *testing.T) {
	cases := map[string]string{
		"/mcp":     "/mcp",
		"mcp":      "/mcp",
		"/a//b":    "/a/b",
		"/a/./b":   "/a/b",
		"/foo/mcp": "/foo/mcp",
	}
	for input, want := range cases {
		got, err := NormalizeLocalPath(input)
		if err != nil {
			t.Fatalf("NormalizeLocalPath(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeLocalPath(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "/mcp?x=1", "/mcp#frag", "/a/../b", "../mcp"} {
		if _, err := NormalizeLocalPath(input); err == nil {
			t.Fatalf("NormalizeLocalPath(%q) = nil error, want rejection", input)
		}
	}
}

func TestPublicPathAndURL(t *testing.T) {
	if got, err := PublicPath(PathStyleNamespaced, "k7f3qz2a", "/mcp"); err != nil || got != "/m/k7f3qz2a/mcp" {
		t.Fatalf("PublicPath namespaced = %q, %v", got, err)
	}
	if got, err := PublicPath(PathStyleRoot, "k7f3qz2a", "/mcp"); err != nil || got != "/k7f3qz2a/mcp" {
		t.Fatalf("PublicPath root = %q, %v", got, err)
	}
	if got, err := PublicPath("", "k7f3qz2a", "mcp"); err != nil || got != "/m/k7f3qz2a/mcp" {
		t.Fatalf("PublicPath default style = %q, %v", got, err)
	}
	if _, err := PublicPath(PathStyleNamespaced, "api", "/mcp"); err == nil {
		t.Fatalf("PublicPath accepted a reserved slug")
	}
	if got := PublicURL("https://gw.example.com/", PathStyleNamespaced, "k7f3qz2a", "/mcp"); got != "https://gw.example.com/m/k7f3qz2a/mcp" {
		t.Fatalf("PublicURL = %q", got)
	}
	if got := PublicURL("", PathStyleNamespaced, "k7f3qz2a", "/mcp"); got != "" {
		t.Fatalf("PublicURL without a base = %q, want empty", got)
	}
}
