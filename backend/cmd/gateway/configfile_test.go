package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newIsolatedFlagSet rebuilds the gateway flags on a fresh FlagSet so config
// precedence tests never touch process-global state.
func newIsolatedFlagSet() (*flag.FlagSet, *string, *string, *time.Duration, *int, *bool) {
	fs := flag.NewFlagSet("gateway-test", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8090", "")
	password := fs.String("owner-password", "", "")
	ttl := fs.Duration("session-ttl", time.Hour, "")
	count := fs.Int("rate-limit", 0, "")
	push := fs.Bool("push-enabled", true, "")
	return fs, listen, password, ttl, count, push
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFlagConfigFileFullKeys(t *testing.T) {
	path := writeTemp(t, `
listen: ":8443"
owner-password: "secret"
session-ttl: 30m
rate-limit: 300
push-enabled: false
`)
	entries, err := loadFlagConfigFile(path)
	if err != nil {
		t.Fatalf("loadFlagConfigFile: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(entries))
	}
	got := map[string]string{}
	for _, kv := range entries {
		got[kv[0]] = kv[1]
	}
	if got["listen"] != ":8443" || got["owner-password"] != "secret" ||
		got["rate-limit"] != "300" || got["push-enabled"] != "false" {
		t.Fatalf("scalar entries wrong: %v", got)
	}
	if _, err := time.ParseDuration(got["session-ttl"]); err != nil {
		t.Fatalf("duration entry %q does not parse: %v", got["session-ttl"], err)
	}
}

func TestLoadFlagConfigFileRejectsBadContent(t *testing.T) {
	cases := map[string]string{
		"meta key":      "config: x\n",
		"empty value":   "listen: \"\"\n",
		"list value":    "listen: [a, b]\n",
		"malformed doc": "listen: [unclosed\n",
	}
	for name, content := range cases {
		path := writeTemp(t, content)
		if _, err := loadFlagConfigFile(path); err == nil {
			t.Errorf("%s: expected error, got none", name)
		}
	}
}

// TestApplyFlagConfigRejectsUnknownAndBadDurations covers the errors that are
// only detectable against a real FlagSet: unknown keys and values the flag's
// own parser rejects (a bare "90" is not a duration — Go would read it as 90ns).
func TestApplyFlagConfigRejectsUnknownAndBadDurations(t *testing.T) {
	fs, _, _, _, _, _ := newIsolatedFlagSet()
	if err := applyFlagConfig(fs, [][2]string{{"bogus-flag", "1"}}, nil); err == nil {
		t.Fatal("unknown key must be rejected at apply time")
	}
	if err := applyFlagConfig(fs, [][2]string{{"session-ttl", "90"}}, nil); err == nil {
		t.Fatal("bare number for a duration flag must be rejected at apply time")
	}
}

func TestLoadFlagConfigFileMissingFile(t *testing.T) {
	if _, err := loadFlagConfigFile(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("missing file must be an error when --config is explicit")
	}
}

func TestApplyFlagConfigCommandLineWins(t *testing.T) {
	fs, listen, password, ttl, count, push := newIsolatedFlagSet()

	// User passed --listen explicitly; the file carries the rest (and one
	// unknown key that must abort).
	explicit := map[string]bool{"listen": true}
	entries := [][2]string{
		{"listen", ":9999"},
		{"owner-password", "from-file"},
		{"session-ttl", "15m"},
		{"rate-limit", "42"},
		{"push-enabled", "false"},
	}
	if err := applyFlagConfig(fs, entries, explicit); err != nil {
		t.Fatalf("applyFlagConfig: %v", err)
	}
	if *listen != "127.0.0.1:8090" {
		t.Fatalf("explicit --listen must win, got %q", *listen)
	}
	if *password != "from-file" {
		t.Fatalf("file password not applied: %q", *password)
	}
	if *ttl != 15*time.Minute {
		t.Fatalf("file ttl not applied: %v", *ttl)
	}
	if *count != 42 {
		t.Fatalf("file rate-limit not applied: %d", *count)
	}
	if *push {
		t.Fatal("file push-enabled=false not applied")
	}

	// Unknown keys abort with a clear error.
	if err := applyFlagConfig(fs, [][2]string{{"bogus", "x"}}, nil); err == nil {
		t.Fatal("unknown flag must be rejected")
	}
	// Values that the flag's own parser rejects also abort (bad duration).
	if err := applyFlagConfig(fs, [][2]string{{"session-ttl", "soon"}}, nil); err == nil {
		t.Fatal("unparseable duration must be rejected")
	}
}

func TestLoadFlagConfigFileDashPrefixAndMetaRejected(t *testing.T) {
	path := writeTemp(t, "\"--listen\": \":9000\"\n")
	entries, err := loadFlagConfigFile(path)
	if err != nil {
		t.Fatalf("dash-prefixed key should be accepted: %v", err)
	}
	if entries[0][0] != "listen" {
		t.Fatalf("dash prefix not stripped: %q", entries[0][0])
	}
	for _, bad := range []string{"help: true\n", "-config: x\n"} {
		if _, err := loadFlagConfigFile(writeTemp(t, bad)); err == nil {
			t.Fatalf("meta key %q must be rejected", bad)
		}
	}
}
