package main

// node_connector_handoff.go — 网关从在线节点复制 QQ/微信 bot 配置并启动
// outbound-only 副本（docs/gateway/18 的 C2 迁移形态）。
//
// 设计边界：
//   - 复制 provider/settings/secrets（+微信 context_tokens 状态），不复制节点
//     的 bindings/policy/session；网关副本用自己的 Bot 通道 target。
//   - 副本 RuntimeMode=outbound：只 Send，不 Activate/不轮询/不建 WS/不注册
//     webhook，节点继续负责 agent 入站/命令/回复。
//   - 导入成功后调用节点 handoff/confirm，节点关闭该连接器的本地 attention；
//     失败删除网关副本，节点保持原样。
//   - 密钥只经 pinned 隧道内存传输，不写日志、不回浏览器。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	nodeHandoffPath         = "/api/connectors/%s/handoff"
	nodeHandoffConfirmPath  = "/api/connectors/%s/handoff/confirm"
	nodeHandoffRollbackPath = "/api/connectors/%s/handoff/rollback"
	nodeHandoffStatePath    = "/api/connectors/%s/state/context-tokens"
	nodeHandoffHeader       = "X-AICLI-Handoff"
	nodeHandoffTimeout      = 15 * time.Second
	wechatContextKey        = "context_tokens"
)

