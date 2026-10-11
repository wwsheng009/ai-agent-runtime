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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/audit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/device"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
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

// nodeHub broadcasts registry changes to console SSE subscribers.
type nodeHub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newNodeHub() *nodeHub {
	return &nodeHub{subs: make(map[chan struct{}]struct{})}
}

func (h *nodeHub) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *nodeHub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *nodeHub) notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
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
		metricsToken   = flag.String("metrics-token", "", "bearer token for /metrics; empty allows loopback only")
		pushEnabled    = flag.Bool("push-enabled", true, "enable Web Push notifications (VAPID + RFC 8291)")
		vapidSubject   = flag.String("vapid-subject", "mailto:gateway@localhost", "VAPID JWT subject (mailto: or https:)")
		presenceTTL    = flag.Duration("presence-ttl", 90*time.Second, "how long a console view suppresses push notifications")
		coalesceWindow = flag.Duration("notify-coalesce-window", 30*time.Second, "merge low-priority notifications (completed/waiting) arriving within this window; 0 disables coalescing")
		quietHours     = flag.String("notify-quiet-hours", "", "comma-separated local quiet hours (HH:MM-HH:MM) muting low-priority notifications; approval/question/error still fire")
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

	auditLog := audit.New(2000, filepath.Join(*dataDir, "audit.log"))
	hub := newNodeHub()
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
					auditLog.Record(audit.Event{Type: audit.TypeRateLimited, IP: ip})
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
	var pushManager *push.Manager
	if *pushEnabled {
		vapid, vapidErr := push.LoadOrCreateVAPID(filepath.Join(*dataDir, "vapid.key"))
		if vapidErr != nil {
			fmt.Fprintf(os.Stderr, "gateway: failed to initialize VAPID key: %v\n", vapidErr)
			return 1
		}
		pushStore, storeErr := push.OpenStore(filepath.Join(*dataDir, "push.json"))
		if storeErr != nil {
			fmt.Fprintf(os.Stderr, "gateway: failed to open push store: %v\n", storeErr)
			return 1
		}
		pushManager = push.NewManager(pushStore, vapid, *vapidSubject)
	}
	presence := newPresenceRegistry(*presenceTTL)
	notifier := newPushNotifier(pushManager, *publicBaseURL, presence)
	quietRanges, quietErr := parseQuietHours(*quietHours)
	if quietErr != nil {
		fmt.Fprintf(os.Stderr, "gateway: invalid --notify-quiet-hours: %v\n", quietErr)
		return 1
	}
	notifier.coalesceWindow = *coalesceWindow
	notifier.quietHours = quietRanges
	// Registry persistence (docs/gateway/08-notifications.md §10.2): restore the
	// last-known snapshot (marked stale) so the console keeps node/session/attention
	// rows and restarts do not re-push old attention, then persist changes going
	// forward via the server's debounced snapshot writer.
	if restored := registry.Restore(tunnel.LoadSnapshot(*dataDir)); restored > 0 {
		notifier.Seed(registry.List())
		fmt.Fprintf(os.Stderr, "gateway: restored %d node(s) from %s\n", restored, filepath.Join(*dataDir, "registry.json"))
	}
	server := tunnel.NewServer(tunnel.Config{
		ServerVersion:        *serverVersion,
		HeartbeatIntervalSec: *heartbeatSec,
		PublicBaseURL:        *publicBaseURL,
		Registry:             registry,
		DataDir:              *dataDir,
		Authenticate:         deviceAuth(devices.Store, *deviceToken),
		OnRouteDenied: func(r *http.Request) {
			bans.Record(resolver.ClientIP(r))
		},
		OnRegistryChanged: func() {
			hub.notify()
			notifier.observe(registry.List())
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
			auditLog.Record(audit.Event{Type: audit.TypeLoginFailure, IP: resolver.ClientIP(r)})
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid credentials"})
			return
		}
		token, expiry := owner.Issue()
		auditLog.Record(audit.Event{Type: audit.TypeLoginSuccess, IP: resolver.ClientIP(r)})
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
			auditLog.Record(audit.Event{Type: audit.TypeDeviceCreated, Detail: info.ID, IP: resolver.ClientIP(r)})
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
		auditLog.Record(audit.Event{Type: audit.TypeDeviceRevoked, Detail: id, IP: resolver.ClientIP(r)})
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
			"push_subscriptions":   pushSubscriptionCount(pushManager),
			"presence_entries":     presence.Count(),
		})
	}))).Methods(http.MethodGet)
	// Audit events (owner-protected).
	router.Handle("/api/audit", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": auditLog.Entries(limit)})
	}))).Methods(http.MethodGet)
	// Live registry events for the console (owner-protected SSE).
	router.Handle("/api/events", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		send := func() bool {
			payload, err := json.Marshal(map[string]any{"nodes": registry.List(), "count": registry.Count()})
			if err != nil {
				return false
			}
			if _, err := fmt.Fprintf(w, "event: nodes\ndata: %s\n\n", payload); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		if !send() {
			return
		}
		ch := hub.subscribe()
		defer hub.unsubscribe(ch)
		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ch:
				if !send() {
					return
				}
			case <-heartbeat.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}))).Methods(http.MethodGet)
	// Ban management (owner-protected): list / manual ban / unban.
	router.Handle("/api/bans", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"bans": bans.Entries()})
		case http.MethodPost:
			var req struct {
				IP  string `json:"ip"`
				TTL string `json:"ttl"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.IP) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ip is required"})
				return
			}
			ttl := time.Duration(0)
			if strings.TrimSpace(req.TTL) != "" {
				if parsed, parseErr := time.ParseDuration(req.TTL); parseErr == nil {
					ttl = parsed
				}
			}
			bans.Ban(strings.TrimSpace(req.IP), ttl)
			auditLog.Record(audit.Event{Type: audit.TypeManualBan, IP: strings.TrimSpace(req.IP)})
			writeJSON(w, http.StatusOK, map[string]any{"bans": bans.Entries()})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})))
	router.Handle("/api/bans/{ip}", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ip := mux.Vars(r)["ip"]
		if !bans.Unban(ip) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "ban not found"})
			return
		}
		auditLog.Record(audit.Event{Type: audit.TypeManualUnban, IP: ip})
		writeJSON(w, http.StatusOK, map[string]any{"unbanned": ip})
	})))
	// Web Push (owner-protected): VAPID config, subscription management, test send.
	router.Handle("/api/push/config", owner.RequireSession(handlePushConfig(pushManager))).Methods(http.MethodGet)
	router.Handle("/api/push/subscriptions", owner.RequireSession(handlePushSubscriptions(pushManager)))
	router.Handle("/api/push/unsubscribe", owner.RequireSession(handlePushUnsubscribe(pushManager)))
	router.Handle("/api/push/test", owner.RequireSession(handlePushTest(pushManager)))
	// Presence: the console reports the session it is viewing so the gateway can
	// suppress push notifications for it (docs/gateway/08-notifications.md §5.4).
	router.Handle("/api/presence", owner.RequireSession(handlePresence(presence))).Methods(http.MethodPost)
	// Prometheus text metrics (bearer token, or loopback when no token is set).
	router.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeMetrics(r, *metricsToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writePrometheusMetrics(w, &metrics, registry.Count(), devices.Count(), len(bans.Entries()), pushSubscriptionCount(pushManager))
	})
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
		server.PersistSnapshot()
		return 1
	}
	server.PersistSnapshot()
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

// pushSubscriptionCount reports stored push subscriptions (0 when disabled).
func pushSubscriptionCount(manager *push.Manager) int {
	if manager == nil {
		return 0
	}
	return manager.Store().Count()
}

// authorizeMetrics allows scraping with a bearer token, or from loopback when
// no token is configured.
func authorizeMetrics(r *http.Request, token string) bool {
	token = strings.TrimSpace(token)
	if token != "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "Bearer "
		return strings.HasPrefix(auth, prefix) && strings.TrimSpace(auth[len(prefix):]) == token
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// writePrometheusMetrics renders the process counters in Prometheus text format.
func writePrometheusMetrics(w http.ResponseWriter, m *gatewayMetrics, nodes, devices, activeBans, pushSubs int) {
	write := func(name, help, typ string, value int64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, typ, name, value)
	}
	write("aicli_gateway_requests_total", "Total HTTP requests.", "counter", m.requests.Load())
	write("aicli_gateway_rate_limited_total", "Requests rejected by rate limiting.", "counter", m.rateLimited.Load())
	write("aicli_gateway_banned_total", "Requests rejected by a ban.", "counter", m.banned.Load())
	write("aicli_gateway_proxy_requests_total", "Authorized reverse-proxy requests.", "counter", m.proxy.Load())
	write("aicli_gateway_nodes", "Registered nodes.", "gauge", int64(nodes))
	write("aicli_gateway_devices", "Device tokens.", "gauge", int64(devices))
	write("aicli_gateway_active_bans", "Active bans.", "gauge", int64(activeBans))
	write("aicli_gateway_push_subscriptions", "Stored Web Push subscriptions.", "gauge", int64(pushSubs))
}
