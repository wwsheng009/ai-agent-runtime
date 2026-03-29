package httpclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/dnscache"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

var (
	sharedClient            *http.Client
	sharedClientOnce        sync.Once
	sharedTransport         *http.Transport
	sharedMetricsRef        atomic.Pointer[sharedMetricsStore]
	sharedConnTracker       = newConnectionTracker()
	sharedClientLastResetAt atomic.Int64
	sharedClientResetCount  atomic.Int64
	sharedClientPreReset    atomic.Value
)

const (
	unsetMinLatency             = int64(^uint64(0) >> 1)
	defaultAdaptiveBaseTimeout  = 30 * time.Second
	defaultAdaptiveMinTimeout   = 15 * time.Second
	defaultAdaptiveMaxTimeout   = 60 * time.Second
	defaultMetricsHistoryBucket = 15 * time.Second
	defaultMetricsHistoryLimit  = 5760
)

// PerformanceMetrics 性能指标收集器
type PerformanceMetrics struct {
	TotalRequests    atomic.Int64 // 总请求数
	SuccessRequests  atomic.Int64 // 成功请求数
	FailedRequests   atomic.Int64 // 失败请求数
	TotalLatency     atomic.Int64 // 总延迟（纳秒）
	MinLatency       atomic.Int64 // 最小延迟
	MaxLatency       atomic.Int64 // 最大延迟
	ActiveConns      atomic.Int64 // 活跃连接数（预留）
	IdleConns        atomic.Int64 // 空闲连接数（预留）
	InFlightRequests atomic.Int64 // 当前进行中的请求数
	LastRequestAt    atomic.Int64 // 最近请求时间（UnixNano）
	LastSuccessAt    atomic.Int64 // 最近成功时间（UnixNano）
	LastFailureAt    atomic.Int64 // 最近失败时间（UnixNano）
}

// DialMetrics 连接拨号指标
type DialMetrics struct {
	TotalDials    atomic.Int64 // 总拨号数
	SuccessDials  atomic.Int64 // 成功拨号数
	FailedDials   atomic.Int64 // 失败拨号数
	TotalLatency  atomic.Int64 // 总拨号延迟（纳秒）
	MinLatency    atomic.Int64 // 最小拨号延迟
	MaxLatency    atomic.Int64 // 最大拨号延迟
	LastSuccessAt atomic.Int64 // 最近成功拨号时间（UnixNano）
	LastFailureAt atomic.Int64 // 最近失败拨号时间（UnixNano）
}

// ConnectionMetrics 共享 HTTP Client 连接级指标
type ConnectionMetrics struct {
	OpenConns     atomic.Int64 // 当前打开的连接数
	BusyConns     atomic.Int64 // 当前承载至少一个请求的连接数
	IdleConns     atomic.Int64 // 当前空闲连接数（打开连接 - Busy）
	PeakOpenConns atomic.Int64 // Reset 以来的打开连接峰值
	PeakBusyConns atomic.Int64 // Reset 以来的活跃连接峰值
	PeakIdleConns atomic.Int64 // Reset 以来的空闲连接峰值
	TotalCreated  atomic.Int64 // 累计创建连接数
	TotalClosed   atomic.Int64 // 累计关闭连接数
	LastCreatedAt atomic.Int64 // 最近一次建连时间（UnixNano）
	LastClosedAt  atomic.Int64 // 最近一次关闭连接时间（UnixNano）
}

type sharedMetricsStore struct {
	perf     *PerformanceMetrics
	dial     *DialMetrics
	history  *metricsHistory
	adaptive *AdaptiveTimeout
}

type trackedConn struct {
	net.Conn
	tracker    *connectionTracker
	generation uint64
	closeOnce  sync.Once
}

type trackedConnState struct {
	activeRequests int64
	busy           bool
	closed         bool
}

type connectionTracker struct {
	mu         sync.Mutex
	generation atomic.Uint64
	states     map[*trackedConn]*trackedConnState
}

var connMetrics = ConnectionMetrics{}

func init() {
	sharedMetricsRef.Store(newSharedMetricsStore())
}

func newPerformanceMetrics() *PerformanceMetrics {
	metrics := &PerformanceMetrics{}
	metrics.MinLatency.Store(unsetMinLatency)
	return metrics
}

func newDialMetrics() *DialMetrics {
	metrics := &DialMetrics{}
	metrics.MinLatency.Store(unsetMinLatency)
	return metrics
}

func newSharedMetricsStore() *sharedMetricsStore {
	return &sharedMetricsStore{
		perf:     newPerformanceMetrics(),
		dial:     newDialMetrics(),
		history:  newMetricsHistory(defaultMetricsHistoryBucket, defaultMetricsHistoryLimit),
		adaptive: NewAdaptiveTimeout(defaultAdaptiveBaseTimeout, defaultAdaptiveMinTimeout, defaultAdaptiveMaxTimeout),
	}
}

func getSharedMetricsStore() *sharedMetricsStore {
	if store := sharedMetricsRef.Load(); store != nil {
		return store
	}

	store := newSharedMetricsStore()
	if sharedMetricsRef.CompareAndSwap(nil, store) {
		return store
	}
	return sharedMetricsRef.Load()
}

func newConnectionTracker() *connectionTracker {
	tracker := &connectionTracker{
		states: make(map[*trackedConn]*trackedConnState),
	}
	tracker.generation.Store(1)
	return tracker
}

func (c *trackedConn) Close() error {
	if c == nil {
		return nil
	}

	var err error
	c.closeOnce.Do(func() {
		err = c.Conn.Close()
		if c.tracker != nil {
			c.tracker.markClosed(c)
		}
	})
	return err
}

func (t *connectionTracker) wrap(conn net.Conn) net.Conn {
	if t == nil || conn == nil {
		return conn
	}

	tracked := &trackedConn{
		Conn:       conn,
		tracker:    t,
		generation: t.currentGeneration(),
	}
	t.registerOpened(tracked)
	return tracked
}

func (t *connectionTracker) currentGeneration() uint64 {
	if t == nil {
		return 0
	}
	return t.generation.Load()
}

func (t *connectionTracker) registerOpened(conn *trackedConn) {
	if t == nil || conn == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if conn.generation != t.generation.Load() {
		return
	}
	if t.states == nil {
		t.states = make(map[*trackedConn]*trackedConnState)
	}
	if _, exists := t.states[conn]; exists {
		return
	}

	t.states[conn] = &trackedConnState{}
	open := connMetrics.OpenConns.Add(1)
	connMetrics.TotalCreated.Add(1)
	connMetrics.LastCreatedAt.Store(time.Now().UnixNano())
	updatePeak(&connMetrics.PeakOpenConns, open)
	t.updateIdleLocked()
}

