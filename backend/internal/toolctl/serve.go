package toolctl

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// newSurfaceMCPServer builds the MCP server for a resolved tool surface.
//
// It is the single construction path for `toolctl serve` (stdio/http) and for
// the compatibility wrappers, so a transport or context fix lands once. The
// decorator binds the session workspace to the same root the sandbox and
// policy engine validated.
func newSurfaceMCPServer(surface *toolSurface) *toolkit.ToolkitMCPServer {
	registry := toolkit.NewRegistry()
	var failures []string
	for name, tool := range surface.tools {
		if !surface.visible[name] {
			continue
		}
		if err := registry.Register(tool); err != nil {
			// Registration of a constructed tool can only fail if two names
			// collide or a schema is invalid. Both are programming errors, but
			// silently dropping the tool would hand the model a partial surface
			// that looks complete; surface it on stderr instead.
			fmt.Fprintf(os.Stderr, "toolctl: register %s: %v\n", name, err)
			failures = append(failures, name)
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(os.Stderr, "toolctl: %d tool(s) failed to register: %v\n", len(failures), failures)
	}
	server := toolkit.NewToolkitMCPServer(registry)
	server.SetToolContextDecorator(func(ctx context.Context, _ *mcp.CallToolRequest) context.Context {
		return toolctx.WithWorkspaceRoot(ctx, surface.workspace)
	})
	return server
}

// RunHTTP exposes the resolved surface over Streamable HTTP.
//
// It is the shared transport implementation for `toolctl serve` and the
// compatibility wrappers. The auth boundary is intentionally strict: a
// non-loopback listener requires a bearer token and TLS, because an
// unauthenticated remote tool server is a remote-shell equivalent.
func (c *CLI) serveSurfaceHTTP(ctx context.Context, surface *toolSurface, listen, tokenEnv, tlsCert, tlsKey string) int {
	if (strings.TrimSpace(tlsCert) == "") != (strings.TrimSpace(tlsKey) == "") {
		fmt.Fprintln(c.Stderr, "--tls-cert and --tls-key must be set together")
		return ExitUsage
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Fprintf(c.Stderr, "invalid --listen %q: %v\n", listen, err)
		return ExitUsage
	}
	ip := net.ParseIP(host)
	nonLoopback := host == "" || ip == nil || !ip.IsLoopback()
	if nonLoopback {
		if strings.TrimSpace(os.Getenv(tokenEnv)) == "" {
			fmt.Fprintf(c.Stderr, "non-loopback listen %q requires %s\n", listen, tokenEnv)
			return ExitUsage
		}
		if strings.TrimSpace(tlsCert) == "" {
			fmt.Fprintln(c.Stderr, "non-loopback listen requires --tls-cert/--tls-key")
			return ExitUsage
		}
	}
	mcpServer := newSurfaceMCPServer(surface)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcpServer.GetMCPServer()
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	root := http.Handler(handler)
	if token := strings.TrimSpace(os.Getenv(tokenEnv)); token != "" {
		root = bearerAuthMiddleware(token, root)
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(c.Stderr, "toolctl mcp http listening on %s\n", listen)
	if strings.TrimSpace(tlsCert) != "" {
		err = server.ListenAndServeTLS(tlsCert, tlsKey)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(c.Stderr, "serve http: %v\n", err)
		return ExitExecution
	}
	return ExitOK
}