type nodeHandoffBinding struct {
	Chat      string `json:"chat"`
	SessionID string `json:"session_id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
}

type nodeHandoffPayload struct {
	ID       string               `json:"id"`
	Provider string               `json:"provider"`
	Name     string               `json:"name"`
	Mode     string               `json:"mode"`
	Settings map[string]string    `json:"settings"`
	Secrets  map[string]string    `json:"secrets"`
	State    map[string]string    `json:"state"`
	Bindings []nodeHandoffBinding `json:"bindings"`
}

// handleNodeConnectorHandoff 是 owner 会话保护的导入入口。
func handleNodeConnectorHandoff(server nodeAttentionProxy, service *connectors.Service, logf func(string, ...any)) http.HandlerFunc {
	if logf == nil {
		logf = log.Printf
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "handoff import is POST-only"})
			return
		}
		if server == nil || service == nil || service.ConnectorStore() == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "connectors are not wired"})
			return
		}
		nodeID := strings.TrimSpace(mux.Vars(r)["node_id"])
		connectorID := strings.TrimSpace(mux.Vars(r)["connector_id"])
		if nodeID == "" || connectorID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "node_id and connector_id are required"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), nodeHandoffTimeout)
		defer cancel()
		payload, err := fetchNodeHandoff(ctx, server, nodeID, connectorID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		if !handoffProviderSupported(payload.Provider) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "only qqbot/wechat connectors can be copied, got " + payload.Provider})
			return
		}
		if _, ok := service.Provider(payload.Provider); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider is not registered on gateway: " + payload.Provider})
			return
		}

		store := service.ConnectorStore()
		id := importedConnectorID(nodeID, connectorID)
		now := time.Now().UTC()
		existing, exists := store.GetConfig(id)
		if exists {
			if !strings.EqualFold(existing.OriginNodeID, nodeID) || !strings.EqualFold(existing.OriginConnectorID, connectorID) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "connector id collision: " + id})
				return
			}
		}
		cfg := connectors.Config{
			ID:                id,
			Provider:          strings.TrimSpace(payload.Provider),
			Name:              "网关副本 · " + firstNonEmpty(strings.TrimSpace(payload.Name), connectorID),
			Enabled:           true,
			Mode:              strings.TrimSpace(payload.Mode),
			RuntimeMode:       "outbound",
			OriginNodeID:      nodeID,
			OriginConnectorID: connectorID,
			CreatedAt:         now,
			UpdatedAt:         now,
			Settings:          cloneHandoffMap(payload.Settings),
			Secrets:           cloneHandoffMap(payload.Secrets),
			Policy:            connectors.Policy{CommandPrefix: "/"},
		}
		if exists {
			cfg.CreatedAt = existing.CreatedAt
		}

		// 先停掉可能存在的旧实例（例如上一次复制后误改成 polling），再落盘。
		service.StopConnector(id)
		if err := store.PutConfig(cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save imported connector: " + err.Error()})
			return
		}
		if strings.EqualFold(cfg.Provider, "wechat") {
			if raw := strings.TrimSpace(payload.State[wechatContextKey]); raw != "" {
				if err := store.PollingState(id).Set(wechatContextKey, raw); err != nil {
					logf("node handoff: import wechat context_tokens %s: %v", id, err)
				}
			}
		}
		// outbound 模式在 startConnector 内跳过 Activate/轮询；Reload 只负责
		// 清理旧运行态并记录新配置。
		if err := service.ReloadConnector(r.Context(), id); err != nil {
			logf("node handoff: reload outbound connector %s: %v", id, err)
		}

		// 只有网关副本落盘成功后才关闭节点本地通知；失败必须回滚节点状态，
		// 不能出现“两边都不发”。
		confirmCtx, confirmCancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer confirmCancel()
		if err := confirmNodeHandoff(confirmCtx, server, nodeID, connectorID); err != nil {
			if exists {
				_ = store.PutConfig(existing)
			} else {
				_ = store.DeleteConfig(id)
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "gateway import rolled back: " + err.Error()})
			return
		}
		logf("node handoff: imported %s from %s/%s provider=%s mode=outbound", id, nodeID, connectorID, cfg.Provider)

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           true,
			"connector_id": id,
			"provider":     cfg.Provider,
			"name":         cfg.Name,
			"runtime_mode": cfg.RuntimeMode,
			"origin": map[string]string{
				"node_id":      nodeID,
				"connector_id": connectorID,
			},
			"bindings": payload.Bindings,
		})
	}
}

// handleNodeConnectorHandoffRollback 恢复节点本地通知（删除网关副本前调用）。
func handleNodeConnectorHandoffRollback(proxy nodeAttentionProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "handoff rollback is POST-only"})
			return
		}
		if proxy == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "node proxy is not wired"})
			return
		}
		nodeID := strings.TrimSpace(mux.Vars(r)["node_id"])
		connectorID := strings.TrimSpace(mux.Vars(r)["connector_id"])
		if nodeID == "" || connectorID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "node_id and connector_id are required"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), nodeHandoffTimeout)
		defer cancel()
		status, body, err := proxyNodeHandoff(ctx, proxy, nodeID, fmt.Sprintf(nodeHandoffRollbackPath, connectorID), http.MethodPost)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		if status != http.StatusOK {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": fmt.Sprintf("node rollback: HTTP %d: %s", status, firstResponseLine(string(body)))})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handoffProviderSupported(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "qqbot", "wechat":
		return true
	default:
		return false
	}
}

func importedConnectorID(nodeID, connectorID string) string {
	nodePart := sanitizeConnectorIDPart(nodeID)
	if len(nodePart) > 8 {
		nodePart = nodePart[len(nodePart)-8:]
	}
	connPart := sanitizeConnectorIDPart(connectorID)
	if connPart == "" {
		connPart = "bot"
	}
	id := "gw-" + nodePart + "-" + connPart
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

func sanitizeConnectorIDPart(in string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(in) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func cloneHandoffMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func fetchNodeHandoff(ctx context.Context, proxy nodeAttentionProxy, nodeID, connectorID string) (nodeHandoffPayload, error) {
	var payload nodeHandoffPayload
	status, body, err := proxyNodeHandoff(ctx, proxy, nodeID, fmt.Sprintf(nodeHandoffPath, connectorID), http.MethodPost)
	if err != nil {
		return payload, err
	}
	if status != http.StatusOK {
		return payload, fmt.Errorf("node handoff %s/%s: HTTP %d: %s", nodeID, connectorID, status, firstResponseLine(string(body)))
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return payload, fmt.Errorf("node handoff decode: %w", err)
	}
	return payload, nil
}

func confirmNodeHandoff(ctx context.Context, proxy nodeAttentionProxy, nodeID, connectorID string) error {
	status, body, err := proxyNodeHandoff(ctx, proxy, nodeID, fmt.Sprintf(nodeHandoffConfirmPath, connectorID), http.MethodPost)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("node handoff confirm %s/%s: HTTP %d: %s", nodeID, connectorID, status, firstResponseLine(string(body)))
	}
	return nil
}

func fetchNodeContextTokens(ctx context.Context, proxy nodeAttentionProxy, nodeID, connectorID string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, body, err := proxyNodeHandoff(callCtx, proxy, nodeID, fmt.Sprintf(nodeHandoffStatePath, connectorID), http.MethodGet)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("node context_tokens %s/%s: HTTP %d: %s", nodeID, connectorID, status, firstResponseLine(string(body)))
	}
	var payload struct {
		ContextTokens string `json:"context_tokens"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("node context_tokens decode: %w", err)
	}
	return strings.TrimSpace(payload.ContextTokens), nil
}

