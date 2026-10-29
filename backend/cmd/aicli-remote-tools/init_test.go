package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type initDoc struct {
	Workspace string `yaml:"workspace"`
	Profile   string `yaml:"profile"`
	HTTP      struct {
		Listen    string `yaml:"listen"`
		Path      string `yaml:"path"`
		TokenFile string `yaml:"tokenFile"`
	} `yaml:"http"`
	Gateway *struct {
		UseAICLIConfig bool   `yaml:"useAICLIConfig"`
		IdentityFile   string `yaml:"identityFile"`
		SelfName       string `yaml:"selfName"`
	} `yaml:"gateway"`
}

func TestInitWritesConfigAndToken(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	tokenPath := filepath.Join(dir, "mcp.token")

	if code := runInit([]string{"--workspace", workspace, "--config", configPath, "--token-file", tokenPath, "--no-gateway"}); code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	rawToken, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	token := strings.TrimSpace(string(rawToken))
	if len(token) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(token))
	}

	rawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	var doc initDoc
	if err := yaml.Unmarshal(rawConfig, &doc); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if doc.Workspace != workspace {
		t.Fatalf("workspace = %q, want %q", doc.Workspace, workspace)
	}
	if doc.HTTP.Listen != "127.0.0.1:18765" || doc.HTTP.Path != "/mcp" {
		t.Fatalf("http defaults = %+v", doc.HTTP)
	}
	if doc.HTTP.TokenFile != tokenPath {
		t.Fatalf("tokenFile = %q", doc.HTTP.TokenFile)
	}
	if doc.Gateway != nil {
		t.Fatalf("--no-gateway wrote a gateway block: %+v", doc.Gateway)
	}
	if doc.Profile != "all" {
		t.Fatalf("profile = %q", doc.Profile)
	}
}

func TestInitGatewayBlockAndOverwriteProtection(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	tokenPath := filepath.Join(dir, "mcp.token")

	if code := runInit([]string{"--workspace", workspace, "--config", configPath, "--token-file", tokenPath, "--gateway"}); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	var doc initDoc
	raw, _ := os.ReadFile(configPath)
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Gateway == nil || !doc.Gateway.UseAICLIConfig {
		t.Fatalf("gateway block = %+v", doc.Gateway)
	}

	firstToken, _ := os.ReadFile(tokenPath)
	if code := runInit([]string{"--workspace", workspace, "--config", configPath, "--token-file", tokenPath, "--gateway"}); code == 0 {
		t.Fatalf("second init without --force succeeded")
	}
	if code := runInit([]string{"--workspace", workspace, "--config", configPath, "--token-file", tokenPath, "--gateway", "--force"}); code != 0 {
		t.Fatalf("init --force exit = %d", code)
	}
	secondToken, _ := os.ReadFile(tokenPath)
	if string(firstToken) != string(secondToken) {
		t.Fatalf("--force rotated the token; it must be preserved")
	}
}
