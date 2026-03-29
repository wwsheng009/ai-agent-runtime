package httpclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestGetSharedHTTPClient(t *testing.T) {
	// 重置以确保干净状态
	ResetSharedClient()

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: agentconfig.HTTPTimeout{
				DialTimeout:           30 * time.Second,
				KeepAlive:             60 * time.Second,
				FallbackDelay:         300 * time.Millisecond,
				DNSCacheTTL:           60 * time.Second,
				PreferIPv4:            false,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   50,
				MaxConnsPerHost:       100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}

	client := GetSharedHTTPClient(cfg)
	if client == nil {
		t.Error("GetSharedHTTPClient() returned nil")
		return
	}

	// 验证 Transport 已配置
	if client.Transport == nil {
		t.Error("GetSharedHTTPClient() returned client with nil Transport")
	}
}

func TestGetSharedHTTPClientSingleton(t *testing.T) {
	// 重置以确保干净状态
	ResetSharedClient()

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	client1 := GetSharedHTTPClient(cfg)
	client2 := GetSharedHTTPClient(cfg)

	if client1 != client2 {
		t.Error("GetSharedHTTPClient() should return the same instance (singleton)")
	}
}

func TestGetSharedHTTPClientConcurrent(t *testing.T) {
	// 重置以确保干净状态
	ResetSharedClient()

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	const goroutines = 100
	clients := make([]*http.Client, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			_ = GetSharedHTTPClient(cfg)
		}(i)
	}

	wg.Wait()

	// 验证最终只有一个实例
	client1 := GetSharedHTTPClient(cfg)
	client2 := GetSharedHTTPClient(cfg)
	if client1 != client2 {
		t.Error("Concurrent GetSharedHTTPClient() should return the same instance")
	}
	_ = clients // avoid unused variable warning
}

func TestGetSharedTransport(t *testing.T) {
	// 重置以确保干净状态
	ResetSharedClient()

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	// 在获取客户端之前，transport 应该是 nil
	transport := GetSharedTransport()
	if transport != nil {
		t.Error("GetSharedTransport() should return nil before GetSharedHTTPClient() is called")
	}

	// 获取客户端初始化共享 transport
	_ = GetSharedHTTPClient(cfg)

	// 现在 transport 应该不为 nil
	transport = GetSharedTransport()
	if transport == nil {
		t.Error("GetSharedTransport() returned nil after GetSharedHTTPClient() was called")
	}
}

func TestGetConnectionPoolStats(t *testing.T) {
	// 重置以确保干净状态
	ResetSharedClient()

	// 在初始化之前应该返回 nil
	stats := GetConnectionPoolStats()
	if stats != nil {
		t.Error("GetConnectionPoolStats() should return nil before initialization")
	}

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: agentconfig.HTTPTimeout{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 100,
				MaxConnsPerHost:     150,
				IdleConnTimeout:     120 * time.Second,
			},
		},
	}

	_ = GetSharedHTTPClient(cfg)

	stats = GetConnectionPoolStats()
	if stats == nil {
		t.Error("GetConnectionPoolStats() returned nil after initialization")
		return
	}

	if stats.MaxIdleConns != 200 {
		t.Errorf("MaxIdleConns = %d, want 200", stats.MaxIdleConns)
	}
	if stats.MaxIdleConnsPerHost != 100 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 100", stats.MaxIdleConnsPerHost)
	}
	if stats.MaxConnsPerHost != 150 {
		t.Errorf("MaxConnsPerHost = %d, want 150", stats.MaxConnsPerHost)
	}
	if stats.IdleConnTimeout != 120*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 120s", stats.IdleConnTimeout)
	}
}

func TestResetSharedClient(t *testing.T) {
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	// 获取初始客户端
	client1 := GetSharedHTTPClient(cfg)
	RecordRequest(true, 100*time.Millisecond)

	// 重置
	ResetSharedClient()

	// 获取新客户端（应该是新实例）
	client2 := GetSharedHTTPClient(cfg)

	// client1 和 client2 应该是不同的实例
	// 注意：这里无法直接比较指针，因为 client1 可能仍指向旧的内存
	// 但我们可以验证 transport 是新的
	transport1 := client1.Transport
	transport2 := client2.Transport
	if transport1 == transport2 {
		t.Error("After ResetSharedClient(), transport should be different")
	}
	if history := GetMetricsHistory(5 * time.Minute); len(history) != 0 {
		t.Errorf("expected metrics history to be cleared, got %d buckets", len(history))
	}
}

