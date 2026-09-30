package contextmgr

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// W7 参数校准（04 §7.6：用中位数 + 95% CI 校准阈值）。
//
//   - TestKnowledgeBroadBudgetCoversTypicalUpperBoundLine 是不变量：按典型上界
//     （target ≤128 / summary ≤200 / version ≤64 rune）构造的 broad 条目行必须
//     落在 DefaultKnowledgeTokens 预算内，且该条目能被注入（单条不落空）。
//   - TestPhase2CalibrationDrill 是 env 门控的真实语料演练：对仓库内
//     `reports/phase1_shadow_calls.jsonl`（真实 grep/view 调用回放）复算
//     broad 条目行尺寸与查询长度的中位数 + 95% CI，产出预算建议。
//     它标注为「装置演练」：语料真实、路径真实，但不是 A/B 收益实测。

// TestKnowledgeBroadBudgetCoversTypicalUpperBoundLine 锁定预算不变量。
func TestKnowledgeBroadBudgetCoversTypicalUpperBoundLine(t *testing.T) {
	item := knowledge.ReuseItem{
		NodeID:           "en-drill",
		NodeType:         knowledge.NodeTypeFile,
		Target:           strings.Repeat("p", 128),
		Summary:          strings.Repeat("s", 200),
		Confidence:       0.95,
		KnowledgeVersion: strings.Repeat("v", 64),
		Scope:            knowledge.ReuseScopeTask,
		Reason:           knowledge.ReuseReasonOK,
	}
	line := knowledgeBroadLine(item)
	cost := approxKnowledgeTokens(line)
	require.LessOrEqual(t, cost, DefaultKnowledgeTokens,
		"典型上界条目行必须落在默认预算内（行成本 %d > 预算 %d）", cost, DefaultKnowledgeTokens)

	content, injected := knowledgeBroadContent([]knowledge.ReuseItem{item}, DefaultKnowledgeTokens)
	require.Equal(t, 1, injected, "预算内必须至少注入该条目")
	require.NotEmpty(t, content)
}

// shadowCall 是 phase1_shadow_calls.jsonl 的最小投影（只取测量所需字段）。
type shadowCall struct {
	Tool string `json:"tool"`
	Args struct {
		Pattern  string `json:"pattern"`
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	} `json:"args"`
	Output string `json:"output"`
}

// TestPhase2CalibrationDrill 对真实 shadow 调用语料做有界演练（默认跳过）。
//
//	KNOWLEDGE_PHASE2_SHADOW_CALLS=<phase1_shadow_calls.jsonl 路径>
func TestPhase2CalibrationDrill(t *testing.T) {
	path := os.Getenv("KNOWLEDGE_PHASE2_SHADOW_CALLS")
	if path == "" {
		t.Skip("KNOWLEDGE_PHASE2_SHADOW_CALLS not set")
	}
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)

	var (
		lineCosts    []float64
		messageCosts []float64
		queryLens    []float64
		grepCount    int
		viewCount    int
		skipped      int
	)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var call shadowCall
		if err := json.Unmarshal([]byte(raw), &call); err != nil {
			skipped++
			continue
		}
		query := strings.TrimSpace(call.Args.Pattern)
		target := strings.TrimSpace(call.Args.Path)
		switch strings.ToLower(strings.TrimSpace(call.Tool)) {
		case "grep":
			grepCount++
			if target == "" {
				target = query
			}
		case "view":
			viewCount++
			query = strings.TrimSpace(call.Args.FilePath)
			target = query
		default:
			skipped++
			continue
		}
		if query == "" {
			skipped++
			continue
		}
		// 用生产格式（knowledgeBroadLine）构造条目行：target 取真实路径/模式，
		// summary 取真实输出（行内按 200 rune 截断，与生产一致）。
		item := knowledge.ReuseItem{
			NodeID:           "en-drill",
			NodeType:         knowledge.NodeTypeFile,
			Target:           target,
			Summary:          call.Output,
			Confidence:       0.95,
			KnowledgeVersion: "wv-drill-00000000",
			Scope:            knowledge.ReuseScopeTask,
			Reason:           knowledge.ReuseReasonOK,
		}
		lineCosts = append(lineCosts, float64(approxKnowledgeTokens(knowledgeBroadLine(item))))
		// 预算语义是「header + Σ条目行」：同时测量单条目整条消息成本。
		if content, injected := knowledgeBroadContent([]knowledge.ReuseItem{item}, DefaultKnowledgeTokens); injected == 1 {
			messageCosts = append(messageCosts, float64(approxKnowledgeTokens(content)))
		}
		queryLens = append(queryLens, float64(utf8.RuneCountInString(query)))
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, lineCosts, "演练语料为空（检查 KNOWLEDGE_PHASE2_SHADOW_CALLS）")
	require.NotEmpty(t, messageCosts, "单条目消息成本样本为空（预算 %d 放不下任何实测条目行）", DefaultKnowledgeTokens)

	lineMedian, lineCILow, lineCIHigh := knowledge.MedianCI95(lineCosts)
	lineP50, lineP95 := knowledge.Percentiles(lineCosts)
	budget, _, _ := knowledge.CalibrateTokenBudget(lineCosts, 100)
	lineMax := lineCosts[0]
	for _, value := range lineCosts {
		if value > lineMax {
			lineMax = value
		}
	}

	queryMedian, queryCILow, queryCIHigh := knowledge.MedianCI95(queryLens)
	_, queryP95 := knowledge.Percentiles(queryLens)
	minQuery := queryLens[0]
	maxQuery := queryLens[0]
	for _, value := range queryLens {
		if value < minQuery {
			minQuery = value
		}
		if value > maxQuery {
			maxQuery = value
		}
	}

	t.Logf("drill corpus: calls=%d (grep=%d view=%d skipped=%d)", len(lineCosts), grepCount, viewCount, skipped)
	t.Logf("broad line cost (rune): median=%.0f CI95=[%.0f,%.0f] p50=%.0f p95=%.0f max=%.0f; recommended budget (round 100) = %d; default=%d",
		lineMedian, lineCILow, lineCIHigh, lineP50, lineP95, lineMax, budget, DefaultKnowledgeTokens)
	messageMedian, messageCILow, messageCIHigh := knowledge.MedianCI95(messageCosts)
	_, messageP95 := knowledge.Percentiles(messageCosts)
	messageMax := messageCosts[0]
	for _, value := range messageCosts {
		if value > messageMax {
			messageMax = value
		}
	}
	messageBudget, _, _ := knowledge.CalibrateTokenBudget(messageCosts, 100)
	t.Logf("single-item broad message cost (rune, header included): median=%.0f CI95=[%.0f,%.0f] p95=%.0f max=%.0f; recommended budget (round 100) = %d",
		messageMedian, messageCILow, messageCIHigh, messageP95, messageMax, messageBudget)
	t.Logf("query length (rune): median=%.0f CI95=[%.0f,%.0f] p95=%.0f min=%.0f max=%.0f; default min query length=%d",
		queryMedian, queryCILow, queryCIHigh, queryP95, minQuery, maxQuery, knowledge.DefaultMinKnowledgeQueryLength)
}