func (t *connectionTracker) markBusy(conn net.Conn) {
	tracked := unwrapTrackedConn(conn)
	if t == nil || tracked == nil || tracked.generation != t.currentGeneration() {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.states[tracked]
	if !exists || state.closed {
		return
	}

	state.activeRequests++
	if !state.busy {
		state.busy = true
		busy := connMetrics.BusyConns.Add(1)
		updatePeak(&connMetrics.PeakBusyConns, busy)
	}
	t.updateIdleLocked()
}

func (t *connectionTracker) markIdle(conn net.Conn) {
	tracked := unwrapTrackedConn(conn)
	if t == nil || tracked == nil || tracked.generation != t.currentGeneration() {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.states[tracked]
	if !exists {
		return
	}
	if state.activeRequests > 0 {
		state.activeRequests--
	}
	if state.activeRequests == 0 && state.busy && !state.closed {
		state.busy = false
		connMetrics.BusyConns.Add(-1)
	}
	t.updateIdleLocked()
}

func (t *connectionTracker) markClosed(conn *trackedConn) {
	if t == nil || conn == nil || conn.generation != t.currentGeneration() {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.states[conn]
	if !exists || state.closed {
		return
	}

	state.closed = true
	if state.busy {
		state.busy = false
		connMetrics.BusyConns.Add(-1)
	}
	delete(t.states, conn)

	open := connMetrics.OpenConns.Add(-1)
	if open < 0 {
		connMetrics.OpenConns.Store(0)
	}
	connMetrics.TotalClosed.Add(1)
	connMetrics.LastClosedAt.Store(time.Now().UnixNano())
	t.updateIdleLocked()
}

func (t *connectionTracker) updateIdleLocked() {
	open := connMetrics.OpenConns.Load()
	if open < 0 {
		open = 0
		connMetrics.OpenConns.Store(0)
	}

	busy := connMetrics.BusyConns.Load()
	if busy < 0 {
		busy = 0
		connMetrics.BusyConns.Store(0)
	}

	idle := open - busy
	if idle < 0 {
		idle = 0
	}
	connMetrics.IdleConns.Store(idle)
	updatePeak(&connMetrics.PeakIdleConns, idle)
}

func (t *connectionTracker) reset() {
	if t == nil {
		return
	}

	t.generation.Add(1)

	t.mu.Lock()
	t.states = make(map[*trackedConn]*trackedConnState)
	t.mu.Unlock()
}

func updatePeak(metric *atomic.Int64, value int64) {
	if metric == nil {
		return
	}

	for {
		current := metric.Load()
		if current >= value {
			return
		}
		if metric.CompareAndSwap(current, value) {
			return
		}
	}
}

func unwrapTrackedConn(conn net.Conn) *trackedConn {
	type netConnUnwrapper interface {
		NetConn() net.Conn
	}

	for depth := 0; conn != nil && depth < 8; depth++ {
		if tracked, ok := conn.(*trackedConn); ok {
			return tracked
		}

		unwrapper, ok := conn.(netConnUnwrapper)
		if !ok {
			return nil
		}

		next := unwrapper.NetConn()
		if next == nil || next == conn {
			return nil
		}
		conn = next
	}

	return nil
}

type observedResponseBody struct {
	base     io.ReadCloser
	once     sync.Once
	finalize func()
}

func (b *observedResponseBody) Read(p []byte) (int, error) {
	if b == nil || b.base == nil {
		return 0, io.EOF
	}

	n, err := b.base.Read(p)
	if err == io.EOF {
		b.finish()
	}
	return n, err
}

func (b *observedResponseBody) Close() error {
	if b == nil || b.base == nil {
		b.finish()
		return nil
	}

	err := b.base.Close()
	b.finish()
	return err
}

func (b *observedResponseBody) finish() {
	if b == nil || b.finalize == nil {
		return
	}

	b.once.Do(b.finalize)
}

type observedRoundTripper struct {
	base  http.RoundTripper
	track bool
	store *sharedMetricsStore
}

func (o *observedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if o == nil || o.base == nil {
		return nil, fmt.Errorf("base transport not configured")
	}
	if !o.track {
		return o.base.RoundTrip(req)
	}

	store := o.store
	if store == nil {
		store = getSharedMetricsStore()
	}

	start := time.Now()
	store.perf.InFlightRequests.Add(1)
	store.perf.LastRequestAt.Store(start.UnixNano())

	var assignedConn net.Conn
	trace := &httptrace.ClientTrace{
		GotConn: func(connInfo httptrace.GotConnInfo) {
			assignedConn = connInfo.Conn
			sharedConnTracker.markBusy(connInfo.Conn)
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err := o.base.RoundTrip(req)
	finishedAt := time.Now()
	latency := finishedAt.Sub(start)
	success := err == nil && resp != nil && resp.StatusCode < http.StatusBadRequest

	recordRequestAtWithStore(store, finishedAt, success, latency)

	finalize := func() {
		store.perf.InFlightRequests.Add(-1)
		if assignedConn != nil {
			sharedConnTracker.markIdle(assignedConn)
		}
	}

	if err != nil || resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		finalize()
		return resp, err
	}

	resp.Body = &observedResponseBody{
		base:     resp.Body,
		finalize: finalize,
	}

	return resp, err
}

func recordDialResult(success bool, latency time.Duration) {
	recordDialResultAt(time.Now(), success, latency)
}

func recordDialResultAt(now time.Time, success bool, latency time.Duration) {
	recordDialResultAtWithStore(getSharedMetricsStore(), now, success, latency)
}

func recordDialResultAtWithStore(store *sharedMetricsStore, now time.Time, success bool, latency time.Duration) {
	if store == nil {
		store = getSharedMetricsStore()
	}

	store.dial.TotalDials.Add(1)
	if success {
		store.dial.SuccessDials.Add(1)
		store.dial.LastSuccessAt.Store(now.UnixNano())
	} else {
		store.dial.FailedDials.Add(1)
		store.dial.LastFailureAt.Store(now.UnixNano())
	}
	store.dial.TotalLatency.Add(latency.Nanoseconds())

	updateMinMaxLatency(&store.dial.MinLatency, &store.dial.MaxLatency, latency.Nanoseconds())
	store.history.recordDialAt(now, success, latency)
}

func updateMinMaxLatency(minMetric, maxMetric *atomic.Int64, value int64) {
	for {
		oldMin := minMetric.Load()
		if oldMin < value {
			break
		}
		if minMetric.CompareAndSwap(oldMin, value) {
			break
		}
	}

	for {
		oldMax := maxMetric.Load()
		if oldMax > value {
			break
		}
		if maxMetric.CompareAndSwap(oldMax, value) {
			break
		}
	}
}

// GetSharedHTTPClient 获取全局共享的 HTTP 客户端
// 使用 sync.Once 确保只创建一次，实现连接池全局共享
func GetSharedHTTPClient(cfg *agentconfig.Config) *http.Client {
	sharedClientOnce.Do(func() {
		httpTimeout := cfg.Providers.HTTPTimeout
		proxyCfg := &cfg.Providers.Proxy
		store := getSharedMetricsStore()

		// 获取代理类型
		proxyType := GetProxyType(proxyCfg)

		logger.Info("Initializing HTTP client",
			logger.String("proxy_type", proxyTypeToString(proxyType)),
			logger.String("proxy", proxyCfg.String()),
			logger.String("dns_server", func() string {
				if httpTimeout.DNSServer != "" {
					return httpTimeout.DNSServer
				}
				return "system-default"
			}()),
		)

		var dialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)
		var proxyFunc func(*http.Request) (*url.URL, error)

		// 准备 SmartDialer 选项
		var smartDialerOpts []dnscache.SmartDialerOption
		if httpTimeout.DNSServer != "" {
			smartDialerOpts = append(smartDialerOpts, dnscache.WithSmartDialerDNSServer(httpTimeout.DNSServer))
			logger.Info("Using custom DNS server", logger.String("dns_server", httpTimeout.DNSServer))
		}

		switch proxyType {
		case ProxyTypeSOCKS5:
			// SOCKS5 代理：使用 DialContext
			dialContextFunc = createSOCKS5DialContextWithDNS(&httpTimeout, proxyCfg)
			proxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }

		case ProxyTypeHTTP, ProxyTypeHTTPS:
			// HTTP/HTTPS 代理：使用 Proxy 字段 + 智能 DNS 拨号器
			smartDialer := dnscache.NewSmartDialer(
				httpTimeout.DialTimeout,
				httpTimeout.KeepAlive,
				httpTimeout.FallbackDelay,
				httpTimeout.DNSCacheTTL,
				httpTimeout.PreferIPv4,
				smartDialerOpts...,
			)
			dialContextFunc = smartDialer.DialContext
			proxyFunc = ProxyFunc(proxyCfg)

		default:
			// 不使用代理：仅使用智能 DNS 拨号器
			smartDialer := dnscache.NewSmartDialer(
				httpTimeout.DialTimeout,
				httpTimeout.KeepAlive,
				httpTimeout.FallbackDelay,
				httpTimeout.DNSCacheTTL,
				httpTimeout.PreferIPv4,
				smartDialerOpts...,
			)
			dialContextFunc = smartDialer.DialContext
			proxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }
		}

		// 包装拨号函数以添加性能监控
		var wrappedDialContext func(ctx context.Context, network, addr string) (net.Conn, error)
		wrappedDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			startTime := time.Now()
			conn, err := dialContextFunc(ctx, network, addr)
			finishedAt := time.Now()
			duration := finishedAt.Sub(startTime)

			if err != nil {
				logger.Debug("Connection dial failed",
					logger.String("addr", addr),
					logger.Duration("duration", duration.Nanoseconds()),
					logger.Err(err))
			} else {
				logger.Debug("Connection dial success",
					logger.String("addr", addr),
					logger.Duration("duration", duration.Nanoseconds()))
			}

			recordDialResultAtWithStore(store, finishedAt, err == nil, duration)
			if err == nil {
				conn = sharedConnTracker.wrap(conn)
			}

			return conn, err
		}

		// 优化的连接池配置
		sharedTransport = &http.Transport{
			// 代理设置（HTTP/HTTPS 使用 Proxy，SOCKS5 使用 DialContext）
			Proxy: proxyFunc,

			// 连接池优化
			MaxIdleConns:        httpTimeout.MaxIdleConns,        // 全局最大空闲连接
			MaxIdleConnsPerHost: httpTimeout.MaxIdleConnsPerHost, // 每个主机最大空闲连接
			MaxConnsPerHost:     httpTimeout.MaxConnsPerHost,     // 每个主机最大连接数

			// 超时配置
			IdleConnTimeout:       httpTimeout.IdleConnTimeout,
			TLSHandshakeTimeout:   httpTimeout.TLSHandshakeTimeout,
			ResponseHeaderTimeout: httpTimeout.ResponseHeaderTimeout,

			// 使用拨号器（SOCKS5 或智能 DNS 拨号器）
			DialContext: wrappedDialContext,

			// ═══════════════════════════════════════════════════════════
			// HTTP 协议版本优化
			// ═══════════════════════════════════════════════════════════
			ForceAttemptHTTP2: getForceAttemptHTTP2(httpTimeout.HTTPVersion),
			// 最大响应头大小 (10MB)
			MaxResponseHeaderBytes: 10 << 20,
			// 写缓冲区大小 (32KB) - 提升大数据传输性能
			WriteBufferSize: 32 << 10,
			// 读缓冲区大小 (32KB) - 提升大数据传输性能
			ReadBufferSize: 32 << 10,
		}

		sharedClient = &http.Client{
			Timeout:   0, // 不设置全局超时，使用 context 超时控制
			Transport: &observedRoundTripper{base: sharedTransport, track: true, store: store},
		}

		logger.Info("Shared HTTP client initialized",
			logger.Int("max_idle_conns", httpTimeout.MaxIdleConns),
			logger.Int("max_idle_conns_per_host", httpTimeout.MaxIdleConnsPerHost),
			logger.Int("max_conns_per_host", httpTimeout.MaxConnsPerHost),
			logger.String("proxy_type", proxyTypeToString(proxyType)),
			logger.String("http_version", httpTimeout.HTTPVersion),
		)
	})

	return sharedClient
}

