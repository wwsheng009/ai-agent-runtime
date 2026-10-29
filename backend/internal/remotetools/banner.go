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
	lines := []string{
		"aicli-remote-tools: HTTP MCP server ready",
		"  endpoint : " + endpoint,
		"  workspace: " + s.workspace.Root(),
		fmt.Sprintf("  tools    : %d (%s)", len(names), preview),
		"  auth     : " + auth,
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
