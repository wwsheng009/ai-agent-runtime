package commands

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
)

// BenchmarkPersistedHistoryUnitMatch 用固定语料量化 seed 的「unit × item」匹配成本——
// 补齐（逐页装载）、装载收尾（整份重放）与一次性启动装载走的都是这条内层循环。
//
// 语料形状对齐真实恢复会话：推理块的 Scene 头部是重放形态（带 "…… reasoning ……"
// 与 "end reasoning" 分隔行），canonical unit 只有正文，必须走
// persistedReasoningBody(item.Head) == persistedReasoningContentBody(unit.content)
// 这条最贵的分支；user/assistant 是精确头部匹配。lf / crlf 两组分别覆盖常见与
// Windows 落盘（含 CR）两种文本。
//
// 实测口径（2026-09-25，Ryzen 7 5800H / GOMAXPROCS=2）：3600 items × 1800 units
// 一轮约 25–27ms、LF 9 次分配 / CRLF ~1.2k 次分配 —— 匹配不是装载瓶颈：
// 「已认领」作用域让扫描短路，归一化只在含 CR 时才真的复制文本。曾据此尝试给
// item 侧归一化加缓存（persistedHistoryMatchCache），两组语料都测不到收益
// （26.9ms → 27.3ms），已回退。保留本基准是为了防止这条内层循环将来退化成真正的
// 二次方（生产曾出现 unit 侧 toolHead 分配风暴，见 resolvedToolHead 注释）。
//
// 运行：go test ./cmd/aicli/commands/ -run '^$' -bench UnitMatch -benchmem
func BenchmarkPersistedHistoryUnitMatch(b *testing.B) {
	b.Run("lf", func(b *testing.B) { benchPersistedHistoryUnitMatch(b, "\n") })
	b.Run("crlf", func(b *testing.B) { benchPersistedHistoryUnitMatch(b, "\r\n") })
}

func benchPersistedHistoryUnitMatch(b *testing.B, eol string) {
	const (
		perKind    = 600
		reasonSize = 3000
	)
	bodies := make([]string, perKind)
	for index := range bodies {
		body := fmt.Sprintf("第 %d 段推理：%s", index, strings.Repeat("上下文片段", reasonSize/18))
		if eol == "\r\n" {
			// 让正文本身也带 CRLF：这样 unit 侧归一化在含 CR 时必须复制整段文本，
			// 正是 Windows 落盘会话的形态。
			body = strings.ReplaceAll(body, "：", "："+eol)
		}
		bodies[index] = body
	}

	items := make([]*encoding.Item, 0, perKind*3)
	units := make([]persistedHistorySeedUnit, 0, perKind*3)
	for index := 0; index < perKind; index++ {
		items = append(items,
			&encoding.Item{
				ID:     fmt.Sprintf("item-r%d", index),
				Kind:   encoding.KindReasoning,
				Status: encoding.StatusCompleted,
				Head:   "…… reasoning ……" + eol + bodies[index] + eol + "end reasoning",
			},
			&encoding.Item{
				ID:     fmt.Sprintf("item-u%d", index),
				Kind:   encoding.KindUser,
				Status: encoding.StatusCompleted,
				Head:   fmt.Sprintf("问题 %d", index),
			},
			&encoding.Item{
				ID:     fmt.Sprintf("item-a%d", index),
				Kind:   encoding.KindAssistant,
				Status: encoding.StatusCompleted,
				Head:   fmt.Sprintf("回答 %d", index),
			},
		)
		units = append(units,
			persistedHistorySeedUnit{
				kind:             persistedHistorySeedSupplement,
				content:          bodies[index],
				boundaryGroupKey: fmt.Sprintf("group-%d", index),
			},
			persistedHistorySeedUnit{kind: persistedHistorySeedUser, content: fmt.Sprintf("问题 %d", index)},
			persistedHistorySeedUnit{kind: persistedHistorySeedAssistant, content: fmt.Sprintf("回答 %d", index)},
		)
	}
	snapshot := &encoding.RenderModel{Items: items}
	b.Logf("items=%d units=%d reasoning_bytes=%d", len(items), len(units), reasonSize)

	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		matched := make(map[string]struct{}, len(items))
		hits := 0
		for index := range units {
			if persistedHistoryUnitMatch(snapshot, units[index], matched) != nil {
				hits++
			}
		}
		if hits != len(units) {
			b.Fatalf("命中 %d/%d：语料构造失效，基准无意义", hits, len(units))
		}
	}
}