// getForceAttemptHTTP2 根据配置判断是否强制尝试 HTTP/2
func getForceAttemptHTTP2(httpVersion string) bool {
	switch httpVersion {
	case "1.1":
		return false // 强制使用 HTTP/1.1
	case "2", "h2":
		return true // 强制使用 HTTP/2
	case "", "auto", "1", "1.0":
		return true // 默认或自动：尝试 HTTP/2（性能更好）
	default:
		return true // 未知值，默认尝试 HTTP/2
	}
}

// GetHTTPClient 获取 HTTP 客户端
// 根据 DisableConnectionPooling 配置决定是否共享连接池
// - DisableConnectionPooling=false: 使用共享客户端（推荐，性能更好）
// - DisableConnectionPooling=true: 每次创建新客户端，禁用连接池（每次重新拨号）
func GetHTTPClient(cfg *agentconfig.Config) *http.Client {
	return GetHTTPClientWithProvider(cfg, nil)
}

// GetHTTPClientWithProvider 获取 HTTP 客户端（支持 Provider 级别代理）
// 根据 DisableConnectionPooling 配置决定是否共享连接池
// - DisableConnectionPooling=false: 使用共享客户端（推荐，性能更好）
// - DisableConnectionPooling=true: 每次创建新客户端，禁用连接池（每次重新拨号）
// - provider: 可选的 Provider 配置，如果设置了 provider.Proxy，将创建新客户端
func GetHTTPClientWithProvider(cfg *agentconfig.Config, provider *agentconfig.Provider) *http.Client {
	httpTimeout := cfg.Providers.HTTPTimeout

	// 合并 Provider 代理配置（如果存在）
	proxyCfg := &cfg.Providers.Proxy
	hasProviderProxy := provider != nil && provider.Proxy != nil
	if hasProviderProxy {
		proxyCfg = cfg.Providers.Proxy.Merge(provider.Proxy)

		// 记录使用 Provider 级别代理
		providerName := provider.Type
		if providerName == "" {
			providerName = "unknown"
		}
		logger.Info("Using provider-specific proxy configuration",
			logger.String("provider", providerName),
			logger.String("proxy", proxyCfg.String()),
		)
	}

	// 如果 Provider 有独立代理配置，总是创建新客户端（禁用连接池）
	// 因为不同的代理配置需要不同的连接池
	if hasProviderProxy {
		return createNewHTTPClient(httpTimeout, proxyCfg, false, true)
	}

	// 如果未禁用连接池，使用共享客户端（性能更好）
	if !httpTimeout.DisableConnectionPooling {
		return GetSharedHTTPClient(cfg)
	}

	// 禁用连接池：创建新客户端，设置连接池参数为 0
	return createNewHTTPClient(httpTimeout, proxyCfg, false, false)
}

