package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// initConfigFile is the minimal, secret-free config `init` writes. Everything
// omitted falls back to the built-in defaults documented by `serve --help`.
type initConfigFile struct {
	Workspace string         `yaml:"workspace"`
	Profile   string         `yaml:"profile,omitempty"`
	HTTP      initHTTPConfig `yaml:"http"`
	Gateway   *initGateway   `yaml:"gateway,omitempty"`
}

type initHTTPConfig struct {
	Listen    string `yaml:"listen"`
	Path      string `yaml:"path"`
	TokenFile string `yaml:"tokenFile"`
}

type initGateway struct {
	UseAICLIConfig bool   `yaml:"useAICLIConfig"`
	IdentityFile   string `yaml:"identityFile,omitempty"`
	SelfName       string `yaml:"selfName,omitempty"`
}

const initUsage = `aicli-remote-tools init - write a ready-to-run config

Usage:
  aicli-remote-tools init [options]

Options:
  --workspace <dir>    workspace root (default: current directory)
  --profile <name>     all (default) | readonly | standard | ops | custom
  --config <file>      config file to write (default ~/.aicli/remote-tools/config.yaml)
  --token-file <file>  token file to create (default ~/.aicli/remote-tools/mcp.token)
  --gateway            write the gateway block even when no aicli config is detected
  --no-gateway         never write the gateway block
  --force              overwrite an existing config file (the token file is never regenerated)

After it succeeds:
  aicli-remote-tools serve --config <the file it printed>
`

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		workspace  string
		profile    string
		configPath string
		tokenPath  string
		force      bool
		gateway    bool
		noGateway  bool
	)
	fs.StringVar(&workspace, "workspace", "", "workspace root (default: current directory)")
	fs.StringVar(&profile, "profile", string(remotetools.ProfileAll), "all|readonly|standard|ops|custom")
	fs.StringVar(&configPath, "config", "", "config file to write")
	fs.StringVar(&tokenPath, "token-file", "", "token file to create")
	fs.BoolVar(&force, "force", false, "overwrite an existing config file")
	fs.BoolVar(&gateway, "gateway", false, "write the gateway block even without a detected aicli config")
	fs.BoolVar(&noGateway, "no-gateway", false, "never write the gateway block")
	fs.Usage = func() { fmt.Fprint(os.Stdout, initUsage) }
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	switch remotetools.Profile(strings.ToLower(strings.TrimSpace(profile))) {
	case remotetools.ProfileReadonly, remotetools.ProfileStandard, remotetools.ProfileOps, remotetools.ProfileAll, remotetools.ProfileCustom:
	default:
		fmt.Fprintf(os.Stderr, "aicli-remote-tools init: unknown profile %q (want all|readonly|standard|ops|custom)\n", profile)
		return 2
	}

	if workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools init: resolve working directory: %v\n", err)
			return 1
		}
		workspace = cwd
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools init: resolve workspace %q: %v\n", workspace, err)
		return 1
	}
	if info, err := os.Stat(absWorkspace); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools init: workspace %q is not a directory\n", absWorkspace)
		return 1
	}

	if configPath == "" {
		configPath, err = remotetools.DefaultConfigPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools init: %v\n", err)
			return 1
		}
	}
	if tokenPath == "" {
		tokenPath, err = remotetools.DefaultRemoteTokenPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools init: %v\n", err)
			return 1
		}
	}

	if _, statErr := os.Stat(configPath); statErr == nil && !force {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools init: %s already exists (use --force to overwrite)\n", configPath)
		return 1
	}

	created, err := remotetools.EnsureTokenFile(tokenPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools init: %v\n", err)
		return 1
	}
	tokenState := "existing, kept"
	if created {
		tokenState = "created"
	}

	wantGateway := !noGateway
	if !gateway {
		// Auto-detect the TUI's persisted connection; keep local-only when there
		// is nothing to reuse.
		section, ok, err := agentconfig.LoadGatewaySection("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools init: inspect aicli config: %v\n", err)
			return 1
		}
		wantGateway = !noGateway && ok && strings.TrimSpace(section.URL) != ""
	}

	file := initConfigFile{
		Workspace: absWorkspace,
		Profile:   strings.ToLower(strings.TrimSpace(profile)),
		HTTP: initHTTPConfig{
			Listen:    "127.0.0.1:18765",
			Path:      "/mcp",
			TokenFile: tokenPath,
		},
	}
	if wantGateway {
		identityPath, identErr := remotetools.DefaultMountIdentityPath()
		if identErr != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools init: %v\n", identErr)
			return 1
		}
		file.Gateway = &initGateway{
			UseAICLIConfig: true,
			IdentityFile:   identityPath,
			SelfName:       "self",
		}
	}

	encoded, err := yaml.Marshal(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools init: encode config: %v\n", err)
		return 1
	}
	if dir := filepath.Dir(configPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools init: create config dir: %v\n", err)
			return 1
		}
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools init: write config: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stdout, "wrote config: %s\n", configPath)
	fmt.Fprintf(os.Stdout, "workspace:    %s\n", absWorkspace)
	fmt.Fprintf(os.Stdout, "profile:      %s\n", file.Profile)
	fmt.Fprintf(os.Stdout, "token file:   %s (%s)\n", tokenPath, tokenState)
	if file.Gateway != nil {
		fmt.Fprintln(os.Stdout, "gateway:      reuse aicli's persisted gateway.* (url detected)")
	} else {
		fmt.Fprintln(os.Stdout, "gateway:      local only (no aicli gateway config detected)")
	}
	fmt.Fprintf(os.Stdout, "\nnext:\n  aicli-remote-tools serve --config %s\n", configPath)
	return 0
}
