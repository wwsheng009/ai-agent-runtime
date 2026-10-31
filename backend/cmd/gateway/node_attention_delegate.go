package main

// node_attention_delegate.go — 网关把通知“委托”给在线节点，由节点自己的 bot
// 按本地绑定出站（docs/gateway/18 复用方案）。
//
// 为什么走 ServeProxyPinned：`/api/connectors/attention` 不在节点通用反代
// allowlist 内（只有只读连接器视图走 pinned 先例）；调用方是网关自身，路径是
// 服务端常量，不接受调用方控制。节点侧隧道 agent 会注入本机写令牌，网关不
// 持有节点密钥。
//
// 边界：只复用**发送能力**，不复制节点 connectors.json / 秘钥；节点离线时
// 返回错误，由 notify 内核回退到 Web Push / ntfy / AppPush。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// nodeAttentionPath 是节点侧注意力接收面（runtime-server/aicli 同契约）。
	nodeAttentionPath = "/api/connectors/attention"
	// nodeAttentionTimeout 是单节点委托上限（节点侧长轮询不在此路径）。
	nodeAttentionTimeout = 10 * time.Second
)

// nodeAttentionProxy 是 pinned 隧道代理的最小视图（*tunnel.Server 实现）；
// 抽成接口便于单测注入假代理。
type nodeAttentionProxy interface {
	ServeProxyPinned(w http.ResponseWriter, r *http.Request, nodeID, path string)
}

// newNodeAttentionDelegate 构造节点委托发送器；proxy 为 nil 时返回 nil
// （节点目标不参与通道面）。
func newNodeAttentionDelegate(proxy nodeAttentionProxy) func(ctx context.Context, nodeID string, notice connectors.AttentionNotice) error {
	if proxy == nil {
		return nil
	}
	return func(ctx context.Context, nodeID string, notice connectors.AttentionNotice) error {
		nodeID = strings.TrimSpace(nodeID)
		if nodeID == "" {
			return fmt.Errorf("node attention: node_id is required")
		}
		sessionID := strings.TrimSpace(notice.Session.SessionID)
		text := strings.TrimSpace(notice.Text)
		if sessionID == "" || text == "" {
			return fmt.Errorf("node attention: session_id and text are required")
		}
		sessionNode := strings.TrimSpace(notice.Session.NodeID)
		if sessionNode == "" {
			sessionNode = nodeID
		}
		payload, err := json.Marshal(map[string]any{
			"kind": strings.TrimSpace(notice.Kind),
			"session": map[string]string{
				"node_id":    sessionNode,
				"session_id": sessionID,
			},
			"text": text,
		})
		if err != nil {
			return fmt.Errorf("node attention: encode: %w", err)
		}
		callCtx, cancel := context.WithTimeout(ctx, nodeAttentionTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, "http://gateway-node.gateway"+nodeAttentionPath, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("node attention: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		recorder := &proxyRecorder{header: make(http.Header)}
		proxy.ServeProxyPinned(recorder, req, nodeID, nodeAttentionPath)
		if callCtx.Err() != nil {
			return fmt.Errorf("node attention %s: %w", nodeID, callCtx.Err())
		}
		if recorder.status == 0 {
			return fmt.Errorf("node attention %s: 节点未返回响应（代理中断）", nodeID)
		}
		if recorder.status != http.StatusOK {
			return fmt.Errorf("node attention %s: HTTP %d：%s", nodeID, recorder.status, firstResponseLine(recorder.body.String()))
		}
		return nil
	}
}
