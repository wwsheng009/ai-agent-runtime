// Command gateway is the aicli-gateway server: it exposes the /gw/agent reverse
// tunnel endpoint that local aicli / runtime-server processes dial into, plus a
// minimal health/node surface. It is the P0 skeleton; the reverse proxy, owner
// auth and console land in later steps (docs/gateway/).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/appdevice"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/audit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/device"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/mcpmount"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ntfy"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ratelimit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify/channels"
)

// gatewayMetrics holds simple process counters exposed at /api/metrics.
type gatewayMetrics struct {
	requests    atomic.Int64
	rateLimited atomic.Int64
	proxy       atomic.Int64
	banned      atomic.Int64
	// MCP mounts (docs/gateway/16-mcp-mount-tunnel.md).
	mountRequests   atomic.Int64
	mountAuthFailed atomic.Int64
}

// hubEvent is one signal for a console SSE subscriber: either "full" (reload
// the whole node snapshot) or "session" (patch one session row).
type hubEvent struct {
	kind      string // "full" | "session"
	nodeID    string
	sessionID string
	state     string
	attention string
}

// nodeHub broadcasts registry changes to console SSE subscribers. The channel
// is small and drop-oldest: a lost "session" delta is reconciled by the next
// full snapshot (meta tick), which keeps a slow subscriber from stalling the
// tunnel.
type nodeHub struct {
	mu   sync.Mutex
	subs map[chan hubEvent]struct{}
}

func newNodeHub() *nodeHub {
	return &nodeHub{subs: make(map[chan hubEvent]struct{})}
}

func (h *nodeHub) subscribe() chan hubEvent {
	ch := make(chan hubEvent, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *nodeHub) unsubscribe(ch chan hubEvent) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// notify asks every subscriber for a full snapshot refresh.
func (h *nodeHub) notify() {
	h.publish(hubEvent{kind: "full"})
}

// publishSession asks subscribers to patch a single session row.
func (h *nodeHub) publishSession(nodeID, sessionID string) {
	h.publish(hubEvent{kind: "session", nodeID: nodeID, sessionID: sessionID})
}

// publishTransition asks subscribers to emit a lightweight session-event
// (state/attention migration) without touching the snapshot.
func (h *nodeHub) publishTransition(nodeID, sessionID, state, attention string) {
	h.publish(hubEvent{kind: "transition", nodeID: nodeID, sessionID: sessionID, state: state, attention: attention})
}

func (h *nodeHub) publish(ev hubEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			// Drop the oldest pending signal, then retry once (latest-wins).
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func main() {
	os.Exit(run())
}

// normalizeServeArgs accepts the documented "gateway serve --flag ..." spelling:
// a single optional positional "serve" is stripped so the flags after it are
// parsed; anything else is rejected instead of silently ignored (which would
// bind the default port with default options).
func normalizeServeArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	if args[0] == "serve" {
		return args[1:]
	}
	if strings.HasPrefix(args[0], "-") {
		return args
	}
	fmt.Fprintf(os.Stderr, "gateway: unknown subcommand %q (this binary has no subcommands; see --help)\n", args[0])
	os.Exit(2)
	return nil
}

// isLoopbackListen reports whether the listen address only exposes loopback
// interfaces (empty host means "all interfaces" and is NOT loopback).
func isLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return false
	}
	if host == "" {
		return false // ":8443" binds every interface
	}
	ip := net.ParseIP(host)
	if ip == nil {
		resolved, err := net.LookupHost(host)
		if err != nil || len(resolved) == 0 {
			return false
		}
		ip = net.ParseIP(resolved[0])
		if ip == nil {
			return false
		}
	}
	return ip.IsLoopback()
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// usage prints the grouped, annotated flag reference (docs/gateway/
// 14-implementation-record.md §2). The Go default lists flags alphabetically
// with one-line texts; operators need the lifecycle around them too.
func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprint(out, `aicli-gateway — session gateway for aicli / runtime-server agents (docs/gateway/)

Usage:
  aicli-gateway [serve] --listen <host:port> [flags]

  "serve" is an optional positional and is ignored; any other positional exits 2.

Config file: --config gateway.yaml loads a YAML flagfile whose keys mirror the
flag names (e.g. listen: ":8443", owner-password: "...", presence-ttl: 90s);
command-line flags win over file entries, file entries win over defaults.

State (under --data-dir, loaded once at startup — edit only while stopped):
  devices.json   device tokens (issue via the console API, shown once)
  vapid.key      Web Push identity, 0600, auto-created
  push.json      push subscriptions
  app-devices.json  native app push tokens, 0600
  registry.json  node registry snapshot; restored as stale entries on start
  audit.log      append-only security audit trail

Shutdown: SIGINT/SIGTERM stop accepting, send GOAWAY to every live agent
(they reconnect to the failover peer via their backoff), flush the snapshot
and exit 0.

─ Listen / identity ─────────────────────────────────────────────
`)
	flag.PrintDefaults()
	fmt.Fprint(out, `
Safety: a non-loopback --listen with no --owner-password and no device tokens
is refused at startup; pass --allow-unprotected to accept that risk explicitly.
 
 Exit codes: 0 clean run or graceful shutdown; 1 runtime failure; 2 usage error.
 Docs: docs/gateway/14-implementation-record.md (run), 10-runbook.md (operate).
 `)
}