// createNewHTTPClient 创建新的 HTTP 客户端
// isShared: 是否为共享客户端（共享客户端使用连接池配置，非共享客户端禁用连接池）
// isProviderSpecific: 是否为 Provider 特定客户端（用于日志标识）
func createNewHTTPClient(httpTimeout agentconfig.HTTPTimeout, proxyCfg *agentconfig.ProxyConfig, isShared bool, isProviderSpecific bool) *http.Client {
	// 获取代理类型
	proxyType := GetProxyType(proxyCfg)
	var store *sharedMetricsStore
	if isShared {
		store = getSharedMetricsStore()
	}

	// 记录 HTTP 客户端初始化信息
	if isShared || isProviderSpecific {
		logger.Info("Initializing HTTP client",
			logger.String("type", func() string {
				if isProviderSpecific {
					return "provider-specific"
				}
				return "shared"
			}()),
			logger.String("proxy_type", proxyTypeToString(proxyType)),
			logger.String("proxy", proxyCfg.String()),
			logger.String("dns_server", func() string {
				if httpTimeout.DNSServer != "" {
					return httpTimeout.DNSServer
				}
				return "system-default"
			}()),
			logger.Bool("connection_pooling", isShared),
		)
	}

	var dialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)
	var proxyFunc func(*http.Request) (*url.URL, error)

	// 准备 SmartDialer 选项
	var smartDialerOpts []dnscache.SmartDialerOption
	if httpTimeout.DNSServer != "" {
		smartDialerOpts = append(smartDialerOpts, dnscache.WithSmartDialerDNSServer(httpTimeout.DNSServer))
		if isShared || isProviderSpecific {
			logger.Info("Using custom DNS server", logger.String("dns_server", httpTimeout.DNSServer))
		}
	}

	switch proxyType {
	case ProxyTypeSOCKS5:
		// SOCKS5 代理：使用 DialContext
		dialContextFunc = createSOCKS5DialContextWithDNS(&httpTimeout, proxyCfg)
		proxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }
	case ProxyTypeHTTP, ProxyTypeHTTPS:
		// HTTP/HTTPS 代理：使用 ProxyFunc
		dialContextFunc = createSmartDialerWithDNS(&httpTimeout, smartDialerOpts)
		proxyFunc = ProxyFunc(proxyCfg)
	default: // ProxyTypeNone
		// 无代理：直接连接
		dialContextFunc = createSmartDialerWithDNS(&httpTimeout, smartDialerOpts)
		proxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }
	}

	// 包装拨号函数以添加性能监控
	var wrappedDialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	wrappedDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		startTime := time.Now()
		conn, err := dialContextFunc(ctx, network, addr)
		finishedAt := time.Now()
		duration := finishedAt.Sub(startTime)

		if err != nil {
			logger.Debug("Connection dial failed",
				logger.String("addr", addr),
				logger.Duration("duration", duration.Nanoseconds()),
				logger.Err(err))
		} else {
			logger.Debug("Connection dial success",
				logger.String("addr", addr),
				logger.Duration("duration", duration.Nanoseconds()))
		}
		if isShared {
			recordDialResultAtWithStore(store, finishedAt, err == nil, duration)
			if err == nil {
				conn = sharedConnTracker.wrap(conn)
			}
		}

		return conn, err
	}

	// 根据是否共享客户端设置连接池参数
	var maxIdleConns, maxIdleConnsPerHost, maxConnsPerHost int
	if isShared {
		maxIdleConns = httpTimeout.MaxIdleConns
		maxIdleConnsPerHost = httpTimeout.MaxIdleConnsPerHost
		maxConnsPerHost = httpTimeout.MaxConnsPerHost
	} else {
		// 禁用连接池：设置为 0，每次请求新建连接
		maxIdleConns = 0
		maxIdleConnsPerHost = 0
		maxConnsPerHost = 0
	}

	transport := &http.Transport{
		// 代理设置（HTTP/HTTPS 使用 Proxy，SOCKS5 使用 DialContext）
		Proxy: proxyFunc,

		// 连接池配置
		MaxIdleConns:        maxIdleConns,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		MaxConnsPerHost:     maxConnsPerHost,

		// 超时配置
		IdleConnTimeout:       httpTimeout.IdleConnTimeout,
		TLSHandshakeTimeout:   httpTimeout.TLSHandshakeTimeout,
		ResponseHeaderTimeout: httpTimeout.ResponseHeaderTimeout,

		// 使用拨号器（SOCKS5 或智能 DNS 拨号器）
		DialContext: wrappedDialContext,

		// HTTP 协议版本优化
		ForceAttemptHTTP2:      getForceAttemptHTTP2(httpTimeout.HTTPVersion),
		MaxResponseHeaderBytes: 10 << 20,
		WriteBufferSize:        32 << 10,
		ReadBufferSize:         32 << 10,
	}

	client := &http.Client{
		Timeout:   0, // 不设置全局超时，使用 context 超时控制
		Transport: &observedRoundTripper{base: transport, track: isShared, store: store},
	}

	if isShared {
		logger.Info("Shared HTTP client initialized",
			logger.Int("max_idle_conns", httpTimeout.MaxIdleConns),
			logger.Int("max_idle_conns_per_host", httpTimeout.MaxIdleConnsPerHost),
			logger.Int("max_conns_per_host", httpTimeout.MaxConnsPerHost),
			logger.String("proxy_type", proxyTypeToString(proxyType)),
			logger.String("http_version", httpTimeout.HTTPVersion),
		)
	}

	return client
}