func TestResetSharedClientTracksRuntimeState(t *testing.T) {
	ResetSharedClient()
	RecordRequest(true, 120*time.Millisecond)
	recordDialResultAt(time.Now(), true, 80*time.Millisecond)

	before := GetSharedClientRuntimeState()
	time.Sleep(time.Millisecond)
	ResetSharedClient()
	after := GetSharedClientRuntimeState()

	if after.ResetCount != before.ResetCount+1 {
		t.Fatalf("reset count = %d, want %d", after.ResetCount, before.ResetCount+1)
	}
	if after.LastResetAt <= before.LastResetAt {
		t.Fatalf("last reset at = %d, want > %d", after.LastResetAt, before.LastResetAt)
	}
	if !after.PreReset.Available {
		t.Fatalf("expected pre-reset snapshot to be available")
	}
	if after.PreReset.Requests.Total != 1 {
		t.Fatalf("pre-reset requests.total = %d, want 1", after.PreReset.Requests.Total)
	}
	if after.PreReset.Dials.Total != 1 {
		t.Fatalf("pre-reset dials.total = %d, want 1", after.PreReset.Dials.Total)
	}
}

func TestDefaultHTTPTimeout(t *testing.T) {
	cfg := DefaultHTTPTimeout()

	if cfg.DialTimeout != 30*time.Second {
		t.Errorf("DialTimeout = %v, want 30s", cfg.DialTimeout)
	}
	if cfg.KeepAlive != 60*time.Second {
		t.Errorf("KeepAlive = %v, want 60s", cfg.KeepAlive)
	}
	if cfg.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100", cfg.MaxIdleConns)
	}
	if cfg.MaxIdleConnsPerHost != 50 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 50", cfg.MaxIdleConnsPerHost)
	}
	if cfg.MaxConnsPerHost != 100 {
		t.Errorf("MaxConnsPerHost = %d, want 100", cfg.MaxConnsPerHost)
	}
}

func TestCreateOptimizedDialer(t *testing.T) {
	cfg := &agentconfig.HTTPTimeout{
		DialTimeout: 10 * time.Second,
		KeepAlive:   30 * time.Second,
	}

	dialer := CreateOptimizedDialer(cfg)
	if dialer == nil {
		t.Error("CreateOptimizedDialer() returned nil")
		return
	}

	if dialer.Timeout != cfg.DialTimeout {
		t.Errorf("Dialer.Timeout = %v, want %v", dialer.Timeout, cfg.DialTimeout)
	}
	if dialer.KeepAlive != cfg.KeepAlive {
		t.Errorf("Dialer.KeepAlive = %v, want %v", dialer.KeepAlive, cfg.KeepAlive)
	}
}

func TestSharedHTTPClientRecordsRequestAndDialMetrics(t *testing.T) {
	ResetSharedClient()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	client := GetSharedHTTPClient(cfg)
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("shared client get failed: %v", err)
	}
	_ = resp.Body.Close()

	perf := GetPerformanceMetrics()
	if got := perf.TotalRequests.Load(); got != 1 {
		t.Fatalf("TotalRequests = %d, want 1", got)
	}
	if got := perf.SuccessRequests.Load(); got != 1 {
		t.Fatalf("SuccessRequests = %d, want 1", got)
	}
	if got := perf.FailedRequests.Load(); got != 0 {
		t.Fatalf("FailedRequests = %d, want 0", got)
	}
	if got := perf.InFlightRequests.Load(); got != 0 {
		t.Fatalf("InFlightRequests = %d, want 0", got)
	}
	if got := perf.TotalLatency.Load(); got <= 0 {
		t.Fatalf("TotalLatency = %d, want > 0", got)
	}
	if got := perf.LastRequestAt.Load(); got <= 0 {
		t.Fatalf("LastRequestAt = %d, want > 0", got)
	}
	if got := perf.LastSuccessAt.Load(); got <= 0 {
		t.Fatalf("LastSuccessAt = %d, want > 0", got)
	}

	dials := GetDialMetrics()
	if got := dials.TotalDials.Load(); got != 1 {
		t.Fatalf("TotalDials = %d, want 1", got)
	}
	if got := dials.SuccessDials.Load(); got != 1 {
		t.Fatalf("SuccessDials = %d, want 1", got)
	}
	if got := dials.FailedDials.Load(); got != 0 {
		t.Fatalf("FailedDials = %d, want 0", got)
	}
	if got := dials.TotalLatency.Load(); got <= 0 {
		t.Fatalf("Dial TotalLatency = %d, want > 0", got)
	}
}

