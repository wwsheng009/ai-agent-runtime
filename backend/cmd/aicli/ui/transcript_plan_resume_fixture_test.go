package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// resumeUnmappableSource 是不可逐行映射的 plain 源（tab 开头），走 whole-cell
// fallback 路径。
const resumeUnmappableSource = "制表符\t开头的行\n第二行\n"

// resumeParitySnapshot 构造覆盖三条规划路径（plain / markdown / whole-cell +
// 折叠工具链）的 transcript 快照，供 snapshot/async/worker 测试共用。
func resumeParitySnapshot() *scene.Snapshot {
	cells := []scene.TranscriptCell{
		{
			ID: 1, Revision: 1, Sequence: 1, Kind: scene.KindUser,
			Source: strings.Repeat("alpha line\n", 3000),
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		},
		{
			ID: 2, Revision: 1, Sequence: 2, Kind: scene.KindAssistant,
			Source: "# 大块\n\n" + strings.Repeat("markdown 段落行\n", 200),
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		},
		{
			ID: 3, Revision: 1, Sequence: 3, Kind: scene.KindUser,
			Source: resumeUnmappableSource,
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		},
		committedToolCell(4, numberedToolLines("resume", 6000)),
		{
			ID: 5, Revision: 1, Sequence: 5, Kind: scene.KindAssistant,
			Source: "仍在生成", Phase: scene.CellMutable, Boundary: boundary.BoundaryNormal,
		},
	}
	refs := make([]*scene.TranscriptCell, 0, len(cells))
	for index := range cells {
		refs = append(refs, &cells[index])
	}
	return &scene.Snapshot{SceneID: 7, Revision: 4, Cells: refs}
}
