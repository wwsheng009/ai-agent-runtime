package schedulehost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// OpScheduleDeliver 是 L2 就近投递的 mesh op（与 internal/mesh 白名单
// "schedule.deliver"、aicli 入站端点 /web/api/schedule-deliver 同源）。
const OpScheduleDeliver = "schedule.deliver"

// PeerDelivererOptions 配置 L2 投递器。
type PeerDelivererOptions struct {
	// Paths 覆盖 mesh 布局（零值 → mesh.ResolvePaths()）。
	Paths mesh.Paths
	// CallerNodeID 是本节点身份（mesh.Call 的调用方头，供审计/跨工作区判定）；
	// 必填——没有稳定身份就不做跨进程投递（fail-closed，退回后续层）。
	CallerNodeID string
	// ResolveTarget 把会话解析为目标节点。默认走 mesh.ResolveCallTarget：
	// 未找到 / 归属歧义（冲突）/ 不可调用一律返回错误——"不盲投"，由
	// LocalFirstDeliverer 降级到下一层。
	ResolveTarget func(sessionID string) (string, error)
	// Call 执行一次 mesh 调用（默认 mesh.Call）。测试注入点。
	Call func(ctx context.Context, callerID, targetNodeID string, args json.RawMessage) *mesh.CallResult
	// Logf 记录降级细节（nil → 丢弃）。
	Logf func(format string, args ...any)
}

// PeerDeliverer 是 L2 投递器：把提醒经 mesh.Call 投给会话的宿主节点。
type PeerDeliverer struct {
	paths    mesh.Paths
	callerID string
	resolve  func(sessionID string) (string, error)
	call     func(ctx context.Context, callerID, targetNodeID string, args json.RawMessage) *mesh.CallResult
	logf     func(format string, args ...any)
}

// NewPeerDeliverer 构造 L2 投递器；缺少调用方身份直接报错（fail-closed）。
func NewPeerDeliverer(opts PeerDelivererOptions) (*PeerDeliverer, error) {
	callerID := strings.TrimSpace(opts.CallerNodeID)
	if callerID == "" {
		return nil, fmt.Errorf("schedulehost: peer deliverer needs a caller node id")
	}
	paths := opts.Paths
	if !paths.Enabled() {
		paths = mesh.ResolvePaths()
	}
	p := &PeerDeliverer{paths: paths, callerID: callerID, logf: opts.Logf}
	if p.logf == nil {
		p.logf = func(string, ...any) {}
	}
	p.resolve = opts.ResolveTarget
	if p.resolve == nil {
		p.resolve = func(sessionID string) (string, error) {
			target, err := mesh.ResolveCallTarget(p.paths, sessionID)
			if err != nil {
				return "", err
			}
			return target.NodeID, nil
		}
	}
	p.call = opts.Call
	if p.call == nil {
		p.call = func(ctx context.Context, callerID, targetNodeID string, args json.RawMessage) *mesh.CallResult {
			return mesh.Call(ctx, p.paths, callerID, mesh.CallRequest{
				Target:     targetNodeID,
				Op:         OpScheduleDeliver,
				Args:       args,
				AllowWrite: true,
			})
		}
	}
	return p, nil
}

// Deliver 是 legacy 形态（无 occurrence 键，透传）。
func (p *PeerDeliverer) Deliver(ctx context.Context, sessionID, text string) error {
	return p.DeliverEnvelope(ctx, schedule.DeliveryEnvelope{SessionID: sessionID, Text: text})
}

// DeliverEnvelope 执行一次 L2 投递；实现 schedule.EnvelopeDeliverer。
func (p *PeerDeliverer) DeliverEnvelope(ctx context.Context, env schedule.DeliveryEnvelope) error {
	if p == nil {
		return fmt.Errorf("schedulehost: peer deliverer not configured")
	}
	target, err := p.resolve(env.SessionID)
	if err != nil {
		return fmt.Errorf("schedulehost: resolve peer for %s: %w", env.SessionID, err)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("schedulehost: no peer target for %s", env.SessionID)
	}
	if target == p.callerID {
		return fmt.Errorf("schedulehost: peer target is self (%s); refusing L2 self-call", target)
	}
	args, err := json.Marshal(map[string]any{
		"session_id":  env.SessionID,
		"text":        env.Text,
		"occurrences": env.Occurrences,
	})
	if err != nil {
		return fmt.Errorf("schedulehost: encode peer delivery: %w", err)
	}
	result := p.call(ctx, p.callerID, target, args)
	if result == nil {
		return fmt.Errorf("schedulehost: peer delivery to %s returned no result", target)
	}
	if !result.OK() {
		return fmt.Errorf("schedulehost: peer delivery to %s failed: status=%s code=%s message=%s",
			target, result.Status, result.Code, result.Message)
	}
	return nil
}