// proxyNodeHandoff 走 ServeProxyPinned；调用方是网关自身，路径由服务端常量
// 拼接，不接受浏览器控制的路径。
func proxyNodeHandoff(ctx context.Context, proxy nodeAttentionProxy, nodeID, path, method string) (int, []byte, error) {
	if proxy == nil {
		return 0, nil, fmt.Errorf("node proxy is not wired")
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://gateway-node.gateway"+path, http.NoBody)
	if err != nil {
		return 0, nil, fmt.Errorf("build node handoff request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(nodeHandoffHeader, "1")
	recorder := &proxyRecorder{header: make(http.Header)}
	proxy.ServeProxyPinned(recorder, req, nodeID, path)
	if ctx.Err() != nil {
		return recorder.status, recorder.body.Bytes(), ctx.Err()
	}
	if recorder.status == 0 {
		return 0, nil, fmt.Errorf("node handoff %s: no response (proxy interrupted)", path)
	}
	return recorder.status, recorder.body.Bytes(), nil
}

// syncImportedWeChatState 周期同步微信 context_token。微信 Send 必须携带
// 该 chat 最近一次入站的 context_token；节点继续轮询入站，token 会更新，
// 网关副本只出站，因此需要这个单向状态同步（不涉及消息投递）。
func syncImportedWeChatState(ctx context.Context, store connectors.Store, proxy nodeAttentionProxy, logf func(string, ...any)) {
	if store == nil || proxy == nil {
		return
	}
	if logf == nil {
		logf = log.Printf
	}
	const interval = 20 * time.Second
	last := make(map[string]string)
	failing := make(map[string]string)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for _, cfg := range store.ListConfigs() {
			if !wechatSyncEligible(cfg) {
				continue
			}
			raw, err := fetchNodeContextTokens(ctx, proxy, cfg.OriginNodeID, cfg.OriginConnectorID)
			if err != nil {
				if failing[cfg.ID] != err.Error() {
					logf("wechat context sync %s (%s/%s): %v", cfg.ID, cfg.OriginNodeID, cfg.OriginConnectorID, err)
					failing[cfg.ID] = err.Error()
				}
				continue
			}
			delete(failing, cfg.ID)
			if strings.TrimSpace(raw) == "" || last[cfg.ID] == raw {
				continue
			}
			if err := store.PollingState(cfg.ID).Set(wechatContextKey, raw); err != nil {
				logf("wechat context sync %s: save: %v", cfg.ID, err)
				continue
			}
			last[cfg.ID] = raw
			logf("wechat context synced: connector=%s source=%s/%s", cfg.ID, cfg.OriginNodeID, cfg.OriginConnectorID)
		}
	}
}

// wechatSyncEligible 报告该连接器是否需要从源节点同步微信 context_token。
//
// 只有「只出站副本」需要：它自己不轮询入站，token 只能来自仍在轮询的节点。
// 「网关自持」（runtime_mode=gateway）的 token 由本机轮询产出并持久化，若还去
// 同步节点状态就会把本机刚产出的新 token 覆盖成节点的旧值（节点离线时是空值）。
func wechatSyncEligible(cfg connectors.Config) bool {
	if !cfg.Enabled || !strings.EqualFold(strings.TrimSpace(cfg.Provider), "wechat") {
		return false
	}
	if !cfg.IsOutboundOnly() {
		return false
	}
	return strings.TrimSpace(cfg.OriginNodeID) != "" && strings.TrimSpace(cfg.OriginConnectorID) != ""
}
