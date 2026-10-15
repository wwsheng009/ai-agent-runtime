package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// statusServer 暴露 loopback 运维端点（D3 + P6）：
//
//	GET /healthz —— 存储可达 + 运行中
//	GET /status  —— 租约、模式、runtime-server URL
//	GET /metrics —— 投递成功/失败/重试/内嵌/死信累计（P6 观测收敛）
type statusServer struct {
	store         *schedule.SQLiteStore
	leader        *schedulehost.Leader
	mode          string
	runtimeServer string
	startedAt     time.Time
}

// Serve 在 127.0.0.1:port 上启动运维端点；返回关闭函数。
func (s *statusServer) Serve(port int) (func(), error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		healthy := s.store != nil
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":     healthy,
			"uptime": time.Since(s.startedAt).Round(time.Second).String(),
		})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// S3（§4.1）：在岗三态快照（与 /schedule 面板、create 回执同口径）。
		presence := schedulehost.ProbePresence(schedule.SupportedSchemaVersion())
		payload := map[string]interface{}{
			"mode":           s.mode,
			"runtime_server": s.runtimeServer,
			"started_at":     s.startedAt.UTC().Format(time.RFC3339),
			"uptime":         time.Since(s.startedAt).Round(time.Second).String(),
			"store_schema":   presence.StoreSchema,
			"presence":       presence,
		}
		// S4b：账本对账快照（任务状态 / pending 兜底 / 交付账本条目）。
		if s.store != nil {
			if stats, err := s.store.StoreStats(); err == nil {
				payload["stats"] = stats
			}
		}
		if s.leader != nil {
			driver := s.leader.Status()
			payload["driver"] = driver
			payload["metrics"] = driver.Metrics
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen status endpoint: %w", err)
	}
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}, nil
}