// GetSharedTransport 获取全局共享的 Transport
// 用于监控连接池状态
func GetSharedTransport() *http.Transport {
	return sharedTransport
}

// ConnectionPoolStats 连接池统计信息
type ConnectionPoolStats struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	MaxConnsPerHost     int
	IdleConnTimeout     time.Duration
}

// GetConnectionPoolStats 获取连接池统计信息
func GetConnectionPoolStats() *ConnectionPoolStats {
	if sharedTransport == nil {
		return nil
	}

	return &ConnectionPoolStats{
		MaxIdleConns:        sharedTransport.MaxIdleConns,
		MaxIdleConnsPerHost: sharedTransport.MaxIdleConnsPerHost,
		MaxConnsPerHost:     sharedTransport.MaxConnsPerHost,
		IdleConnTimeout:     sharedTransport.IdleConnTimeout,
	}
}

// ResetSharedClient 重置共享客户端（用于测试或配置更新）
// 注意：这会关闭所有现有连接
func ResetSharedClient() {
	resetAt := time.Now().UTC().UnixNano()
	store := getSharedMetricsStore()
	sharedClientPreReset.Store(buildPreResetSnapshot(store))
	if sharedTransport != nil {
		sharedTransport.CloseIdleConnections()
	}
	sharedClient = nil
	sharedTransport = nil
	sharedClientOnce = sync.Once{}
	sharedClientLastResetAt.Store(resetAt)
	sharedClientResetCount.Add(1)
	sharedMetricsRef.Store(newSharedMetricsStore())
	sharedConnTracker.reset()
	connMetrics = ConnectionMetrics{}
}

// GetPerformanceMetrics 获取性能指标
func GetPerformanceMetrics() *PerformanceMetrics {
	return getSharedMetricsStore().perf
}

// GetDialMetrics 获取拨号指标
func GetDialMetrics() *DialMetrics {
	return getSharedMetricsStore().dial
}

// GetConnectionMetrics 获取共享 HTTP Client 的连接级指标
func GetConnectionMetrics() *ConnectionMetrics {
	return &connMetrics
}

// RecordRequest 记录请求指标（供外部调用）
func RecordRequest(success bool, latency time.Duration) {
	recordRequestAt(time.Now(), success, latency)
}

func recordRequestAt(now time.Time, success bool, latency time.Duration) {
	recordRequestAtWithStore(getSharedMetricsStore(), now, success, latency)
}

func recordRequestAtWithStore(store *sharedMetricsStore, now time.Time, success bool, latency time.Duration) {
	if store == nil {
		store = getSharedMetricsStore()
	}

	nowUnixNano := now.UnixNano()
	store.perf.TotalRequests.Add(1)
	store.perf.LastRequestAt.Store(nowUnixNano)
	if success {
		store.perf.SuccessRequests.Add(1)
		store.perf.LastSuccessAt.Store(nowUnixNano)
	} else {
		store.perf.FailedRequests.Add(1)
		store.perf.LastFailureAt.Store(nowUnixNano)
	}
	store.perf.TotalLatency.Add(latency.Nanoseconds())
	updateMinMaxLatency(&store.perf.MinLatency, &store.perf.MaxLatency, latency.Nanoseconds())
	store.history.recordRequestAt(now, success, latency)
	store.adaptive.RecordResult(success, latency)
}

// MetricsHistoryPoint 表示共享 HTTP Client 在一个时间桶内的聚合指标
type MetricsHistoryPoint struct {
	Timestamp time.Time
	Requests  MetricsHistorySummary
	Dials     MetricsHistorySummary
}

// MetricsHistorySnapshot 表示共享 HTTP Client 一段时间窗口内的时间序列快照
type MetricsHistorySnapshot struct {
	BucketSize time.Duration
	Window     time.Duration
	Points     []MetricsHistoryPoint
}

// SharedClientRuntimeState 表示共享 HTTP Client 的运行态元信息
type SharedClientRuntimeState struct {
	LastResetAt int64
	ResetCount  int64
	PreReset    SharedClientResetSnapshot
}

// SharedClientResetSnapshot 表示最近一次 reset 之前的共享 HTTP Client 指标快照
type SharedClientResetSnapshot struct {
	Available bool
	Requests  SharedClientMetricSnapshot
	Dials     SharedClientMetricSnapshot
}

// SharedClientMetricSnapshot 表示请求或拨号在某个时间点的累计指标快照
type SharedClientMetricSnapshot struct {
	Total        int64
	Success      int64
	Failed       int64
	TotalLatency int64
	MinLatency   int64
	MaxLatency   int64
}

func snapshotPerformanceMetrics(metrics *PerformanceMetrics) SharedClientMetricSnapshot {
	if metrics == nil {
		return SharedClientMetricSnapshot{}
	}
	return SharedClientMetricSnapshot{
		Total:        metrics.TotalRequests.Load(),
		Success:      metrics.SuccessRequests.Load(),
		Failed:       metrics.FailedRequests.Load(),
		TotalLatency: metrics.TotalLatency.Load(),
		MinLatency:   metrics.MinLatency.Load(),
		MaxLatency:   metrics.MaxLatency.Load(),
	}
}

func snapshotDialMetrics(metrics *DialMetrics) SharedClientMetricSnapshot {
	if metrics == nil {
		return SharedClientMetricSnapshot{}
	}
	return SharedClientMetricSnapshot{
		Total:        metrics.TotalDials.Load(),
		Success:      metrics.SuccessDials.Load(),
		Failed:       metrics.FailedDials.Load(),
		TotalLatency: metrics.TotalLatency.Load(),
		MinLatency:   metrics.MinLatency.Load(),
		MaxLatency:   metrics.MaxLatency.Load(),
	}
}

func buildPreResetSnapshot(store *sharedMetricsStore) SharedClientResetSnapshot {
	if store == nil {
		store = getSharedMetricsStore()
	}

	return SharedClientResetSnapshot{
		Available: true,
		Requests:  snapshotPerformanceMetrics(store.perf),
		Dials:     snapshotDialMetrics(store.dial),
	}
}

// MetricsHistorySummary 表示请求或拨号在一个时间桶内的聚合结果
type MetricsHistorySummary struct {
	Total        int64
	Success      int64
	Failed       int64
	SuccessRate  float64
	AvgLatency   time.Duration
	MinLatency   time.Duration
	MaxLatency   time.Duration
	TotalLatency time.Duration
}

type metricsHistory struct {
	mu         sync.Mutex
	bucketSize time.Duration
	maxBuckets int
	buckets    []metricsHistoryBucket
}

type metricsHistoryBucket struct {
	timestamp time.Time
	requests  metricsHistoryAggregate
	dials     metricsHistoryAggregate
}

