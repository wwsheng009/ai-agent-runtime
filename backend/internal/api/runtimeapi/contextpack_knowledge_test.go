package runtimeapi

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// Phase 6 切片 4：context pack 的知识层装配门控——
// 仅 knowledge.mode=on 注入 pack；shadow 只保鲜索引（ModeShadow 契约）绝不进
// prompt；off / 未激活零装配（pack 与改动前逐字节一致）。
func TestContextPackKnowledgeLayerGating(t *testing.T) {
	handler := &Handler{}
	if handler.contextPackKnowledgeLayer() != nil {
		t.Fatal("未激活时不得装配 knowledge provider")
	}

	on := activateKnowledgeAssemblyForTest(t, knowledge.ModeOn)
	handler.SetKnowledgeActivation(on)
	if handler.contextPackKnowledgeLayer() == nil {
		t.Fatal("on 档必须装配 knowledge provider")
	}

	shadow := activateKnowledgeAssemblyForTest(t, knowledge.ModeShadow)
	handler.SetKnowledgeActivation(shadow)
	if handler.contextPackKnowledgeLayer() != nil {
		t.Fatal("shadow 档只保鲜索引，不得进入 context pack")
	}

	// off（默认）：Activate 契约返回 (nil, nil)，handler 不持有句柄——零装配。
	handler.SetKnowledgeActivation(nil)
	if handler.contextPackKnowledgeLayer() != nil {
		t.Fatal("off 档不得装配 knowledge provider")
	}
}
