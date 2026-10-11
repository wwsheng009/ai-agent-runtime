// Command gateway is the aicli-gateway server: it exposes the /gw/agent reverse
// tunnel endpoint that local aicli / runtime-server processes dial into, plus a
// minimal health/node surface. It is the P0 skeleton; the reverse proxy, owner
// auth and console land in later steps (docs/gateway/).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/device"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ratelimit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// gatewayMetrics holds simple process counters exposed at /api/metrics.
type gatewayMetrics struct {
	requests    atomic.Int64
	rateLimited atomic.Int64
	proxy       atomic.Int64
	banned      atomic.Int64
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		listen         = flag.String("listen", "127.0.0.1:8090", "listen address (host:port)")
		publicBaseURL  = flag.String("public-base-url", "", "externally reachable base URL used to build console URLs")
		serverVersion  = flag.String("server-version", "0.1.0", "server version reported in WELCOME")
		deviceToken    = flag.String("device-token", "", "shared device token (Authorization: Bearer); empty accepts all (dev only)")
		heartbeatSec   = flag.Int("heartbeat-interval", 0, "heartbeat interval (seconds) advertised in WELCOME; 0 uses the agent default")
		tlsCert        = flag.String("tls-cert", "", "TLS certificate file (requires --tls-key)")
		tlsKey         = flag.String("tls-key", "", "TLS key file (requires --tls-cert)")
		ownerPassword  = flag.String("owner-password", "", "owner login password (empty disables auth — dev only)")
		sessionTTL     = flag.Duration("session-ttl", auth.DefaultTTL, "owner session lifetime")
		rateLimit      = flag.Int("rate-limit", 0, "per-IP requests per minute for the proxy and tunnel upgrade; 0 disables")
		dataDir        = flag.String("data-dir", "./data", "directory for persisted gateway state (device tokens)")
		trustedProxies = flag.String("trusted-proxies", "127.0.0.1/32,::1/128", "comma-separated trusted proxy CIDRs/IPs; X-Forwarded-For is honored only from these")
		banThreshold   = flag.Int("ban-threshold", 0, "penalty count within --ban-window before an IP is auto-banned; 0 disables")
		banWindow      = flag.Duration("ban-window", time.Minute, "penalty window")
		banTTL         = flag.Duration("ban-ttl", 10*time.Minute, "auto-ban duration")
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

	var metrics gatewayMetrics
	limiter := ratelimit.New(*rateLimit, time.Minute)
	resolver := ratelimit.NewResolver(strings.Split(*trustedProxies, ","))
	bans := ratelimit.NewBanList(*banThreshold, *banWindow, *banTTL)
	limited := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := resolver.ClientIP(r)
			if bans.IsBanned(ip) {
				metrics.banned.Add(1)
				http.Error(w, "banned", http.StatusForbidden)
				return
			}
			if *rateLimit > 0 {
				if ok, retry := limiter.Allow(ip); !ok {
					metrics.rateLimited.Add(1)
					bans.Record(ip)
					w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
					http.Error(w, "too many requests", http.StatusTooManyRequests)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}

	registry := tunnel.NewRegistry()
	devices, err := device.Open(filepath.Join(*dataDir, "devices.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway: failed to open device store: %v\n", err)
		return 1
	}
	server := tunnel.NewServer(tunnel.Config{
		ServerVersion:        *serverVersion,
		HeartbeatIntervalSec: *heartbeatSec,
		PublicBaseURL:        *publicBaseURL,
		Registry:             registry,
		Authenticate:         deviceAuth(devices.Store, *deviceToken),
		OnRouteDenied: func(r *http.Request) {
			bans.Record(resolver.ClientIP(r))
		},
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "gateway: "+format+"\n", args...)
		},
	})

	router := mux.NewRouter()
	router.Handle("/gw/agent", limited(server))
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
	// Device tokens (owner-protected): issue once, list, revoke.
	router.Handle("/api/devices", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"devices": devices.List()})
		case http.MethodPost:
			var req struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			token, info, err := devices.Create(req.Name)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to create device token"})
				return
			}
			// The plaintext token is returned exactly once.
			writeJSON(w, http.StatusCreated, map[string]any{"token": token, "device": info})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})))
	router.Handle("/api/devices/{id}", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := mux.Vars(r)["id"]
		if !devices.Revoke(id) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "device not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	})))
	// Owner-protected node list.
	router.Handle("/api/nodes", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"nodes": registry.List()})
	}))).Methods(http.MethodGet)
	router.Handle("/api/metrics", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"requests_total":       metrics.requests.Load(),
			"rate_limited_total":   metrics.rateLimited.Load(),
			"banned_total":         metrics.banned.Load(),
			"proxy_requests_total": metrics.proxy.Load(),
			"nodes":                registry.Count(),
			"devices":              devices.Count(),
		})
	}))).Methods(http.MethodGet)
	// Reverse proxy: /n/{node_id}/... → the node's local service.
	router.PathPrefix("/n/{node_id}/").Handler(limited(owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.proxy.Add(1)
		nodeID := mux.Vars(r)["node_id"]
		path := strings.TrimPrefix(r.URL.Path, "/n/"+nodeID)
		if path == "" {
			path = "/"
		}
		server.ServeProxy(w, r, nodeID, path)
	}))))
	// Lightweight embedded console (registered last so specific routes win).
	router.PathPrefix("/").Handler(consoleHandler())

	httpServer := &http.Server{
		Addr: *listen,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			metrics.requests.Add(1)
			router.ServeHTTP(w, r)
		}),
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

// deviceAuth returns the tunnel upgrade authenticator. It accepts a store-issued
// device token or the static --device-token fallback. It returns nil (dev mode)
// only when neither is configured.
func deviceAuth(store *device.Store, staticToken string) func(*http.Request) error {
	staticToken = strings.TrimSpace(staticToken)
	if store == nil && staticToken == "" {
		return nil
	}
	return func(r *http.Request) error {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			return fmt.Errorf("missing device token")
		}
		token := strings.TrimSpace(auth[len(prefix):])
		if staticToken != "" && token == staticToken {
			return nil
		}
		if store != nil {
			if _, ok := store.Authenticate(token); ok {
				return nil
			}
		}
		return fmt.Errorf("invalid device token")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