type metricsHistoryAggregate struct {
	total        int64
	success      int64
	failed       int64
	totalLatency int64
	minLatency   int64
	maxLatency   int64
}

func newMetricsHistory(bucketSize time.Duration, maxBuckets int) *metricsHistory {
	if bucketSize <= 0 {
		bucketSize = 15 * time.Second
	}
	if maxBuckets <= 0 {
		maxBuckets = 5760
	}
	return &metricsHistory{
		bucketSize: bucketSize,
		maxBuckets: maxBuckets,
		buckets:    make([]metricsHistoryBucket, 0, maxBuckets),
	}
}

func newMetricsHistoryBucket(timestamp time.Time) metricsHistoryBucket {
	return metricsHistoryBucket{
		timestamp: timestamp.UTC(),
		requests:  newMetricsHistoryAggregate(),
		dials:     newMetricsHistoryAggregate(),
	}
}

func newMetricsHistoryAggregate() metricsHistoryAggregate {
	return metricsHistoryAggregate{
		minLatency: unsetMinLatency,
	}
}

func (h *metricsHistory) recordRequestAt(now time.Time, success bool, latency time.Duration) {
	h.recordAt(now, true, success, latency)
}

func (h *metricsHistory) recordDialAt(now time.Time, success bool, latency time.Duration) {
	h.recordAt(now, false, success, latency)
}

func (h *metricsHistory) recordAt(now time.Time, isRequest bool, success bool, latency time.Duration) {
	if h == nil {
		return
	}

	bucketStart := now.UTC().Truncate(h.bucketSize)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.trimExpiredLocked(now.UTC())
	index := h.findOrCreateBucketLocked(bucketStart)
	if index < 0 || index >= len(h.buckets) {
		return
	}

	aggregate := &h.buckets[index].dials
	if isRequest {
		aggregate = &h.buckets[index].requests
	}
	aggregate.record(success, latency)
}

func (h *metricsHistory) findOrCreateBucketLocked(bucketStart time.Time) int {
	if len(h.buckets) == 0 {
		h.buckets = append(h.buckets, newMetricsHistoryBucket(bucketStart))
		return 0
	}

	for index := len(h.buckets) - 1; index >= 0; index-- {
		if h.buckets[index].timestamp.Equal(bucketStart) {
			return index
		}
		if h.buckets[index].timestamp.Before(bucketStart) {
			insertIndex := index + 1
			if insertIndex == len(h.buckets) {
				h.buckets = append(h.buckets, newMetricsHistoryBucket(bucketStart))
				h.trimOverflowLocked()
				return len(h.buckets) - 1
			}

			if len(h.buckets) >= h.maxBuckets {
				h.buckets = h.buckets[1:]
				insertIndex--
				if insertIndex < 0 {
					insertIndex = 0
				}
			}

			h.buckets = append(h.buckets, metricsHistoryBucket{})
			copy(h.buckets[insertIndex+1:], h.buckets[insertIndex:])
			h.buckets[insertIndex] = newMetricsHistoryBucket(bucketStart)
			return insertIndex
		}
	}

	if len(h.buckets) >= h.maxBuckets {
		return -1
	}

	h.buckets = append([]metricsHistoryBucket{newMetricsHistoryBucket(bucketStart)}, h.buckets...)
	return 0
}

func (h *metricsHistory) trimOverflowLocked() {
	if len(h.buckets) <= h.maxBuckets {
		return
	}
	h.buckets = h.buckets[len(h.buckets)-h.maxBuckets:]
}

func (h *metricsHistory) trimExpiredLocked(now time.Time) {
	if len(h.buckets) == 0 {
		return
	}

	cutoff := now.Add(-h.retention())
	trimIndex := 0
	for trimIndex < len(h.buckets) && h.buckets[trimIndex].timestamp.Before(cutoff) {
		trimIndex++
	}
	if trimIndex > 0 {
		h.buckets = h.buckets[trimIndex:]
	}
}

func (h *metricsHistory) retention() time.Duration {
	return time.Duration(h.maxBuckets) * h.bucketSize
}

func (h *metricsHistory) snapshot(window time.Duration, maxPoints int) MetricsHistorySnapshot {
	snapshot := MetricsHistorySnapshot{}
	if h == nil {
		return snapshot
	}

	now := time.Now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()

	h.trimExpiredLocked(now)
	snapshot.BucketSize = h.bucketSize

	if len(h.buckets) == 0 {
		return snapshot
	}

	if window <= 0 || window > h.retention() {
		window = h.retention()
	}
	snapshot.Window = window
	cutoff := now.Add(-window)

	selectedBuckets := make([]metricsHistoryBucket, 0, len(h.buckets))
	for _, bucket := range h.buckets {
		if bucket.timestamp.Before(cutoff) {
			continue
		}
		selectedBuckets = append(selectedBuckets, bucket)
	}

	if len(selectedBuckets) == 0 {
		return snapshot
	}

	groupSize := 1
	if maxPoints > 0 && len(selectedBuckets) > maxPoints {
		groupSize = (len(selectedBuckets) + maxPoints - 1) / maxPoints
		snapshot.BucketSize = time.Duration(groupSize) * h.bucketSize
	}

	points := make([]MetricsHistoryPoint, 0, (len(selectedBuckets)+groupSize-1)/groupSize)
	for index := 0; index < len(selectedBuckets); index += groupSize {
		end := index + groupSize
		if end > len(selectedBuckets) {
			end = len(selectedBuckets)
		}

		bucket := aggregateMetricsHistoryBuckets(selectedBuckets[index:end])
		points = append(points, MetricsHistoryPoint{
			Timestamp: bucket.timestamp,
			Requests:  bucket.requests.summary(),
			Dials:     bucket.dials.summary(),
		})
	}

	snapshot.Points = points
	return snapshot
}

func (h *metricsHistory) reset() {
	if h == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.buckets = h.buckets[:0]
}

func (a *metricsHistoryAggregate) record(success bool, latency time.Duration) {
	if a == nil {
		return
	}

	a.total++
	if success {
		a.success++
	} else {
		a.failed++
	}

	latencyNs := latency.Nanoseconds()
	a.totalLatency += latencyNs
	if latencyNs < a.minLatency {
		a.minLatency = latencyNs
	}
	if latencyNs > a.maxLatency {
		a.maxLatency = latencyNs
	}
}

func (a *metricsHistoryAggregate) merge(other metricsHistoryAggregate) {
	if a == nil || other.total <= 0 {
		return
	}

	a.total += other.total
	a.success += other.success
	a.failed += other.failed
	a.totalLatency += other.totalLatency

	if other.minLatency < a.minLatency {
		a.minLatency = other.minLatency
	}
	if other.maxLatency > a.maxLatency {
		a.maxLatency = other.maxLatency
	}
}