func TestSharedHTTPClientTracksConnectionConcurrencyUntilBodyClose(t *testing.T) {
	ResetSharedClient()

	releaseBody := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-releaseBody
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	client := GetSharedHTTPClient(cfg)
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("shared client get failed: %v", err)
	}

	perf := GetPerformanceMetrics()
	if got := perf.InFlightRequests.Load(); got != 1 {
		t.Fatalf("InFlightRequests before body close = %d, want 1", got)
	}

	connections := GetConnectionMetrics()
	if got := connections.BusyConns.Load(); got != 1 {
		t.Fatalf("BusyConns before body close = %d, want 1", got)
	}
	if got := connections.OpenConns.Load(); got < 1 {
		t.Fatalf("OpenConns before body close = %d, want >= 1", got)
	}

	close(releaseBody)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}

	if got := perf.InFlightRequests.Load(); got != 0 {
		t.Fatalf("InFlightRequests after body close = %d, want 0", got)
	}
	if got := connections.BusyConns.Load(); got != 0 {
		t.Fatalf("BusyConns after body close = %d, want 0", got)
	}
	if got := connections.TotalCreated.Load(); got < 1 {
		t.Fatalf("TotalCreated = %d, want >= 1", got)
	}
	if got := connections.PeakBusyConns.Load(); got < 1 {
		t.Fatalf("PeakBusyConns = %d, want >= 1", got)
	}
}

func TestResetSharedClientKeepsInFlightNonNegativeWhenOldBodyCloses(t *testing.T) {
	ResetSharedClient()

	releaseBody := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-releaseBody
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	client := GetSharedHTTPClient(cfg)
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("shared client get failed: %v", err)
	}

	if got := GetPerformanceMetrics().InFlightRequests.Load(); got != 1 {
		t.Fatalf("InFlightRequests before reset = %d, want 1", got)
	}

	ResetSharedClient()

	if got := GetPerformanceMetrics().InFlightRequests.Load(); got != 0 {
		t.Fatalf("InFlightRequests immediately after reset = %d, want 0", got)
	}

	close(releaseBody)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}

	perf := GetPerformanceMetrics()
	if got := perf.InFlightRequests.Load(); got != 0 {
		t.Fatalf("InFlightRequests after old body close = %d, want 0", got)
	}
	if got := perf.TotalRequests.Load(); got != 0 {
		t.Fatalf("TotalRequests after reset = %d, want 0", got)
	}
}

func TestResetSharedClientIgnoresCompletionOfPreResetRequest(t *testing.T) {
	ResetSharedClient()

	requestStarted := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		<-releaseResponse
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()

	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	client := GetSharedHTTPClient(cfg)
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := client.Get(server.URL)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach server before reset")
	}

	ResetSharedClient()
	close(releaseResponse)

	var resp *http.Response
	select {
	case err := <-errCh:
		t.Fatalf("shared client get failed: %v", err)
	case resp = <-respCh:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not complete after release")
	}

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain response body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}

	perf := GetPerformanceMetrics()
	if got := perf.TotalRequests.Load(); got != 0 {
		t.Fatalf("TotalRequests after old request completion = %d, want 0", got)
	}
	if got := perf.SuccessRequests.Load(); got != 0 {
		t.Fatalf("SuccessRequests after old request completion = %d, want 0", got)
	}
	if got := perf.InFlightRequests.Load(); got != 0 {
		t.Fatalf("InFlightRequests after old request completion = %d, want 0", got)
	}
}

