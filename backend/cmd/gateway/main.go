// Command gateway is the aicli-gateway server: it exposes the /gw/agent reverse
// tunnel endpoint that local aicli / runtime-server processes dial into, plus a
// minimal health/node surface. It is the P0 skeleton; the reverse proxy, owner
// auth and console land in later steps (docs/gateway/).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		listen        = flag.String("listen", "127.0.0.1:8090", "listen address (host:port)")
		publicBaseURL = flag.String("public-base-url", "", "externally reachable base URL used to build console URLs")
		serverVersion = flag.String("server-version", "0.1.0", "server version reported in WELCOME")
		deviceToken   = flag.String("device-token", "", "shared device token (Authorization: Bearer); empty accepts all (dev only)")
		heartbeatSec  = flag.Int("heartbeat-interval", 0, "heartbeat interval (seconds) advertised in WELCOME; 0 uses the agent default")
		tlsCert       = flag.String("tls-cert", "", "TLS certificate file (requires --tls-key)")
		tlsKey        = flag.String("tls-key", "", "TLS key file (requires --tls-cert)")
	)
	flag.Parse()

	if (*tlsCert == "") != (*tlsKey == "") {
		fmt.Fprintln(os.Stderr, "both --tls-cert and --tls-key are required to enable TLS")
		return 1
	}

	registry := tunnel.NewRegistry()
	server := tunnel.NewServer(tunnel.Config{
		ServerVersion:        *serverVersion,
		HeartbeatIntervalSec: *heartbeatSec,
		PublicBaseURL:        *publicBaseURL,
		Registry:             registry,
		Authenticate:         deviceTokenAuth(*deviceToken),
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "gateway: "+format+"\n", args...)
		},
	})

	mux := http.NewServeMux()
	mux.Handle("/gw/agent", server)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "nodes": registry.Count()})
	})
	mux.HandleFunc("/api/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"nodes": registry.List()})
	})

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	scheme := "http"
	if *tlsCert != "" {
		scheme = "https"
	}
	fmt.Fprintf(os.Stdout, "aicli-gateway listening on %s://%s (public=%s)\n", scheme, *listen, strings.TrimSpace(*publicBaseURL))

	var err error
	if *tlsCert != "" {
		err = httpServer.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		err = httpServer.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "gateway: server exited: %v\n", err)
		return 1
	}
	return 0
}

// deviceTokenAuth returns a request authenticator, or nil when no token is
// configured (development only — production must set a token).
func deviceTokenAuth(token string) func(*http.Request) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return func(r *http.Request) error {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) || strings.TrimSpace(auth[len(prefix):]) != token {
			return fmt.Errorf("invalid device token")
		}
		return nil
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