func (a metricsHistoryAggregate) summary() MetricsHistorySummary {
	if a.total <= 0 {
		return MetricsHistorySummary{}
	}

	minLatency := a.minLatency
	if minLatency == unsetMinLatency {
		minLatency = 0
	}

	totalLatency := time.Duration(a.totalLatency)
	successRate := float64(0)
	if a.total > 0 {
		successRate = float64(a.success) / float64(a.total) * 100
	}
	return MetricsHistorySummary{
		Total:        a.total,
		Success:      a.success,
		Failed:       a.failed,
		SuccessRate:  successRate,
		AvgLatency:   time.Duration(a.totalLatency / a.total),
		MinLatency:   time.Duration(minLatency),
		MaxLatency:   time.Duration(a.maxLatency),
		TotalLatency: totalLatency,
	}
}

func aggregateMetricsHistoryBuckets(buckets []metricsHistoryBucket) metricsHistoryBucket {
	if len(buckets) == 0 {
		return newMetricsHistoryBucket(time.Time{})
	}

	aggregated := newMetricsHistoryBucket(buckets[0].timestamp)
	for _, bucket := range buckets {
		aggregated.requests.merge(bucket.requests)
		aggregated.dials.merge(bucket.dials)
	}

	return aggregated
}

// GetMetricsHistory 获取共享 HTTP Client 的最近时间序列数据
func GetMetricsHistory(window time.Duration) []MetricsHistoryPoint {
	return GetMetricsHistorySnapshot(window, 0).Points
}

// GetMetricsHistorySnapshot 获取共享 HTTP Client 的最近时间序列快照
func GetMetricsHistorySnapshot(window time.Duration, maxPoints int) MetricsHistorySnapshot {
	return getSharedMetricsStore().history.snapshot(window, maxPoints)
}

// GetMetricsHistoryBucketSize 返回时间序列的桶大小
func GetMetricsHistoryBucketSize() time.Duration {
	return getSharedMetricsStore().history.bucketSize
}

// GetMetricsHistoryRetention 返回时间序列的最大保留窗口
func GetMetricsHistoryRetention() time.Duration {
	return getSharedMetricsStore().history.retention()
}

// GetSharedClientRuntimeState 获取共享 HTTP Client 的运行态元信息
func GetSharedClientRuntimeState() SharedClientRuntimeState {
	preReset, _ := sharedClientPreReset.Load().(SharedClientResetSnapshot)
	return SharedClientRuntimeState{
		LastResetAt: sharedClientLastResetAt.Load(),
		ResetCount:  sharedClientResetCount.Load(),
		PreReset:    preReset,
	}
}

// DefaultHTTPTimeout 返回默认的 HTTP 超时配置
func DefaultHTTPTimeout() agentconfig.HTTPTimeout {
	return agentconfig.HTTPTimeout{
		DialTimeout:            30 * time.Second,
		KeepAlive:              60 * time.Second,
		FallbackDelay:          100 * time.Millisecond,
		DNSCacheTTL:            60 * time.Second,
		PreferIPv4:             true,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    50,
		MaxConnsPerHost:        100,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		UpstreamAcceptEncoding: "auto",
	}
}

// CreateOptimizedDialer 创建优化的拨号器
// 用于需要自定义拨号逻辑的场景
func CreateOptimizedDialer(cfg *agentconfig.HTTPTimeout) *net.Dialer {
	return &net.Dialer{
		Timeout:   cfg.DialTimeout,
		KeepAlive: cfg.KeepAlive,
	}
}

