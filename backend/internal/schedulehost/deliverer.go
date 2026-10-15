package schedulehost

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// LocalFirstOptions 是组合式投递器的三层依赖（就近优先，见联盟设计 §3.3）。
// 至少一层非空；L4（embedded）/L5（mailbox）由 schedule.Runtime 兜底，不在这里。
type LocalFirstOptions struct {
	// Local 是 L1：本进程 session hub（driver 就是会话宿主时零网络直达）。
	Local schedule.Deliverer
	// Peer 是 L2：目标宿主节点（mesh.Call；S2b 起接入）。
	Peer schedule.Deliverer
	// HTTP 是 L3：跨进程/跨机 delegate（runtime-server 命令端点）。
	HTTP schedule.Deliverer
	// Logf 记录降级路径（nil → 丢弃）。
	Logf func(format string, args ...any)
}

// LocalFirstDeliverer 按 L1 → L2 → L3 顺序尝试投递，全部失败才返回错误
// （错误聚合各层原因，便于排障）。
type LocalFirstDeliverer struct {
	stages []deliveryStage
	logf   func(format string, args ...any)
}

type deliveryStage struct {
	name string
	d    schedule.Deliverer
}

// NewLocalFirstDeliverer 构造组合投递器；三层全空直接报错（fail-closed）。
func NewLocalFirstDeliverer(opts LocalFirstOptions) (*LocalFirstDeliverer, error) {
	stages := make([]deliveryStage, 0, 3)
	if opts.Local != nil {
		stages = append(stages, deliveryStage{name: "local(L1)", d: opts.Local})
	}
	if opts.Peer != nil {
		stages = append(stages, deliveryStage{name: "peer(L2)", d: opts.Peer})
	}
	if opts.HTTP != nil {
		stages = append(stages, deliveryStage{name: "http(L3)", d: opts.HTTP})
	}
	if len(stages) == 0 {
		return nil, fmt.Errorf("schedulehost: LocalFirstDeliverer needs at least one of local/peer/http")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &LocalFirstDeliverer{stages: stages, logf: logf}, nil
}

// Deliver 是 legacy 形态（无 occurrence 键，透传给各层）。
func (d *LocalFirstDeliverer) Deliver(ctx context.Context, sessionID, text string) error {
	return d.DeliverEnvelope(ctx, schedule.DeliveryEnvelope{SessionID: sessionID, Text: text})
}

// DeliverEnvelope 按就近优先顺序投递；实现 schedule.EnvelopeDeliverer，
// 因此 Runtime 会把 occurrence 键带到每一层（接收侧可做账本去重）。
func (d *LocalFirstDeliverer) DeliverEnvelope(ctx context.Context, env schedule.DeliveryEnvelope) error {
	if d == nil || len(d.stages) == 0 {
		return fmt.Errorf("schedulehost: deliverer not configured")
	}
	failures := make([]string, 0, len(d.stages))
	for _, stage := range d.stages {
		err := schedule.DeliverEnvelopeTo(ctx, stage.d, env)
		if err == nil {
			if len(failures) > 0 {
				d.logf("schedulehost: delivered via %s after earlier stage failures: %s",
					stage.name, strings.Join(failures, "; "))
			}
			return nil
		}
		failures = append(failures, stage.name+": "+err.Error())
	}
	return fmt.Errorf("schedulehost: all delivery stages failed: %s", strings.Join(failures, " | "))
}
