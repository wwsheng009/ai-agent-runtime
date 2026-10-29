package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// applyAutoInit makes a bare `serve` self-configuring on first run:
//
//   - creates ~/.aicli/remote-tools/mcp.token when no credential is configured;
//   - reuses aicli's persisted gateway.* (unless --no-gateway);
//   - writes ~/.aicli/remote-tools/config.yaml when no config file was found.
//
// Every step is best-effort and logged; failures leave the server running with
// in-code defaults instead of turning a convenience feature into a startup
// blocker.
func applyAutoInit(cfg *remotetools.Config, flags *serveFlags) {
	if cfg == nil || flags == nil {
		return
	}
	if flags.noGateway {
		disabled := false
		cfg.Gateway.UseAICLIConfig = &disabled
	}

	if !flags.noAutoInit {
		ensureAutoToken(cfg)
	}

	if flags.configSource != "" || flags.configB64 != "" {
		// An operator-managed config wins; never write a second one next to it.
		return
	}

	useAICLI := false
	if !flags.noGateway {
		section, ok, err := agentconfig.LoadGatewaySection("")
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: WARNING: inspect aicli gateway config: %v\n", err)
		case ok && strings.TrimSpace(section.URL) != "":
			useAICLI = true
			enabled := true
			cfg.Gateway.UseAICLIConfig = &enabled
			fmt.Fprintln(os.Stderr, "aicli-remote-tools: gateway auto-enabled from aicli config (use --no-gateway to stay local)")
		}
	}

	if flags.noAutoInit {
		return
	}
	path, err := remotetools.DefaultConfigPath()
	if err != nil {
		return
	}
	if err := remotetools.WriteDefaultConfig(path, useAICLI); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another process won the race; its config is as good as ours.
			return
		}
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: WARNING: cannot write config %s: %v\n", path, err)
		return
	}
	fmt.Fprintf(os.Stderr, "aicli-remote-tools: wrote config %s (edit it or pass --config)\n", path)
}

// ensureAutoToken creates the default token file when neither a token file nor
// a populated environment variable is configured.
func ensureAutoToken(cfg *remotetools.Config) {
	if strings.TrimSpace(cfg.HTTP.TokenFile) != "" {
		return
	}
	envName := strings.TrimSpace(cfg.HTTP.TokenEnv)
	if envName != "" && strings.TrimSpace(os.Getenv(envName)) != "" {
		return
	}
	path, err := remotetools.DefaultRemoteTokenPath()
	if err != nil {
		return
	}
	created, err := remotetools.EnsureTokenFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aicli-remote-tools: WARNING: cannot create token %s: %v (loopback accepts all requests)\n", path, err)
		return
	}
	cfg.HTTP.TokenFile = path
	state := "existing"
	if created {
		state = "created"
	}
	fmt.Fprintf(os.Stderr, "aicli-remote-tools: token file %s: %s\n", path, state)
}
