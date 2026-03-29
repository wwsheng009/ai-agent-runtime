// Package dnscache 提供带缓存的 DNS 解析器，优化 IPv4/IPv6 处理
package dnscache

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// CacheEntry DNS 缓存条目
type CacheEntry struct {
	IPs       []net.IPAddr
	ExpiresAt time.Time
}

// CachedResolver 带缓存的 DNS 解析器（并发安全，防缓存穿透）
type CachedResolver struct {
	resolver *net.Resolver
	cache    map[string]*CacheEntry
	mu       sync.RWMutex
	ttl      time.Duration

	// 防止缓存穿透：正在查询的域名
	pending   map[string]*pendingQuery
	pendingMu sync.Mutex

	// 统计
	hitCount  atomic.Uint64
	missCount atomic.Uint64
}

// pendingQuery 正在进行的 DNS 查询
type pendingQuery struct {
	done   chan struct{}
	result []net.IPAddr
	err    error
}

// ResolverOption DNS 解析器选项
type ResolverOption func(*CachedResolver)

// WithDNSServer 设置自定义 DNS 服务器（如 "8.8.8.8:53"）
func WithDNSServer(dnsServer string) ResolverOption {
	return func(r *CachedResolver) {
		r.resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{}
				return d.DialContext(ctx, network, dnsServer)
			},
		}
	}
}

// NewCachedResolver 创建带缓存的 DNS 解析器
func NewCachedResolver(ttl time.Duration, opts ...ResolverOption) *CachedResolver {
	r := &CachedResolver{
		resolver: net.DefaultResolver,
		cache:    make(map[string]*CacheEntry),
		ttl:      ttl,
		pending:  make(map[string]*pendingQuery),
	}

	// 应用选项
	for _, opt := range opts {
		opt(r)
	}

	// 启动后台清理过期缓存的 goroutine
	go r.cleanupExpired()
	return r
}

// LookupIPAddr 查询 IP 地址（带缓存和防穿透）
func (r *CachedResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	startTime := time.Now()

	// 1. 检查缓存
	r.mu.RLock()
	if entry, ok := r.cache[host]; ok && time.Now().Before(entry.ExpiresAt) {
		r.mu.RUnlock()
		r.hitCount.Add(1)
		cacheAge := time.Since(entry.ExpiresAt.Add(-r.ttl))

		logger.Debug("DNS cache hit",
			logger.String("host", host),
			logger.Duration("cache_age_ms", cacheAge.Milliseconds()),
			logger.Int("ips_count", len(entry.IPs)),
			logger.Duration("resolve_time_ms", time.Since(startTime).Milliseconds()),
		)

		// 返回缓存的副本，避免外部修改
		result := make([]net.IPAddr, len(entry.IPs))
		copy(result, entry.IPs)
		return result, nil
	}
	r.mu.RUnlock()

	// 2. 防止缓存穿透：检查是否有正在进行的查询
	r.pendingMu.Lock()
	if pending, exists := r.pending[host]; exists {
		r.pendingMu.Unlock()
		logger.Debug("DNS pending query wait",
			logger.String("host", host),
		)
		// 等待正在进行的查询完成
		select {
		case <-pending.done:
			if pending.err != nil {
				logger.Warn("DNS pending query failed",
					logger.String("host", host),
					logger.Err(pending.err),
				)
				return nil, pending.err
			}
			r.hitCount.Add(1) // 等待其他查询结果也算命中
			result := make([]net.IPAddr, len(pending.result))
			copy(result, pending.result)

			logger.Debug("DNS pending query success",
				logger.String("host", host),
				logger.Int("ips_count", len(pending.result)),
				logger.Duration("resolve_time_ms", time.Since(startTime).Milliseconds()),
			)
			return result, nil
		case <-ctx.Done():
			logger.Warn("DNS pending query timeout",
				logger.String("host", host),
				logger.Err(ctx.Err()),
			)
			return nil, ctx.Err()
		}
	}

	// 创建新的 pending 查询
	pending := &pendingQuery{done: make(chan struct{})}
	r.pending[host] = pending
	r.pendingMu.Unlock()

	// 3. 执行 DNS 查询
	logger.Debug("DNS resolution started",
		logger.String("host", host),
	)
	r.missCount.Add(1)
	queryStart := time.Now()
	ips, err := r.resolver.LookupIPAddr(ctx, host)
	queryDuration := time.Since(queryStart)

	if err != nil {
		logger.Error("DNS resolution failed",
			logger.String("host", host),
			logger.Duration("duration_ms", queryDuration.Milliseconds()),
			logger.Err(err),
		)
	} else {
		var ipv4Count, ipv6Count int
		for _, ip := range ips {
			if ip.IP.To4() != nil {
				ipv4Count++
			} else {
				ipv6Count++
			}
		}

		logger.Info("DNS resolution succeeded",
			logger.String("host", host),
			logger.Duration("duration_ms", queryDuration.Milliseconds()),
			logger.Int("total_ips", len(ips)),
			logger.Int("ipv4_count", ipv4Count),
			logger.Int("ipv6_count", ipv6Count),
		)

		if len(ips) > 0 {
			ipList := make([]string, len(ips))
			for i, ip := range ips {
				ipList[i] = ip.String()
			}
			// 使用 Any 记录 IP 列表
			logger.Debug("DNS resolved IPs",
				logger.String("host", host),
				logger.Any("ips", ipList),
			)
		}
	}

	// 4. 缓存结果（成功时）
	if err == nil && len(ips) > 0 {
		r.mu.Lock()
		r.cache[host] = &CacheEntry{
			IPs:       ips,
			ExpiresAt: time.Now().Add(r.ttl),
		}
		r.mu.Unlock()

		logger.Debug("DNS cached",
			logger.String("host", host),
			logger.Duration("ttl_ms", r.ttl.Milliseconds()),
			logger.Int("ips_count", len(ips)),
		)
	}

	// 5. 更新 pending 状态并通知等待者
	pending.result = ips
	pending.err = err
	close(pending.done)

	// 6. 清理 pending 记录
	r.pendingMu.Lock()
	delete(r.pending, host)
	r.pendingMu.Unlock()

	if err != nil {
		return nil, err
	}

	// 返回副本
	result := make([]net.IPAddr, len(ips))
	copy(result, ips)
	return result, nil
}