func run() int {
	flag.Usage = usage
	var (
		configPath      = flag.String("config", "", "optional YAML flagfile whose keys mirror flag names; command-line flags win over file entries")
		listen          = flag.String("listen", "127.0.0.1:8090", "listen address (host:port)")
		publicBaseURL   = flag.String("public-base-url", "", "externally reachable base URL used to build console URLs")
		serverVersion   = flag.String("server-version", "0.1.0", "server version reported in WELCOME")
		deviceToken     = flag.String("device-token", "", "shared device token (Authorization: Bearer); empty accepts all (dev only)")
		heartbeatSec    = flag.Int("heartbeat-interval", 0, "heartbeat interval (seconds) advertised in WELCOME; 0 uses the agent default")
		tlsCert         = flag.String("tls-cert", "", "TLS certificate file (requires --tls-key)")
		tlsKey          = flag.String("tls-key", "", "TLS key file (requires --tls-cert)")
		ownerPassword   = flag.String("owner-password", "", "owner login password (empty disables auth — dev only)")
		sessionTTL      = flag.Duration("session-ttl", auth.DefaultTTL, "owner session lifetime")
		rateLimit       = flag.Int("rate-limit", 0, "per-IP requests per minute for the proxy and tunnel upgrade; 0 disables")
		dataDir         = flag.String("data-dir", "./data", "directory for persisted gateway state (device tokens)")
		trustedProxies  = flag.String("trusted-proxies", "127.0.0.1/32,::1/128", "comma-separated trusted proxy CIDRs/IPs; X-Forwarded-For is honored only from these")
		banThreshold    = flag.Int("ban-threshold", 0, "penalty count within --ban-window before an IP is auto-banned; 0 disables")
		banWindow       = flag.Duration("ban-window", time.Minute, "penalty window")
		banTTL          = flag.Duration("ban-ttl", 10*time.Minute, "auto-ban duration")
		metricsToken    = flag.String("metrics-token", "", "bearer token for /metrics; empty allows loopback only")
		pushEnabled     = flag.Bool("push-enabled", true, "enable Web Push notifications (VAPID + RFC 8291)")
		ntfyURL         = flag.String("ntfy-url", "", "ntfy server base URL for the secondary notification channel (empty disables), e.g. https://ntfy.sh")
		ntfyTopic       = flag.String("ntfy-topic", "", "ntfy topic to publish notifications to (required with --ntfy-url)")
		ntfyToken       = flag.String("ntfy-token", "", "ntfy access token (Bearer) for protected topics; prefer the --config flagfile for secrets")
		appPushURL      = flag.String("app-push-relay-url", "", "trusted relay webhook for native app notifications (empty disables apppush); receives device tokens and notification metadata")
		appPushToken    = flag.String("app-push-relay-token", "", "bearer token for --app-push-relay-url; prefer the --config flagfile for secrets")
		vapidSubject    = flag.String("vapid-subject", "mailto:gateway@localhost", "VAPID JWT subject (mailto: or https:)")
		presenceTTL     = flag.Duration("presence-ttl", 90*time.Second, "how long a console view suppresses push notifications")
		presenceSources = flag.String("presence-sources", "console,host", "enabled presence sources: console,host (comma separated; host = agent-reported foreground viewer, docs/bots/10)")
		coalesceWindow  = flag.Duration("notify-coalesce-window", 30*time.Second, "merge low-priority notifications (completed/waiting) arriving within this window; 0 disables coalescing")
		quietHours      = flag.String("notify-quiet-hours", "", "comma-separated local quiet hours (HH:MM-HH:MM) muting low-priority notifications; approval/question/error still fire")
		normalChannels  = flag.String("notify-normal-channels", "", "comma-separated channels for normal notifications (completed/waiting), e.g. webpush,ntfy; empty keeps the D6 single primary channel (webpush)")
		// 通知正文里的会话深链默认压成短址（/s/{code}）：完整深链 ~95 字符，
		// 会把 IM 正文预算挤掉大半（docs/gateway/08 §5.2）。false = 旧行为。
		notifyShortLinks = flag.Bool("notify-short-links", true, "use short links (/s/{code}) instead of full console deep links in notification bodies")
		eventTick       = flag.Duration("event-tick", 30*time.Second, "periodic SSE meta tick: re-broadcast the full node snapshot so idle consoles keep fresh last_heartbeat/synced_at values; 0 disables (SSE-only, no push fan-out)")
		// allowUnprotected lifts the fail-fast guard for "non-loopback listen
		// without owner password and without any device token" — the combination
		// that publishes an unauthenticated console AND an open agent tunnel to
		// the network. Defaults stay dev-friendly on loopback.
		allowUnprotected = flag.Bool("allow-unprotected", false, "start even when listening non-loopback with no owner password and no device token (overrides the safety refusal)")
		// MCP mounts (docs/gateway/16-mcp-mount-tunnel.md). The public endpoints
		// are always mount-token authenticated, even when owner auth is disabled.
		mountPathStyle = flag.String("mount-path-style", "m", "public path style for MCP mounts: \"m\" (/m/{slug}/...) or \"root\" (/{slug}/...)")
		mountTokenTTL  = flag.Duration("mount-token-ttl", 90*24*time.Hour, "default lifetime of an issued mount token")
		proxyMaxBody   = flag.Int64("proxy-max-request-body", 8<<20, "buffered request body cap for reverse-proxied requests in bytes (MCP tool calls need more than the historical 1 MiB)")
	)
	// Parse the (possibly "serve"-stripped) argument list after all flags are
	// registered, so the documented "gateway serve --flag ..." spelling works.
	args := normalizeServeArgs(os.Args[1:])
	if err := flag.CommandLine.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "gateway: %v\n", err)
		return 2
	}
	// Flagfile fallback: apply --config entries for every flag the user did NOT
	// pass explicitly (command line > file > defaults). A missing file is an
	// error only when --config was given explicitly.
	if path := strings.TrimSpace(*configPath); path != "" {
		entries, cfgErr := loadFlagConfigFile(path)
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr, "gateway: --config %s: %v\n", path, cfgErr)
			return 1
		}
		explicit := map[string]bool{}
		flag.CommandLine.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
		if applyErr := applyFlagConfig(flag.CommandLine, entries, explicit); applyErr != nil {
			fmt.Fprintf(os.Stderr, "gateway: --config %s: %v\n", path, applyErr)
			return 1
		}
		fmt.Fprintf(os.Stderr, "gateway: applied %d config entries from %s\n", len(entries), path)
	}

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
	// Meta tick: refreshes the console's snapshot on an otherwise idle
	// registry so last_heartbeat/synced_at values stay fresh. It only wakes
	// the SSE hub — it must not feed the push notifier (no periodic noise).
	if *eventTick > 0 {
		go func() {
			ticker := time.NewTicker(*eventTick)
			defer ticker.Stop()
			for range ticker.C {
				hub.notify()
			}
		}()
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
	appDevices, err := appdevice.Open(filepath.Join(*dataDir, "app-devices.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway: failed to open app device store: %v\n", err)
		return 1
	}
	// Safety refusal (docs/gateway/04-security.md): a non-loopback listener with
	// neither an owner password nor any device-token path is an unauthenticated
	// public exposure — refuse it instead of silently starting. Loopback keeps
	// the frictionless dev default; --allow-unprotected overrides explicitly.
	if !*allowUnprotected && !owner.Enabled() && !isLoopbackListen(*listen) &&
		strings.TrimSpace(*deviceToken) == "" && devices.Count() == 0 {
		fmt.Fprintln(os.Stderr, "gateway: refusing to start: non-loopback --listen with no --owner-password and no device tokens exposes the console and tunnel without authentication")
		fmt.Fprintln(os.Stderr, "gateway: set --owner-password (recommended), issue device tokens, or pass --allow-unprotected to accept the risk")
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
	// host 源 TTL 与心跳周期绑定（2×，下限 90s）：心跳停止即自动失效。
	if *heartbeatSec > 0 {
		if hostTTL := 2 * time.Duration(*heartbeatSec) * time.Second; hostTTL > presence.hostTTL {
			presence.hostTTL = hostTTL
		}
	}
	notifier := newPushNotifier(pushManager, *publicBaseURL, presence)
	// 通知短址（docs/gateway/08 §5.2）：把会话深链从 ~95 字符压到 ~35 字符，
	// 给正文留出预算。映射落 <data-dir>/short-links.json（0600，含随机 HMAC
	// 密钥）；--notify-short-links=false 时保持完整深链（旧行为，行为零变化）。
	var shortLinks *shortLinker
	if *notifyShortLinks {
		shortLinks = newShortLinker(filepath.Join(*dataDir, "short-links.json"), *publicBaseURL)
		notifier.shortLinks = shortLinks
	}
	ntfySender := ntfy.NewSender(*ntfyURL, *ntfyTopic, *ntfyToken)
	if *ntfyURL != "" && !ntfySender.Enabled() {
		fmt.Fprintln(os.Stderr, "gateway: --ntfy-url requires --ntfy-topic (and a valid base URL)")
		return 1
	}
	notifier.ntfy = ntfySender
	// 目标级出站健康：连接器每次出站的结果都记在这里（成功即清除），
	// 控制台 /api/notify/im/health 读取它并显示"最近发送失败：…"。
	imHealth := newNotifyTargetHealth()
	// 通知策略（可选文件；热加载，见 notify_policy.go）：notify-policy.json
	// 优先；缺失时只读回退旧 notify-prefs.json（PUT 保存后不再读取旧文件）。
	policySource := newNotifyPolicySource(
		filepath.Join(*dataDir, "notify-policy.json"),
		filepath.Join(*dataDir, "notify-prefs.json"),
	)
	if policyErr := policySource.Load(); policyErr != nil {
		fmt.Fprintf(os.Stderr, "gateway: WARNING: notify policy ignored: %v\n", policyErr)
	}
	notifier.policy = policySource
	// 网关自持 bot（IM 通道）配置源（docs/gateway/18 §6）。文件缺失 = 未配置
	// bot：通道面与 primary 保持既有行为（§5.2 动态主通道）。
	imSource := newNotifyIMSource(filepath.Join(*dataDir, "gateway-im.json"))
	notifier.imSource = imSource
	if _, warn := imSource.Current(); warn != "" {
		fmt.Fprintf(os.Stderr, "gateway: WARNING: gateway-im.json ignored: %s\n", warn)
	}
	// 通道级交付账本（P1）：跨重启/多目标的「同一通道同一事件只投一次」。
	deliveryLedger := newNotifyDeliveryLedger(
		filepath.Join(*dataDir, "notify-delivery.json"),
		notifyDeliveryTTL,
		notifyDeliveryCap,
	)
	notifier.deliveryLedger = deliveryLedger
	defer deliveryLedger.Close()
	// connectorAttentionHook 在下方 setupGatewayConnectors 装配后赋值（var 预声明：
	// tunnel OnSessionTransition 闭包先于 connectors setup 构造，靠闭包捕获变量
	// 实现构造期后接线；装配失败时保持 nil，transition 回调 nil 检查兜底）。
	var connectorAttentionHook gatewayAttentionHook
	// connectorIMMainChannel 同上，connectors 装配后赋值：normal 类通知的
	// IM 主通道判定（06 §1 D6），nil 时 push 侧一律自行投递。
	var connectorIMMainChannel func(nodeID, sessionID, kind string) bool
	if ntfySender.Enabled() {
		fmt.Fprintf(os.Stderr, "gateway: ntfy channel enabled: %s topic=%s\n", strings.TrimRight(*ntfyURL, "/"), ntfySender.Topic())
	}
	if raw := strings.TrimSpace(*appPushURL); raw != "" {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil || parsed.Host == "" {
			fmt.Fprintf(os.Stderr, "gateway: invalid --app-push-relay-url %q: %v\n", raw, parseErr)
			return 1
		}
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
			fmt.Fprintln(os.Stderr, "gateway: --app-push-relay-url must use HTTPS (HTTP is allowed only for loopback development)")
			return 1
		}
		notifier.appPush = &channels.AppPush{
			Store:  appDevices,
			Sender: channels.NewWebhookSender(raw, *appPushToken),
		}
		fmt.Fprintf(os.Stderr, "gateway: apppush channel enabled: %s\n", raw)
	}
	notifier.normalChannels = parseChannelList(*normalChannels)
	if len(notifier.normalChannels) > 0 {
		fmt.Fprintf(os.Stderr, "gateway: normal notification channels: %s\n", strings.Join(notifier.normalChannels, ","))
		for _, name := range notifier.normalChannels {
			switch name {
			case "webpush":
				if pushManager == nil {
					fmt.Fprintln(os.Stderr, "gateway: WARNING: normal channel webpush is configured but Web Push is disabled")
				}
			case "ntfy":
				if !ntfySender.Enabled() {
					fmt.Fprintln(os.Stderr, "gateway: WARNING: normal channel ntfy is configured but --ntfy-url/--ntfy-topic is incomplete")
				}
			case "apppush":
				if notifier.appPush == nil || !notifier.appPush.Enabled() {
					fmt.Fprintln(os.Stderr, "gateway: WARNING: normal channel apppush is configured but no --app-push-relay-url is set")
				}
			default:
				fmt.Fprintf(os.Stderr, "gateway: WARNING: unknown normal notification channel %q (ignored)\n", name)
			}
		}
	}
	if ntfySender.Enabled() && pushManager != nil {
		if len(notifier.normalChannels) == 0 {
			fmt.Fprintln(os.Stderr, "gateway: ntfy receives high-priority notifications only; set --notify-normal-channels webpush,ntfy for completed/waiting")
		} else {
			hasNtfy := false
			for _, name := range notifier.normalChannels {
				if name == "ntfy" {
					hasNtfy = true
					break
				}
			}
			if !hasNtfy {
				fmt.Fprintln(os.Stderr, "gateway: WARNING: ntfy is enabled but absent from --notify-normal-channels; completed/waiting will not go to ntfy")
			}
		}
	}
	quietRanges, quietErr := notify.ParseQuietHours(*quietHours)
	if quietErr != nil {
		fmt.Fprintf(os.Stderr, "gateway: invalid --notify-quiet-hours: %v\n", quietErr)
		return 1
	}
	notifier.coalesceWindow = *coalesceWindow
	notifier.baseQuietHours = quietRanges
	notifier.quietHours = quietRanges
	notifier.applyPolicy()
	// Registry persistence (docs/gateway/08-notifications.md §10.2): restore the
	// last-known snapshot (marked stale) so the console keeps node/session/attention
	// rows and restarts do not re-push old attention, then persist changes going
	// forward via the server's debounced snapshot writer.
	if restored := registry.Restore(tunnel.LoadSnapshot(*dataDir)); restored > 0 {
		notifier.Seed(registry.List())
		fmt.Fprintf(os.Stderr, "gateway: restored %d node(s) from %s\n", restored, filepath.Join(*dataDir, "registry.json"))
	}
	// 在场（host 源，docs/bots/10）：agent 心跳携带 Viewing → presence 续期；
	// --presence-sources 不含 host 时不接线（回退 console 单源现状）。
	var onSessionPresence func(nodeID, sessionID string)
	if presenceSourceEnabled(*presenceSources, presenceSourceHost) {
		onSessionPresence = func(nodeID, sessionID string) {
			presence.TouchSource(presenceSourceHost, nodeID, sessionID, false)
		}
	}
	// mountManagerRef is assigned right after the tunnel server exists (the
	// handler needs it as its Proxy). The callbacks cannot fire before the HTTP
	// server starts, so a nil check is only a startup-ordering guard.
	var mountManagerRef *mountManager
	server := tunnel.NewServer(tunnel.Config{
		ServerVersion:        *serverVersion,
		HeartbeatIntervalSec: *heartbeatSec,
		PublicBaseURL:        *publicBaseURL,
		Registry:             registry,
		DataDir:              *dataDir,
		MaxProxyRequestBody:  *proxyMaxBody,
		Authenticate:         deviceAuth(devices.Store, *deviceToken),
		OnMountsPrepare: func(nodeID, nodeStable string, routes []string, mounts []wire.MountSpec) ([]wire.MountAck, []wire.MountAck) {
			if mountManagerRef == nil {
				rejected := make([]wire.MountAck, 0, len(mounts))
				for _, mount := range mounts {
					rejected = append(rejected, wire.MountAck{Slug: mount.Slug, Reason: "mounts_unavailable"})
				}
				return nil, rejected
			}
			return mountManagerRef.prepareMounts(nodeID, nodeStable, routes, mounts)
		},
		OnMountsActivate: func(nodeID, nodeStable string) {
			if mountManagerRef != nil {
				mountManagerRef.activateMounts(nodeID, nodeStable)
			}
		},
		OnMountsDeactivate: func(nodeID, nodeStable string) {
			if mountManagerRef != nil {
				mountManagerRef.deactivateMounts(nodeID, nodeStable)
			}
		},
		OnRouteDenied: func(r *http.Request) {
			bans.Record(resolver.ClientIP(r))
		},
		// 自动本地通知抑制：source=node 的 target 由网关委托节点 bot 出站，
		// 规则随 REGISTER_ACK/HEARTBEAT_ACK 下发，节点据此只关闭重叠的本地
		// 自动发送（不再需要 AICLI_CONNECTOR_NOTIFY_OWNER）。target 热加载后
		// 最多一个心跳周期收敛；source=gateway 不参与，保留多通道 fan-out。
		NotifySuppress: func(nodeID string) []wire.NotifySuppressRule {
			return imSource.LocalSuppression(nodeID)
		},
		OnRegistryChanged: func() {
			hub.notify()
			notifier.observe(registry.List())
		},
		OnSessionChanged: func(nodeID, sessionID string) {
			hub.publishSession(nodeID, sessionID)
		},
		OnSessionTransition: func(nodeID, sessionID, state, attention string) {
			hub.publishTransition(nodeID, sessionID, state, attention)
			// 连接器注意力出站（FR-8）：connectorAttentionHook 在下方
			// setupGatewayConnectors 装配后赋值；装配失败时为零值闭包 no-op
			//（tunnel 回调先于 connectors setup 到达的可能性仅限启动竞态，
			// nil 检查兜底）。闭包捕获变量而非值，实现构造期后接线。
			if connectorAttentionHook != nil {
				connectorAttentionHook(nodeID, sessionID, state, attention)
			}
		},
		OnSessionPresence: onSessionPresence,
		// P1-b: presence with tail=true gates which sessions' live output the
		// agent pushes (diffed onto HEARTBEAT_ACK pending actions).
		TailWatchTargets: func(nodeID string) []string {
			return presence.TailWatched(nodeID)
		},
		OnRegistryTouch: func() {
			notifier.observe(registry.List())
		},
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "gateway: "+format+"\n", args...)
		},
	})

	mounts, err := newMountManager(
		*dataDir,
		*mountPathStyle,
		*publicBaseURL,
		*mountTokenTTL,
		server,
		server.HasSession,
		resolver.ClientIP,
		&metrics,
		auditLog,
		func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "gateway: "+format+"\n", args...)
		},
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway: %v\n", err)
		return 1
	}
	mountManagerRef = mounts
	if restored := mounts.registry.Count(); restored > 0 {
		fmt.Fprintf(os.Stderr, "gateway: restored %d MCP mounts (%d live)\n", restored, mounts.registry.CountLive())
	}

	router := mux.NewRouter()
	router.Handle("/gw/agent", limited(server))
	router.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			// nodes = registry entries (live + stale snapshots); live_nodes = only
			// connected agents. A load balancer's failover check should treat a
			// gateway as serving when it can accept /gw/agent upgrades, not based
			// on node counts (a just-restarted instance legitimately has 0).
			"nodes":      registry.Count(),
			"live_nodes": registry.CountLive(),
		})
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
		if r.URL.Query().Get("purge") == "1" {
			// Permanent removal of an already-revoked token record.
			if !devices.Delete(id) {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "only revoked tokens can be deleted — revoke it first",
				})
				return
			}
			auditLog.Record(audit.Event{Type: audit.TypeDeviceDeleted, Detail: id, IP: resolver.ClientIP(r)})
			writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
			return
		}
		if !devices.Revoke(id) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "device not found"})
			return
		}
		auditLog.Record(audit.Event{Type: audit.TypeDeviceRevoked, Detail: id, IP: resolver.ClientIP(r)})
		writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	})))
	// Owner-protected node list + bulk cleanup of offline snapshots
	// (DELETE /api/nodes?stale=1 — the console's one-click clear action).
	router.Handle("/api/nodes", owner.RequireSession(handleNodes(registry, auditLog, resolver.ClientIP, func() {
		hub.notify()
		notifier.observe(registry.List())
	}))).Methods(http.MethodGet, http.MethodDelete)
	// Owner-protected registry cleanup: remove a stale node or a dead session
	// entry (docs/gateway/10-runbook.md — orphan snapshot rows never resolve on
	// their own). Live agents simply re-register on their next sync, so the
	// operation is safe on both.
	router.Handle("/api/nodes/{node_id}", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeID := mux.Vars(r)["node_id"]
		registry.Remove(nodeID)
		auditLog.Record(audit.Event{Type: audit.TypeNodeRemoved, Detail: nodeID, IP: resolver.ClientIP(r)})
		hub.notify()
		notifier.observe(registry.List())
		writeJSON(w, http.StatusOK, map[string]any{"removed": nodeID})
	}))).Methods(http.MethodDelete)
	// 连接器页只读聚合：网关自身 store 之外，按节点读取其本地连接器管理面。
	// 该入口固定指向 /api/connectors 命名空间且只允许 GET；不把任意路径交给
	// 隧道代理。节点离线/未开启 Web 时由 ServeProxy 返回 502。
	nodeConnectorView := limited(owner.RequireSession(handleNodeConnectors(server)))
	router.Handle("/api/nodes/{node_id}/sessions/{session_id}", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		nodeID, sessionID := vars["node_id"], vars["session_id"]
		removed := registry.RemoveSession(nodeID, sessionID)
		if removed {
			auditLog.Record(audit.Event{Type: audit.TypeSessionRemoved, Detail: nodeID + "/" + sessionID, IP: resolver.ClientIP(r)})
			hub.notify()
			notifier.observe(registry.List())
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "node": nodeID, "session": sessionID})
	}))).Methods(http.MethodDelete)
	// 窗口动作（控制台 → 该节点本机执行）：在新终端里打开 / 恢复 / 关闭某个会话，
	// 或新建一个会话窗口。动作排进队列，随该节点下一次 HEARTBEAT_ACK 的 Pending
	// 下发（与 watch/unwatch 同形）；节点离线时 409，绝不排队到下次无关的重连上
	// 再执行。docs/gateway/03-protocol.md。
	windowActionRoute := owner.RequireSession(handleNodeWindowAction(server, auditLog, resolver.ClientIP))
	router.Handle("/api/nodes/{node_id}/sessions/{session_id}/window", windowActionRoute).Methods(http.MethodPost)
	router.Handle("/api/nodes/{node_id}/window", windowActionRoute).Methods(http.MethodPost)
	router.Handle("/api/metrics", owner.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"requests_total":       metrics.requests.Load(),
			"rate_limited_total":   metrics.rateLimited.Load(),
			"banned_total":         metrics.banned.Load(),
			"proxy_requests_total": metrics.proxy.Load(),
			"nodes":                registry.Count(),
			"devices":              devices.Count(),
			"app_devices":          appDevices.Count(),
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
		if limit > 2000 {
			limit = 2000
		}
		typePrefix := strings.TrimSpace(r.URL.Query().Get("type"))
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		errorsOnly := r.URL.Query().Get("errors") == "1"
		events := auditLog.Filter(limit, func(event audit.Event) bool {
			if typePrefix != "" && !strings.HasPrefix(event.Type, typePrefix) {
				return false
			}
			if errorsOnly && event.Result == "" {
				return false
			}
			if query != "" {
				haystack := strings.ToLower(strings.Join([]string{
					event.Type, event.Actor, event.Result, event.IP, event.Detail,
				}, " "))
				if !strings.Contains(haystack, query) {
					return false
				}
			}
			return true
		})
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
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
		// Single-session delta: the console patches one row instead of
		// re-marshalling the whole node list (P1 live tail path).
		sendSession := func(nodeID, sessionID string) bool {
			node, ok := registry.Get(nodeID)
			if !ok {
				return true // structural change follows via the full snapshot
			}
			for _, s := range node.Sessions {
				if s.SessionID != sessionID {
					continue
				}
				payload, err := json.Marshal(map[string]any{"node_id": nodeID, "session": s})
				if err != nil {
					return false
				}
				if _, err := fmt.Fprintf(w, "event: session\ndata: %s\n\n", payload); err != nil {
					return false
				}
				flusher.Flush()
				return true
			}
			return true
		}
		// State/attention migration: a tiny event the console renders in the
		// "刚刚" strip (no snapshot payload).
		sendTransition := func(ev hubEvent) bool {
			payload, err := json.Marshal(map[string]any{
				"node_id":    ev.nodeID,
				"session_id": ev.sessionID,
				"state":      ev.state,
				"attention":  ev.attention,
				"ts":         time.Now().UTC(),
			})
			if err != nil {
				return false
			}
			if _, err := fmt.Fprintf(w, "event: session-event\ndata: %s\n\n", payload); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		ch := hub.subscribe()
		defer hub.unsubscribe(ch)
		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-ch:
				switch ev.kind {
				case "session":
					if !sendSession(ev.nodeID, ev.sessionID) {
						return
					}
				case "transition":
					if !sendTransition(ev) {
						return
					}
				default:
					if !send() {
						return
					}
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
	// ntfy rides along as the secondary channel on the same endpoints.
	router.Handle("/api/push/config", owner.RequireSession(handlePushConfig(pushManager, ntfySender))).Methods(http.MethodGet)
	router.Handle("/api/push/subscriptions", owner.RequireSession(handlePushSubscriptions(pushManager)))
	router.Handle("/api/push/unsubscribe", owner.RequireSession(handlePushUnsubscribe(pushManager)))
	router.Handle("/api/push/test", owner.RequireSession(handlePushTest(pushManager, ntfySender)))
	// 通知策略（owner 保护，热加载；控制台「通知设置」页的数据面）。
	policyMeta := func() notifyPolicyMeta {
		return notifyPolicyMeta{
			Source:     policySource.SourceLabel(),
			Path:       policySource.Path(),
			LegacyPath: policySource.LegacyPath(),
			Channels: []notifyChannelMeta{
				{Name: "webpush", Label: "Web Push / PWA", Available: pushManager != nil},
				{Name: "ntfy", Label: "ntfy", Available: ntfySender.Enabled()},
				{Name: "apppush", Label: "原生 App", Available: notifier.appPush != nil && notifier.appPush.Enabled()},
			},
		}
	}
	router.Handle("/api/notify/policy", owner.RequireSession(handleNotifyPolicy(policySource, policyMeta, func() {
		notifier.applyPolicy()
	}))).Methods(http.MethodGet, http.MethodPut)
	// 网关自持 bot（IM 通道）目标配置（docs/gateway/18 §6.1）。owner 保护；
	// PUT 成功后在"是否存在启用 target"变化时重建通道面。
	imMeta := func() notifyIMMeta {
		return notifyIMMeta{Path: imSource.Path(), TargetLimit: notifyIMTargetLimit}
	}
	router.Handle("/api/notify/im", owner.RequireSession(handleNotifyIM(imSource, imMeta, func(wasEnabled bool) {
		notifier.applyIMConfig(wasEnabled)
	}))).Methods(http.MethodGet, http.MethodPut)
	router.Handle("/api/notify/im/test", owner.RequireSession(handleNotifyIMTest(imSource))).Methods(http.MethodPost)
	// 目标级出站健康（owner 保护）：控制台「Bot 目标」用它显示"最近发送失败"的
	// 具体原因（如微信缺 context_token），不必让运维去翻 journalctl。
	router.Handle("/api/notify/im/health", owner.RequireSession(handleNotifyIMHealth(imHealth))).Methods(http.MethodGet)
	// Native App push-token registry (owner-protected). Registration is kept
	// separate from the agent tunnel token store.
	router.Handle("/api/app/devices", owner.RequireSession(handleAppDevices(appDevices))).Methods(http.MethodGet, http.MethodPost)
	router.Handle("/api/app/devices/{id}", owner.RequireSession(handleAppDeviceDelete(appDevices))).Methods(http.MethodDelete)
	// 扫码登录二维码：微信等通道返回的是 liteapp 链接而非图片，由本端点
	// 用 go-qrcode 编码为 PNG（与 aicli micro web 的 /web/api/qr 同口径）。
	router.Handle("/api/qr", owner.RequireSession(handleQRCode())).Methods(http.MethodGet)
	// 通知短址（docs/gateway/08 §5.2）：public、**不**走 owner 鉴权——手机/微信
	// 点开时没有 cookie；控制台自身仍要求登录，短链只做 302 跳转（拿到链接的人
	// 看到的仍旧只是 node/session 标识，与旧行为把完整深链发到 IM 等价）。
	if shortLinks != nil {
		router.Handle("/s/{code}", limited(shortLinks.Handler())).Methods(http.MethodGet, http.MethodHead)
	}
	// Presence: the console reports the session it is viewing so the gateway can
	// suppress push notifications for it (docs/gateway/08-notifications.md §5.4).
	router.Handle("/api/presence", owner.RequireSession(handlePresence(presence))).Methods(http.MethodPost)
	router.Handle("/api/presence/release", owner.RequireSession(handlePresenceRelease(presence))).Methods(http.MethodPost)
	// Prometheus text metrics (bearer token, or loopback when no token is set).
	router.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeMetrics(r, *metricsToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writePrometheusMetrics(w, &metrics, registry.Count(), devices.Count(), len(bans.Entries()), pushSubscriptionCount(pushManager), mounts.registry.Count(), mounts.registry.CountLive())
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
	// MCP mounts: public endpoints authenticated by a mount token, never by the
	// owner cookie. They intentionally bypass owner.RequireSession so
	// programmatic MCP clients (which have no cookie) can call them; a disabled
	// owner password must not open this namespace.
	mountServe := http.HandlerFunc(mounts.serveMounts)
	if mounts.style == mcpmount.PathStyleRoot {
		router.MatcherFunc(func(r *http.Request, _ *mux.RouteMatch) bool {
			return mounts.matchesRootPath(r.URL.Path)
		}).Handler(limited(mountServe))
	} else {
		router.Handle("/m/{slug}", limited(mountServe))
		router.PathPrefix("/m/{slug}/").Handler(limited(mountServe))
	}
	// Mount administration (owner session, same shape as /api/devices).
	router.Handle("/api/mounts", owner.RequireSession(http.HandlerFunc(mounts.handleList))).Methods(http.MethodGet)
	router.Handle("/api/mounts/logs", owner.RequireSession(http.HandlerFunc(mounts.handleLogs))).Methods(http.MethodGet)
	router.Handle("/api/mounts/{slug}", owner.RequireSession(http.HandlerFunc(mounts.handleDelete))).Methods(http.MethodDelete)
	router.Handle("/api/mounts/{slug}/tokens", owner.RequireSession(http.HandlerFunc(mounts.handleTokens)))
	router.Handle("/api/mounts/{slug}/tokens/{id}", owner.RequireSession(http.HandlerFunc(mounts.handleTokenDelete))).Methods(http.MethodDelete)
	router.Handle("/api/mounts/{slug}/disable", owner.RequireSession(http.HandlerFunc(mounts.handleDisable))).Methods(http.MethodPost)
	router.Handle("/api/mounts/{slug}/enable", owner.RequireSession(http.HandlerFunc(mounts.handleEnable))).Methods(http.MethodPost)
	// Connectors host wiring (docs/bots/03-host-integration.md §6/§7): JSON store
	// under --data-dir, owner-session auth for the management API, public
	// webhooks verified by each provider. Registered before the console "/"
	// catch-all so /api/connectors* and /hooks/connectors/* are never swallowed
	// by the static page. A wiring failure only warns: connectors must not take
	// the tunnel/console down.
	connectorCtx, cancelConnectors := context.WithCancel(context.Background())
	defer cancelConnectors()
	connectorService, connectorAttentionHook, connectorIMMainChannel, connectorErr := setupGatewayConnectors(connectorCtx, router, owner, registry, server, *dataDir, nil)
	if connectorErr != nil {
		fmt.Fprintf(os.Stderr, "gateway: WARNING: connectors disabled: %v\n", connectorErr)
	} else {
		// D6 主通道接线：pushNotifier 的 normal 类通知据此让位给 IM（已绑定场景）。
		notifier.imMainChannel = connectorIMMainChannel
		// 网关自持 bot 的通道依赖（docs/gateway/18 §5.3）：连接器存储用于 D5
		// 过滤，SendOutbound 用于全局 target 出站，NotifyScoped 用于会话绑定
		// 路径。装配失败时三者保持 nil，IM 通道不参与通道面。
		notifier.connectorStore = connectorService.ConnectorStore()
		// 出站结果顺带写入 imHealth：成功清除旧错误、失败记下原因，
		// 控制台据此在目标行显示「最近发送失败」（见 notify_target_health.go）。
		notifier.sendOutbound = func(ctx context.Context, connectorID, chat string, msgs []connectors.OutboundMessage) error {
			err := connectorService.SendOutbound(ctx, connectorID, chat, msgs)
			imHealth.Record(connectorID, err)
			return err
		}
		notifier.notifyScoped = func(ctx context.Context, connectorIDs []string, notice connectors.AttentionNotice) error {
			err := connectorService.NotifyAttentionScoped(ctx, connectorIDs, notice)
			imHealth.RecordMany(connectorIDs, err)
			return err
		}
		imSource.SetSendTest(func(ctx context.Context, connectorID, chat, text string) error {
			// 手动「测试」也是一次真实出站：失败同样记进 imHealth，让控制台立刻显示
			// 原因（例如微信缺 context_token），不必等下一个真实事件。
			err := connectorService.SendOutbound(ctx, connectorID, chat, []connectors.OutboundMessage{{Text: text}})
			imHealth.Record(connectorID, err)
			return err
		})
	}
	handoffLogf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "gateway: "+format+"\n", args...)
	}
	// 节点 bot 配置复制（QQ/微信）：先注册具体路径，再注册只读聚合前缀；
	// gorilla/mux 按注册顺序匹配，顺序反了会被只读 handler 吞掉。
	if connectorErr == nil && server != nil {
		router.Handle("/api/nodes/{node_id}/connectors/{connector_id}/handoff",
			limited(owner.RequireSession(handleNodeConnectorHandoff(server, connectorService, handoffLogf))),
		).Methods(http.MethodPost)
	}
	if server != nil {
		router.Handle("/api/nodes/{node_id}/connectors/{connector_id}/handoff/rollback",
			limited(owner.RequireSession(handleNodeConnectorHandoffRollback(server))),
		).Methods(http.MethodPost)
	}
	// 不在这里挂 Methods(GET)：handler 自己回 405 + Allow，避免写方法落到 404。
	router.Handle("/api/nodes/{node_id}/connectors", nodeConnectorView)
	router.PathPrefix("/api/nodes/{node_id}/connectors/").Handler(nodeConnectorView)
	if connectorErr == nil && server != nil {
		go syncImportedWeChatState(connectorCtx, connectorService.ConnectorStore(), server, handoffLogf)
	}
	// 节点委托（docs/gateway/18 复用方案）：通知可交给目标节点，由节点自己的
	// bot 按本地绑定出站（不复制密钥；节点离线时由 notify 回退链改走 push）。
	// 与 connectors 装配独立——连接器管理面失败时节点委托仍可用。
	if server != nil {
		notifier.delegateToNode = newNodeAttentionDelegate(server)
		imSource.SetSendNodeAttention(notifier.delegateToNode)
	}
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

	serveErr := make(chan error, 1)
	go func() {
		if *tlsCert != "" {
			serveErr <- httpServer.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			serveErr <- httpServer.ListenAndServe()
		}
	}()
	// Graceful shutdown: on SIGINT/SIGTERM (or a server error) stop accepting,
	// send GOAWAY to every live agent (they fail over to the standby gateway
	// via their retryable reconnect path), flush the registry snapshot, and
	// give in-flight proxy requests a short drain window.
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err = <-serveErr:
	case <-stopCtx.Done():
		fmt.Fprintln(os.Stderr, "gateway: shutting down (signal received)")
	}
	server.CloseAll("gateway shutting down for failover")
	// Connectors: cancel the host ctx and stop polling / release run ownership
	// (bounded at 5s so a stuck provider cannot delay exit).
	cancelConnectors()
	stopGatewayConnectors(connectorService)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	if err == nil || err == http.ErrServerClosed {
		err = nil
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
func writePrometheusMetrics(w http.ResponseWriter, m *gatewayMetrics, nodes, devices, activeBans, pushSubs, mounts, liveMounts int) {
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
	write("aicli_gateway_mount_requests_total", "Requests to the MCP mount namespace.", "counter", m.mountRequests.Load())
	write("aicli_gateway_mount_auth_failures_total", "Mount requests rejected by authentication or scope.", "counter", m.mountAuthFailed.Load())
	write("aicli_gateway_mounts_registered", "Registered MCP mounts (live and stale).", "gauge", int64(mounts))
	write("aicli_gateway_mounts_live", "MCP mounts backed by a live node.", "gauge", int64(liveMounts))
}
