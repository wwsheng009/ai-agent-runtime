package remotetools

import (
	"fmt"
	"os"
	"strings"
)

// httpEndpoint renders the URL a client should use, using the bound address
// (so --http-listen 127.0.0.1:0 reports the real port) and the TLS setting.
func (s *Server) httpEndpoint(addr string) string {
	scheme := "http"
	if strings.TrimSpace(s.cfg.HTTP.TLSCert) != "" && strings.TrimSpace(s.cfg.HTTP.TLSKey) != "" {
		scheme = "https"
	}
	return scheme + "://" + addr + s.cfg.HTTP.Path
}

// httpBannerLines is the startup summary printed for `serve --http`.
//
// It deliberately reports WHERE the credential comes from, never its value: the
// banner ends up in terminal scrollback and systemd journals.
func (s *Server) httpBannerLines(addr string) []string {
	endpoint := s.httpEndpoint(addr)
	auth := "none (loopback only)"
	switch {
	case strings.TrimSpace(s.cfg.HTTP.TokenFile) != "":
		auth = "bearer token from file " + s.cfg.HTTP.TokenFile
	case strings.TrimSpace(s.cfg.HTTP.TokenEnv) != "" && strings.TrimSpace(os.Getenv(s.cfg.HTTP.TokenEnv)) != "":
		auth = "bearer token from env " + s.cfg.HTTP.TokenEnv
	}
	names := s.ToolNames()
	preview := strings.Join(names, ", ")
	if len(names) > 8 {
		preview = strings.Join(names[:8], ", ") + fmt.Sprintf(", ... (%d total)", len(names))
	}
	profile := strings.TrimSpace(string(s.cfg.Profile))
	if profile == "" {
		profile = string(ProfileAll)
	}
	lines := []string{
		"aicli-remote-tools: HTTP MCP server ready",
		"  endpoint : " + endpoint,
		"  workspace: " + s.workspace.Root(),
		fmt.Sprintf("  tools    : %d (%s)", len(names), preview),
		fmt.Sprintf("  profile  : %s (readOnly=%t)", profile, s.cfg.ReadOnly),
		"  auth     : " + auth,
	}
	if s.cfg.Backend == BackendLocal && s.cfg.ShellExposed() {
		lines = append(lines, "  shell    : "+shellLabel(s.cfg.ResolvedShell))
	}
	if s.cfg.Network.Enabled {
		allowed := "any host"
		if len(s.cfg.Network.AllowedHosts) > 0 {
			allowed = strings.Join(s.cfg.Network.AllowedHosts, ", ")
		}
		lines = append(lines, "  network  : enabled (allowed hosts: "+allowed+")")
	}
	if s.cfg.Gateway.Instance != "" {
		lines = append(lines, "  instance : "+s.cfg.Gateway.Instance)
		if strings.TrimSpace(s.cfg.Gateway.IdentityFile) != "" {
			lines = append(lines, "  identity : "+s.cfg.Gateway.IdentityFile)
		}
	}
	if auth == "none (loopback only)" {
		lines = append(lines, "  warning  : no token configured; any local process can call this endpoint (use --http-token-file)")
	}
	if s.cfg.Profile == ProfileAll && !s.cfg.ReadOnly {
		lines = append(lines, "  warning  : profile all exposes write/shell tools; mount tokens grant the full tool surface")
	}
	if s.cfg.ShellExposed() &&
		len(s.cfg.Shell.AllowedCommands) == 0 && len(s.cfg.Shell.AllowedCommandPatterns) == 0 {
		lines = append(lines,
			"  warning  : shell is visible but no allowlist is configured; every shell call will be refused (fail-closed)",
			"             enable : "+ShellPolicyEnableHint(),
			"             docs   : docs/user-guide/aicli-remote-tools.md §7",
		)
	}
	if s.cfg.ShellExposed() && strings.TrimSpace(s.cfg.Artifacts.Root) == "" {
		lines = append(lines,
			"  warning  : artifacts.root is empty; shell output above the model window will be truncated with no recovery",
			"             enable : artifacts.root: <writable directory>",
		)
	}
	lines = append(lines,
		"  connect  : aicli chat --remote "+endpoint,
		"             or any MCP client with  Authorization: Bearer <token>",
	)
	return lines
}

// printHTTPBanner writes the summary to stderr (stdout stays clean for stdio
// transports and machine-readable commands).
func (s *Server) printHTTPBanner(addr string) {
	for _, line := range s.httpBannerLines(addr) {
		fmt.Fprintln(os.Stderr, line)
	}
}