// cleanupExpired 定期清理过期缓存
func (r *CachedResolver) cleanupExpired() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		r.mu.Lock()
		now := time.Now()
		for host, entry := range r.cache {
			if now.After(entry.ExpiresAt) {
				delete(r.cache, host)
			}
		}
		r.mu.Unlock()
	}
}

// Clear 清除所有缓存
func (r *CachedResolver) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = make(map[string]*CacheEntry)
}

// Stats 返回缓存统计信息
func (r *CachedResolver) Stats() (entries int, pendingQueries int, hits uint64, misses uint64) {
	r.mu.RLock()
	entries = len(r.cache)
	r.mu.RUnlock()

	r.pendingMu.Lock()
	pendingQueries = len(r.pending)
	r.pendingMu.Unlock()

	hits = r.hitCount.Load()
	misses = r.missCount.Load()
	return
}

// SmartDialer 智能拨号器，优化 IPv4/IPv6 处理
type SmartDialer struct {
	dialer        *net.Dialer
	resolver      *CachedResolver
	preferIPv4    bool
	fallbackDelay time.Duration

	// 统计信息
	connectCount atomic.Uint64
}

// SmartDialerOption SmartDialer 选项
type SmartDialerOption func(*SmartDialer)

// WithSmartDialerDNSServer 设置自定义 DNS 服务器
// 如果地址不包含端口，自动添加默认端口 53
// 支持格式：
//   - "8.8.8.8" -> 自动转换为 "8.8.8.8:53"
//   - "8.8.8.8:53" -> 不变
//   - "https://dns.google" -> 支持带协议的格式
func WithSmartDialerDNSServer(dnsServer string) SmartDialerOption {
	// 如果 DNS 地址不包含端口号，自动添加 :53
	if dnsServer != "" && !containsPort(dnsServer) {
		dnsServer = dnsServer + ":53"
	}
	return func(d *SmartDialer) {
		d.resolver = NewCachedResolver(d.resolver.ttl, WithDNSServer(dnsServer))
	}
}

// containsPort 检查地址是否包含端口号
func containsPort(address string) bool {
	// 检查最后是否为数字格式 :port
	i := len(address) - 1
	for i > 0 {
		if address[i] == ':' {
			// 找到冒号，检查后面是否都是数字
			for j := i + 1; j < len(address); j++ {
				if address[j] < '0' || address[j] > '9' {
					return false
				}
			}
			return true
		}
		i--
	}
	return false
}

// NewSmartDialer 创建智能拨号器
func NewSmartDialer(timeout, keepAlive, fallbackDelay, dnsCacheTTL time.Duration, preferIPv4 bool, opts ...SmartDialerOption) *SmartDialer {
	d := &SmartDialer{
		dialer: &net.Dialer{
			Timeout:   timeout,
			KeepAlive: keepAlive,
		},
		resolver:      NewCachedResolver(dnsCacheTTL),
		preferIPv4:    preferIPv4,
		fallbackDelay: fallbackDelay,
	}

	// 应用选项
	for _, opt := range opts {
		opt(d)
	}

	return d
}

