// delivery_metrics.go 是连接器投递面的进程指标（docs/bots/13 §7.4 D10）：
// 订阅平面（content/notice/digest）每次投递结果的低基数计数，落
// observability.GlobalMetrics：
//
//	connectors_deliveries_total{connector,form,outcome}   sent|failed
//	connectors_digest_merged_total{connector}             被 digest 吸收的事件数
//
// 高基数维度（会话 id、订阅 id、去重键）只进 ConnectorLog，不进指标——
// Prometheus 面保持有界。口径与审计（Record*Delivery）严格互补：
// 审计回答「投了什么、凭哪个订阅」，指标回答「投递成功率与聚合放大率」。
package connectors

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
)

// 投递形态（指标 form label 值；13 §7.1）。
const (
	DeliveryFormContent = "content"
	DeliveryFormNotice  = "notice"
	DeliveryFormDigest  = "digest"
	DeliveryFormStream  = "stream"
)

// 投递结果（指标 outcome label 值）。
const (
	deliveryOutcomeSent   = "sent"
	deliveryOutcomeFailed = "failed"
)

// recordConnectorDelivery 记一次投递结果。空 label 不产生样本
// （指标不得出现空维度）；observability 内部并发安全，可在投递热路径调用。
func recordConnectorDelivery(connectorID, form, outcome string) {
	connectorID = strings.TrimSpace(connectorID)
	form = strings.TrimSpace(form)
	outcome = strings.TrimSpace(outcome)
	if connectorID == "" || form == "" || outcome == "" {
		return
	}
	observability.IncrementCounter(observability.MetricConnectorsDeliveriesTotal, map[string]string{
		observability.LabelConnector: connectorID,
		observability.LabelForm:      form,
		observability.LabelOutcome:   outcome,
	})
}

// recordDigestMerge 记一次「事件被 digest 窗口吸收」（未即时投递）。与
// deliveries_total 分列：merge 是聚合有效性的读数（13 §3.7#2「不放大」），
// 不是一次投递，混合会污染投递成功率。
func recordDigestMerge(connectorID string) {
	connectorID = strings.TrimSpace(connectorID)
	if connectorID == "" {
		return
	}
	observability.IncrementCounter(observability.MetricConnectorsDigestMergedTotal, map[string]string{
		observability.LabelConnector: connectorID,
	})
}
