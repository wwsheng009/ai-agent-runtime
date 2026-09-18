package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMCPHeaderOptions(t *testing.T) {
	headers := parseMCPHeaderOptions([]string{
		"Authorization: Bearer test-token",
		"X-Trace:   abc  ",
		"malformed",
		": empty-key",
	})
	if len(headers) != 2 {
		t.Fatalf("expected 2 parsed headers, got %#v", headers)
	}
	if headers["Authorization"] != "Bearer test-token" {
		t.Fatalf("unexpected Authorization: %q", headers["Authorization"])
	}
	if headers["X-Trace"] != "abc" {
		t.Fatalf("unexpected X-Trace: %q", headers["X-Trace"])
	}
}

func TestResolveMCPConfigPathForWrite_PrefersExplicitFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "explicit.yaml")

	prev := mcpConfigFile
	mcpConfigFile = configPath
	t.Cleanup(func() { mcpConfigFile = prev })

	if got := resolveMCPConfigPathForWrite(); got != configPath {
		t.Fatalf("expected explicit path %q, got %q", configPath, got)
	}
}

func TestRunMCPAddRemoveCommand_UsesSharedConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.yaml")

	prev := mcpConfigFile
	mcpConfigFile = configPath
	t.Cleanup(func() { mcpConfigFile = prev })

	result, err := runMCPAddCommand(mcpAddCommandOptions{
		Name:      "cli-added",
		Target:    "http://127.0.0.1:9/mcp",
		Transport: "streamableHttp",
		Headers:   []string{"Authorization: Bearer test"},
	})
	if err != nil {
		t.Fatalf("runMCPAddCommand: %v", err)
	}
	if result.Config == nil || result.Config.Type != "streamable" {
		t.Fatalf("expected canonical streamable config, got %#v", result.Config)
	}
	if got := result.Config.Env["HEADER_Authorization"]; got != "Bearer test" {
		t.Fatalf("expected header env mapping, got %q", got)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"cli-added", "streamable", "127.0.0.1:9"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}

	if _, err := runMCPRemoveCommand("cli-added"); err != nil {
		t.Fatalf("runMCPRemoveCommand: %v", err)
	}
	content, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after remove: %v", err)
	}
	if strings.Contains(string(content), "cli-added") {
		t.Fatalf("config still contains removed entry:\n%s", content)
	}
}
