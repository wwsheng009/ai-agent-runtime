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

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/auth"
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
		ownerPassword = flag.String("owner-password", "", "owner login password (empty disables auth — dev only)")
		sessionTTL    = flag.Duration("session-ttl", auth.DefaultTTL, "owner session lifetime")
	)
	flag.Parse()

	if (*tlsCert == "") != (*tlsKey == "") {
		fmt.Fprintln(os.Stderr, "both --tls-cert and --tls-key are required to enable TLS")
		return 1
	}
	secure := *tlsCert != ""

	owner, err := auth.NewOwner(*ownerPassword, *sessionTTL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway: failed to initialize owner auth: %v\n", err)
		return 1
	}
	if !owner.Enabled() {
		fmt.Fprintln(os.Stderr, "gateway: WARNING: --owner-password not set; console/API/proxy are UNPROTECTED (dev only)")
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

	router := mux.NewRouter()
	router.Handle("/gw/agent", server)
	router.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "nodes": registry.Count()})
	})
	// Owner login/logout (unauthenticated; the login route verifies the password).
	router.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
			return
		}
		if !owner.Enabled() || !owner.VerifyPassword(req.Password) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid credentials"})
			return
		}
		token, expiry := owner.Issue()
		http.SetCookie(w, auth.SessionCookie(token, owner.TTL(), secure))
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "expires_at": expiry})
	}).Methods(http.MethodPost)
	router.HandleFunc("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(auth.CookieName); err == nil {
			owner.Revoke(cookie.Value)
		}
		http.SetCookie(w, auth.ClearCookie(secure))
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
	}).Methods(http.MethodPost)
	// Owner-protected node list.
	router.Handle("/api/nodes", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"nodes": registry.List()})
	}))).Methods(http.MethodGet)
	// Reverse proxy: /n/{node_id}/... → the node's local service.
	router.PathPrefix("/n/{node_id}/").Handler(owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeID := mux.Vars(r)["node_id"]
		path := strings.TrimPrefix(r.URL.Path, "/n/"+nodeID)
		if path == "" {
			path = "/"
		}
		server.ServeProxy(w, r, nodeID, path)
	})))

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	scheme := "http"
	if *tlsCert != "" {
		scheme = "https"
	}
	fmt.Fprintf(os.Stdout, "aicli-gateway listening on %s://%s (public=%s)\n", scheme, *listen, strings.TrimSpace(*publicBaseURL))

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