// createSOCKS5DialContextWithDNS 创建带 DNS 缓存的 SOCKS5 拨号器
// SOCKS5 代理通过域名连接时，DNS 解析发生在本地，然后通过代理连接
func createSOCKS5DialContextWithDNS(httpTimeout *agentconfig.HTTPTimeout, proxyCfg *agentconfig.ProxyConfig) func(ctx context.Context, network, addr string) (net.Conn, error) {
	// 创建 SOCKS5 拨号器
	socksDialer, err := CreateSOCKS5Dialer(proxyCfg, httpTimeout.DialTimeout, httpTimeout.KeepAlive)
	if err != nil {
		logger.Error("Failed to create SOCKS5 dialer", logger.Err(err))
		// 回退到直接连接
		smartDialer := dnscache.NewSmartDialer(
			httpTimeout.DialTimeout,
			httpTimeout.KeepAlive,
			httpTimeout.FallbackDelay,
			httpTimeout.DNSCacheTTL,
			httpTimeout.PreferIPv4,
		)
		return smartDialer.DialContext
	}

	logger.Info("SOCKS5 dialer created successfully")

	// 返回 DialContext 函数
	// 对于 SOCKS5 代理，直接将原始域名/IP 传递给代理服务器
	// 不进行本地 DNS 解析，利用代理服务器的 DNS 解析能力
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// 通过 SOCKS5 代理连接
		// 使用原始 addr（域名或IP），让 SOCKS5 代理服务器处理 DNS 解析
		// 这样可以确保代理服务器的网络策略和 DNS 解析被正确使用
		type result struct {
			conn net.Conn
			err  error
		}

		resultCh := make(chan *result, 1)

		go func() {
			conn, err := socksDialer.Dial(network, addr)
			resultCh <- &result{conn: conn, err: err}
		}()

		select {
		case res := <-resultCh:
			return res.conn, res.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// createSmartDialerWithDNS 创建智能 DNS 拨号器
// 使用自定义 DNS 服务器进行 DNS 解析
func createSmartDialerWithDNS(httpTimeout *agentconfig.HTTPTimeout, opts []dnscache.SmartDialerOption) func(ctx context.Context, network, addr string) (net.Conn, error) {
	smartDialer := dnscache.NewSmartDialer(
		httpTimeout.DialTimeout,
		httpTimeout.KeepAlive,
		httpTimeout.FallbackDelay,
		httpTimeout.DNSCacheTTL,
		httpTimeout.PreferIPv4,
		opts...,
	)
	return smartDialer.DialContext
}

// proxyTypeToString 将代理类型转换为字符串
func proxyTypeToString(t ProxyType) string {
	switch t {
	case ProxyTypeHTTP:
		return "HTTP"
	case ProxyTypeHTTPS:
		return "HTTPS"
	case ProxyTypeSOCKS5:
		return "SOCKS5"
	case ProxyTypeNone:
		return "NONE"
	default:
		return "UNKNOWN"
	}
}

// ═══════════════════════════════════════════════════════════════════
// 自适应超时
// ═══════════════════════════════════════════════════════════════════

// AdaptiveTimeout 自适应超时控制器
// 根据历史成功率和响应延迟动态调整超时时间
type AdaptiveTimeout struct {
	baseTimeout     time.Duration // 基础超时
	minTimeout      time.Duration // 最小超时
	maxTimeout      time.Duration // 最大超时
	successCount    atomic.Int64  // 成功计数
	failureCount    atomic.Int64  // 失败计数
	totalLatency    atomic.Int64  // 总延迟（纳秒）
	adjustThreshold int64         // 调整阈值（次数）
	smoothingFactor float64       // 平滑因子 (0-1)
}

// NewAdaptiveTimeout 创建新的自适应超时控制器
func NewAdaptiveTimeout(base, min, max time.Duration) *AdaptiveTimeout {
	return &AdaptiveTimeout{
		baseTimeout:     base,
		minTimeout:      min,
		maxTimeout:      max,
		adjustThreshold: 10,  // 每 10 次请求调整一次
		smoothingFactor: 0.2, // 20% 的平滑因子
	}
}

// GetTimeout 获取当前超时时间
func (a *AdaptiveTimeout) GetTimeout() time.Duration {
	total := a.successCount.Load() + a.failureCount.Load()
	if total == 0 {
		return a.baseTimeout
	}

	// 成功率
	successRate := float64(a.successCount.Load()) / float64(total)

	// 平均延迟
	var avgLatency time.Duration
	if a.successCount.Load() > 0 {
		avgLatency = time.Duration(a.totalLatency.Load() / a.successCount.Load())
	}

	// 根据成功率计算超时
	var targetTimeout time.Duration
	if successRate < 0.5 {
		// 成功率低，增加超时
		targetTimeout = time.Duration(float64(a.baseTimeout) * (1 + (0.5-successRate)*2))
	} else if successRate > 0.9 {
		// 成功率高，减少超时
		targetTimeout = time.Duration(float64(a.baseTimeout) * (1 - (successRate - 0.9)))
	} else {
		// 成功率适中，保持基础超时
		targetTimeout = a.baseTimeout
	}

	// 考虑平均延迟：如果延迟很高，增加超时
	if avgLatency > a.baseTimeout {
		latencyRatio := float64(avgLatency) / float64(a.baseTimeout)
		targetTimeout = time.Duration(float64(targetTimeout) * (0.5 + latencyRatio*0.5))
	}

	// 限制在 [minTimeout, maxTimeout] 范围内
	if targetTimeout < a.minTimeout {
		targetTimeout = a.minTimeout
	} else if targetTimeout > a.maxTimeout {
		targetTimeout = a.maxTimeout
	}

	return targetTimeout
}

// RecordResult 记录请求结果
func (a *AdaptiveTimeout) RecordResult(success bool, latency time.Duration) {
	if success {
		a.successCount.Add(1)
		a.totalLatency.Add(latency.Nanoseconds())
	} else {
		a.failureCount.Add(1)
	}

	// 达到调整阈值时，可以触发回调或日志
	total := a.successCount.Load() + a.failureCount.Load()
	if total%a.adjustThreshold == 0 {
		currentTimeout := a.GetTimeout()
		var avgLatency time.Duration
		if a.successCount.Load() > 0 {
			avgLatency = time.Duration(a.totalLatency.Load() / a.successCount.Load())
		}
		logger.Debug("Adaptive timeout updated",
			logger.Duration("current_timeout", currentTimeout.Nanoseconds()),
			logger.Float64("success_rate", float64(a.successCount.Load())/float64(total)),
			logger.Duration("avg_latency", avgLatency.Nanoseconds()),
		)
	}
}

// GetStats 获取统计信息
func (a *AdaptiveTimeout) GetStats() (successRate float64, avgLatency time.Duration, currentTimeout time.Duration) {
	total := a.successCount.Load() + a.failureCount.Load()
	if total == 0 {
		return 0, 0, a.baseTimeout
	}

	successRate = float64(a.successCount.Load()) / float64(total)
	if a.successCount.Load() > 0 {
		avgLatency = time.Duration(a.totalLatency.Load() / a.successCount.Load())
	}
	currentTimeout = a.GetTimeout()

	return
}

// Reset 重置自适应超时控制器
func (a *AdaptiveTimeout) Reset() {
	a.successCount.Store(0)
	a.failureCount.Store(0)
	a.totalLatency.Store(0)
}

// GetAdaptiveTimeout 获取全局自适应超时控制器
func GetAdaptiveTimeout() *AdaptiveTimeout {
	return getSharedMetricsStore().adaptive
}

// ═══════════════════════════════════════════════════════════════════
// 连接预热
// ═══════════════════════════════════════════════════════════════════

// WarmupConfig 连接预热配置
type WarmupConfig struct {
	Hosts       []string // 要预热的 host 列表（格式：example.com:443）
	HTTPS       bool     // 是否使用 HTTPS
	Concurrency int      // 并发数
	Timeout     time.Duration
}

// WarmupResult 预热结果
type WarmupResult struct {
	Success  int64
	Failed   int64
	Duration time.Duration
	Errors   []error
}

// WarmupConnections 预热连接
// 在服务启动时预先建立到指定 hosts 的连接
func WarmupConnections(cfg *WarmupConfig) *WarmupResult {
	if sharedClient == nil {
		return &WarmupResult{Failed: int64(len(cfg.Hosts)), Errors: []error{fmt.Errorf("shared client not initialized")}}
	}

	result := &WarmupResult{
		Errors: make([]error, 0),
	}

	start := time.Now()

	// 创建信号量限制并发
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup

	for _, host := range cfg.Hosts {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()

			// 获取信号量
			sem <- struct{}{}
			defer func() { <-sem }()

			// 构建请求
			scheme := "http://"
			if cfg.HTTPS {
				scheme = "https://"
			}
			url := scheme + h

			ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
			defer cancel()

			// 发送请求（HEAD 请求最小化数据传输）
			req, _ := http.NewRequestWithContext(ctx, "HEAD", url, nil)
			sharedClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // 不跟随重定向
			}

			resp, err := sharedClient.Do(req)
			if err != nil {
				result.Failed++
				result.Errors = append(result.Errors, fmt.Errorf("warmup %s: %w", h, err))
				return
			}
			resp.Body.Close()

			result.Success++
		}(host)
	}

	wg.Wait()
	result.Duration = time.Since(start)

	logger.Info("Connection warmup completed",
		logger.Int("total", len(cfg.Hosts)),
		logger.Int64("success", result.Success),
		logger.Int64("failed", result.Failed),
		logger.Duration("duration", result.Duration.Nanoseconds()),
	)

	return result
}

// DefaultWarmupConfig 获取默认的连接预热配置
func DefaultWarmupConfig() *WarmupConfig {
	return &WarmupConfig{
		Hosts: []string{
			"api.openai.com:443",
			"api.anthropic.com:443",
			"generativelanguage.googleapis.com:443",
			"api.deepseek.com:443",
		},
		HTTPS:       true,
		Concurrency: 5,
		Timeout:     5 * time.Second,
	}
}
