package ui

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_body_store_test.go —— T1 编解码契约：round-trip、指纹稳定、
// 篡改检测、内存 store 身份隔离（设计文档 §14.2/§14.5）。

func spike1SampleCells() []scene.TranscriptCell {
	doc := render.Document{Blocks: []render.Block{{
		Kind: render.BlockParagraph,
		Lines: []render.Line{
			{Spans: []render.Span{{Text: "hello ", Style: render.Style{Bold: true}}, {Text: "世界", Link: "https://example.com"}}},
			{Spans: []render.Span{{Text: "second line", Style: render.Style{Italic: true, Role: "role-text"}}}},
		},
	}}}
	created := time.Unix(1700000000, 0)
	finalized := time.Unix(1700000100, 0)
	return []scene.TranscriptCell{
		{
			ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "第一行\n\n尾随空行\n",
			Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			Revision:     3, Phase: scene.CellCommitted, ChainKey: "", BoundaryGroupKey: "grp-1",
			CreatedAt: created, FinalizedAt: &finalized,
		},
		{
			ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "# 标题\n\n正文 with `code`\n",
			Presentation: scene.TranscriptPresentation{Kind: scene.PresentationDocument, Document: doc},
			Revision:     9, Phase: scene.CellCommitted, ChainKey: "chain-a", BoundaryGroupKey: "grp-1",
			CreatedAt: created,
		},
		{
			ID: 3, Sequence: 3, Kind: scene.KindSupplement, Source: "diff --git a/x b/x\n@@ -1 +1 @@\n-old\n+new\n",
			Presentation: scene.TranscriptPresentation{Kind: scene.PresentationDiffSupplement, DiffLabel: "Edited"},
			Revision:     1, Phase: scene.CellCommitted, ChainKey: "chain-a",
		},
		{
			ID: 4, Sequence: 4, Kind: scene.KindReasoning, Source: "   \n",
			Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			Revision:     2, Phase: scene.CellCommitted,
		},
		{
			ID: 5, Sequence: 5, Kind: scene.KindToolChain, Source: "",
			Presentation: scene.TranscriptPresentation{Kind: scene.PresentationPlain},
			Revision:     1, Phase: scene.CellCommitted, ChainKey: "chain-b", BoundaryGroupKey: "grp-2",
			HistoryCommitBlocked: true,
		},
	}
}

func TestCellBodyRoundTripAndFingerprintStability(t *testing.T) {
	for _, cell := range spike1SampleCells() {
		payload, err := MarshalTranscriptCellBody(cell, 12, 4)
		if err != nil {
			t.Fatalf("cell %d marshal: %v", cell.ID, err)
		}
		if payload.Meta.SourceFP != CellBodySourceFingerprint(cell) {
			t.Fatalf("cell %d fingerprint 不稳定", cell.ID)
		}
		decoded, err := ValidateTranscriptCellBody(payload)
		if err != nil {
			t.Fatalf("cell %d validate: %v", cell.ID, err)
		}
		if decoded.ID != cell.ID || decoded.Revision != cell.Revision || decoded.Sequence != cell.Sequence ||
			decoded.Kind != cell.Kind || decoded.Phase != cell.Phase ||
			decoded.Source != cell.Source || decoded.ChainKey != cell.ChainKey ||
			decoded.BoundaryGroupKey != cell.BoundaryGroupKey ||
			decoded.HistoryCommitBlocked != cell.HistoryCommitBlocked {
			t.Fatalf("cell %d round-trip 字段不一致：%+v", cell.ID, decoded)
		}
		if !reflect.DeepEqual(decoded.Presentation, cell.Presentation) {
			t.Fatalf("cell %d presentation round-trip 不一致：%+v vs %+v", cell.ID, decoded.Presentation, cell.Presentation)
		}
		if (decoded.FinalizedAt == nil) != (cell.FinalizedAt == nil) {
			t.Fatalf("cell %d FinalizedAt 有无不一致：decoded=%v want=%v", cell.ID, decoded.FinalizedAt, cell.FinalizedAt)
		}
		if cell.FinalizedAt != nil && !decoded.FinalizedAt.Equal(*cell.FinalizedAt) {
			t.Fatalf("cell %d FinalizedAt 未保留：decoded=%v want=%v", cell.ID, *decoded.FinalizedAt, *cell.FinalizedAt)
		}
	}
}

func TestCellBodyTamperIsRejected(t *testing.T) {
	original := spike1SampleCells()[1]
	payload, err := MarshalTranscriptCellBody(original, 12, 4)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// 伪装：用另一个 cell 的 body 但保留原身份元数据（模拟冷区内容被替换/损坏）。
	other := original
	other.Source = original.Source + "tampered"
	tamperedPayload, err := MarshalTranscriptCellBody(other, 12, 4)
	if err != nil {
		t.Fatalf("marshal tampered: %v", err)
	}
	tamperedPayload.Meta.CellID = payload.Meta.CellID
	tamperedPayload.Meta.Revision = payload.Meta.Revision
	// 模拟"身份元数据是原 cell、body 已被替换"：指纹也必须沿用原值，
	// 否则校验会因为元数据自洽而放行。
	tamperedPayload.Meta.SourceFP = payload.Meta.SourceFP
	if _, err := ValidateTranscriptCellBody(tamperedPayload); err == nil {
		t.Fatal("指纹不一致时必须 fail-closed")
	}

	// 截断/破坏 payload 也必须失败。
	broken := payload
	broken.Data = broken.Data[:len(broken.Data)/2]
	if _, err := ValidateTranscriptCellBody(broken); err == nil {
		t.Fatal("损坏 payload 必须被拒绝")
	}
}

func TestMemoryCellBodyStoreIdentityIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryCellBodyStore()
	cell := spike1SampleCells()[0]
	payload, err := MarshalTranscriptCellBody(cell, 3, 1)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Put(ctx, "s1", payload); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok, err := store.Get(ctx, "s1", cell.ID, cell.Revision); err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := store.Get(ctx, "s2", cell.ID, cell.Revision); ok {
		t.Fatal("会话隔离失效（s2 不应读到 s1 的行）")
	}
	if _, ok, _ := store.Get(ctx, "s1", cell.ID, cell.Revision+1); ok {
		t.Fatal("revision 隔离失效")
	}
}