func TestGetMetricsHistory(t *testing.T) {
	ResetSharedClient()

	now := time.Now().UTC().Truncate(GetMetricsHistoryBucketSize())
	recordRequestAt(now.Add(-GetMetricsHistoryBucketSize()+2*time.Second), true, 120*time.Millisecond)
	recordRequestAt(now.Add(-GetMetricsHistoryBucketSize()+5*time.Second), false, 240*time.Millisecond)
	recordDialResultAt(now.Add(-GetMetricsHistoryBucketSize()+6*time.Second), true, 80*time.Millisecond)
	recordRequestAt(now.Add(2*time.Second), true, 360*time.Millisecond)

	points := GetMetricsHistory(5 * time.Minute)
	if len(points) != 2 {
		t.Fatalf("expected 2 history points, got %d", len(points))
	}

	first := points[0]
	if got := first.Requests.Total; got != 2 {
		t.Fatalf("first bucket requests.total = %d, want 2", got)
	}
	if got := first.Requests.Success; got != 1 {
		t.Fatalf("first bucket requests.success = %d, want 1", got)
	}
	if got := first.Requests.Failed; got != 1 {
		t.Fatalf("first bucket requests.failed = %d, want 1", got)
	}
	if got := first.Requests.AvgLatency; got != 180*time.Millisecond {
		t.Fatalf("first bucket requests.avg = %v, want 180ms", got)
	}
	if got := first.Dials.Total; got != 1 {
		t.Fatalf("first bucket dials.total = %d, want 1", got)
	}
	if got := first.Dials.MaxLatency; got != 80*time.Millisecond {
		t.Fatalf("first bucket dials.max = %v, want 80ms", got)
	}

	second := points[1]
	if got := second.Requests.Total; got != 1 {
		t.Fatalf("second bucket requests.total = %d, want 1", got)
	}
	if got := second.Requests.AvgLatency; got != 360*time.Millisecond {
		t.Fatalf("second bucket requests.avg = %v, want 360ms", got)
	}
}

func TestGetMetricsHistorySnapshotAggregatesLongWindow(t *testing.T) {
	history := newMetricsHistory(15*time.Second, 5760)
	base := time.Now().UTC().Truncate(15 * time.Second).Add(-2 * time.Hour)

	for index := 0; index < 300; index++ {
		pointTime := base.Add(time.Duration(index) * 15 * time.Second)
		history.recordRequestAt(pointTime, true, 100*time.Millisecond)
		history.recordDialAt(pointTime, true, 50*time.Millisecond)
	}

	snapshot := history.snapshot(3*time.Hour, 60)
	if got := len(snapshot.Points); got > 60 {
		t.Fatalf("expected at most 60 points, got %d", got)
	}
	if snapshot.BucketSize <= 15*time.Second {
		t.Fatalf("expected aggregated bucket size > 15s, got %v", snapshot.BucketSize)
	}
	if snapshot.Window != 3*time.Hour {
		t.Fatalf("window = %v, want 3h", snapshot.Window)
	}

	totalRequests := int64(0)
	totalDials := int64(0)
	for _, point := range snapshot.Points {
		totalRequests += point.Requests.Total
		totalDials += point.Dials.Total
	}
	if totalRequests != 300 {
		t.Fatalf("aggregated requests total = %d, want 300", totalRequests)
	}
	if totalDials != 300 {
		t.Fatalf("aggregated dials total = %d, want 300", totalDials)
	}
}

// 基准测试

func BenchmarkGetSharedHTTPClient(b *testing.B) {
	ResetSharedClient()
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetSharedHTTPClient(cfg)
	}
}

func BenchmarkGetSharedHTTPClientConcurrent(b *testing.B) {
	ResetSharedClient()
	cfg := &agentconfig.Config{
		Providers: agentconfig.ProvidersConfig{
			HTTPTimeout: DefaultHTTPTimeout(),
		},
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = GetSharedHTTPClient(cfg)
		}
	})
}
