package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// toolFoldHint 追加在折叠标记后，指明取回完整正文的交互入口。折叠是显示层
// 选择，标记必须自己给出恢复路径，否则就是死胡同。
//
// 提示只出现在「最近的一次折叠」上：Ctrl+T 打开的 transcript pager 初始帧
// 展开那一次折叠、其余折叠保持收起。把提示印在每个折叠 cell 上等于承诺一个
// 并不存在的「一键展开全部」，用户按下 Ctrl+T 会看到整份 transcript 铺开，
// 于是判定折叠功能坏了。
const toolFoldHint = "Ctrl+T 查看完整文本"

// transcriptPagerFoldHint 是 pager 内部收起折叠的恢复路径。pager 是只读的
// 全文视图，它不能把「展开」这一动作再交回给 Ctrl+T（那会递归地承诺同一次
// 折叠），因此给出 pager 自己的按键。
const transcriptPagerFoldHint = "e 展开全部"

// toolFoldOptions 返回已提交工具链 cell 的显示预算投影参数。工具/MCP 输出
// 不可信：绝不保留原始 CSI（AllowANSI=false）。
func toolFoldOptions(hint string) cell.PreviewOptions {
	opts := cell.ToolDisplayPreviewOptions()
	opts.AllowANSI = false
	opts.Hint = hint
	return opts
}

// toolFoldOmits 判定显示预算投影是否真的隐藏了正文（出现 head/tail 标记或
// 字节上限标记）。
//
// 判定必须走 BuildPreview 本身：任何更便宜的估算（数行数、比字节数）都可能
// 与真正渲染出来的标记不一致——例如尾部空行会被丢弃——从而把提示或默认展开
// 挂在一个根本没有标记的 cell 上，再次变成假承诺。
func toolFoldOmits(source string) bool {
	preview := cell.BuildPreview(source, toolFoldOptions(""))
	return preview.OmittedLines > 0 || preview.ByteTruncated
}

// toolFoldPlainLines 返回显示预算投影后的纯文本行（含折叠标记）。pager 与首屏
// 共用同一投影，因此 pager 里收起的折叠与首屏逐行一致。
func toolFoldPlainLines(source string, hint string) []string {
	lines := cell.BuildPreview(source, toolFoldOptions(hint)).Lines
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, render.PlainBackend{}.Render(render.LinesDoc(line)))
	}
	return out
}

// toolFoldTarget 返回按 transcript 顺序排列的 cell 中最后（最近）一次折叠的
// cell ID；没有折叠时返回 0。首屏提示与 pager 的默认展开都以此为准，保证
// 「提示指向的那次折叠」和「实际被展开的那次折叠」永远是同一个。
func toolFoldTarget(cells []scene.TranscriptCell) scene.CellID {
	target := scene.CellID(0)
	for index := range cells {
		candidate := cells[index]
		if !cellUsesFoldedToolPresentation(candidate) || !toolFoldOmits(candidate.Source) {
			continue
		}
		target = candidate.ID
	}
	return target
}

// toolFoldTargetRows 同 toolFoldTarget，但输入是 layout rows：同一个 cell 可能
// 占据多行，且未提交（mutable）的 cell 由 active band 渲染，不参与折叠归属。
func toolFoldTargetRows(rows []scene.LayoutRow, cells map[scene.CellID]scene.TranscriptCell, mutable map[scene.CellID]struct{}) scene.CellID {
	target := scene.CellID(0)
	for _, row := range rows {
		if _, excluded := mutable[row.CellID]; excluded {
			continue
		}
		candidate, found := cells[row.CellID]
		if !found || candidate.ID == target {
			continue
		}
		if !cellUsesFoldedToolPresentation(candidate) || !toolFoldOmits(candidate.Source) {
			continue
		}
		target = candidate.ID
	}
	return target
}
