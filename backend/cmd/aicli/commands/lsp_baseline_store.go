package commands

import (
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/baseline"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/baselinesql"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// lspBaselineFromStore 从分析库读 LSP 基线（**不扫日志**）。
//
// 为什么 TUI 与 web 面板也改走库：这两处是用户真正会看的地方，若它们继续回扫
// chat-logs，那么"日志有保留策略、数字会无声退化"和"cold_first_publish 早已冻结"
// 这两个缺陷就会在面板上原样留存 —— 修了后端等于没修。
//
// 只读挂载：读路径不该建表。若目标库还没有 LSP 两张新表，查询层会退化成空结果
// （Store.query 对缺表返回"无结果"而非报错），此时面板如实显示未采集，而不是
// 报错或伪造 0。
//
// lspBaselineStorePath 是测试接缝：生产走默认库路径，测试指到临时库。
// 此前事实源是 chat-logs 目录时，接缝是 lspBaselineRoots（改扫描根）；现在事实源
// 是数据库，接缝相应地变成"改库路径"。
var lspBaselineStorePath = usageanalytics.DefaultDBPath

func lspBaselineFromStore(since time.Time) (baseline.Stats, error) {
	service, err := usageanalytics.Attach(nil, usageanalytics.Options{
		Config: usageanalytics.Config{Path: lspBaselineStorePath(), ReadOnly: true},
	})
	if err != nil {
		return baseline.Stats{}, err
	}
	defer service.Close()
	return baselinesql.Analyze(service.Store(), baselinesql.Options{Since: since})
}