// DialContext 实现 DialContext 接口
func (d *SmartDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.connectCount.Add(1)

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	// 如果是 IP 地址，直接连接
	if ip := net.ParseIP(host); ip != nil {
		return d.dialer.DialContext(ctx, network, addr)
	}

	// 解析域名（带缓存，内部已处理并发防穿透）
	ips, err := d.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}

	// 按 IPv4/IPv6 优先级排序
	ips = d.sortIPs(ips)

	// 尝试连接（带快速回退）
	return d.dialWithFallback(ctx, network, host, port, ips)
}

// dialWithFallback 尝试连接，支持快速回退
func (d *SmartDialer) dialWithFallback(ctx context.Context, network, host, port string, ips []net.IPAddr) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, &net.DNSError{Err: "no addresses found", Name: host}
	}

	// 单地址直接连接
	if len(ips) == 1 {
		return d.dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}

	// 多地址：尝试第一个，失败后快速尝试下一个
	type dialResult struct {
		conn net.Conn
		err  error
	}

	// 使用通道收集结果
	resultCh := make(chan *dialResult, len(ips))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 尝试第一个地址
	go func() {
		conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		resultCh <- &dialResult{conn: conn, err: err}
	}()

	// 等待第一个结果或超时
	select {
	case result := <-resultCh:
		if result.err == nil {
			return result.conn, nil
		}
		// 第一个失败，继续尝试其他
	case <-time.After(d.fallbackDelay):
		// 超时，启动第二个尝试
	}

	// 尝试剩余地址
	for i := 1; i < len(ips); i++ {
		conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(ips[i].String(), port))
		if err == nil {
			cancel() // 取消其他尝试
			return conn, nil
		}
	}

	// 等待第一个结果
	select {
	case result := <-resultCh:
		if result.conn != nil {
			return result.conn, nil
		}
		if result.err != nil {
			return nil, result.err
		}
	default:
	}

	return nil, &net.DNSError{Err: "all addresses failed", Name: host}
}

// isIPv6LocalAddress 检查 IPv6 地址是否为本地地址（需要过滤）
// 包括：fc00::/7 (ULA), fe80::/10 (Link-Local), ::1 (Loopback)
func isIPv6LocalAddress(ip net.IP) bool {
	if ip.To4() != nil {
		return false
	}

	// 检查是否为 IPv6
	if len(ip) != net.IPv6len {
		return false
	}

	// 检查 fc00::/7 (ULA - Unique Local Addresses)
	// fc00:: 到 fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff
	if ip[0]&0xfe == 0xfc {
		return true
	}

	// 检查 fe80::/10 (Link-Local)
	if ip[0] == 0xfe && (ip[1]&0xc0) == 0x80 {
		return true
	}

	// 检查 ::1 (Loopback)
	if ip.IsLoopback() {
		return true
	}

	return false
}

// sortIPs 根据 preferIPv4 设置对 IP 地址排序，并过滤 IPv6 本地地址
func (d *SmartDialer) sortIPs(ips []net.IPAddr) []net.IPAddr {
	if len(ips) <= 1 {
		return ips
	}

	var ipv4, ipv6 []net.IPAddr
	var filteredIPv6 []string
	originalCount := len(ips)

	for _, ip := range ips {
		if ip.IP.To4() != nil {
			ipv4 = append(ipv4, ip)
		} else {
			// 过滤 IPv6 本地地址
			if !isIPv6LocalAddress(ip.IP) {
				ipv6 = append(ipv6, ip)
			} else {
				filteredIPv6 = append(filteredIPv6, ip.IP.String())
			}
		}
	}

	// 记录 IPv6 过滤情况
	if len(filteredIPv6) > 0 {
		logger.Warn("IPv6 local addresses filtered",
			logger.Any("filtered_addresses", filteredIPv6),
			logger.Int("filtered_count", len(filteredIPv6)),
			logger.Int("original_total", originalCount),
		)
	}

	if len(ipv4)+len(ipv6) < originalCount {
		logger.Debug("IP address sorting result",
			logger.Int("ipv4_count", len(ipv4)),
			logger.Int("ipv6_count", len(ipv6)),
			logger.Int("filtered_ipv6", len(filteredIPv6)),
			logger.Bool("prefer_ipv4", d.preferIPv4),
		)
	}

	// 如果优先 IPv4，把 IPv4 放前面
	if d.preferIPv4 {
		return append(ipv4, ipv6...)
	}
	// 否则保持原顺序（通常 IPv6 优先）
	return append(ipv6, ipv4...)
}

// Stats 返回拨号器统计信息
func (d *SmartDialer) Stats() (connects, dnsHits, dnsMisses uint64) {
	_, _, hits, misses := d.resolver.Stats()
	return d.connectCount.Load(), hits, misses
}
