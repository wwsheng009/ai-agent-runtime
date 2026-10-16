package agent

// subagent_handoff_test.go — handoff 契约测试（P1-2'）。
//
// 覆盖（对齐 v2 方案 §4 P1-2' 测试计划）：
//   - 状态机三态 × 合法/非法字段组合；
//   - 超限边界（=maxChars 合法、+1 拒绝，报错含实测值）；
//   - 防御解码的键集合变体（多键/少键/类型错/status 不匹配）；
//   - ValidateHandoffOrWarn 的降级语义；
//   - decodeHandoffCandidate 的形态识别（纯文本不进入通道）。

import (
	"encoding/json"
	"strings"
	"testing"
)

func validContinueHandoff() Handoff {
	return Handoff{
		Status:    HandoffContinue,
		Summary:   "wrote the parser skeleton",
		NextSteps: []string{"wire the CLI flag", "add unit tests"},
	}
}

func TestValidateHandoffContinue(t *testing.T) {
	if err := ValidateHandoff(validContinueHandoff(), 0); err != nil {
		t.Fatalf("valid continue handoff must pass: %v", err)
	}
	// continue + blocker 非空 → 拒绝。
	h := validContinueHandoff()
	h.Blocker = "waiting on CI"
	if err := ValidateHandoff(h, 0); err == nil || !strings.Contains(err.Error(), "blocker") {
		t.Fatalf("continue with blocker must fail on blocker, got %v", err)
	}
	// continue 无 nextSteps → 拒绝。
	h = validContinueHandoff()
	h.NextSteps = nil
	if err := ValidateHandoff(h, 0); err == nil || !strings.Contains(err.Error(), "next_steps") {
		t.Fatalf("continue without next_steps must fail, got %v", err)
	}
}

func TestValidateHandoffComplete(t *testing.T) {
	h := Handoff{
		Status:   HandoffComplete,
		Summary:  "migration finished",
		Evidence: []string{"all tests pass", "schema v2 active"},
	}
	if err := ValidateHandoff(h, 0); err != nil {
		t.Fatalf("valid complete handoff must pass: %v", err)
	}
	// complete 无 evidence → 拒绝。
	noEvidence := h
	noEvidence.Evidence = nil
	if err := ValidateHandoff(noEvidence, 0); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("complete without evidence must fail, got %v", err)
	}
	// complete 带 nextSteps → 拒绝。
	withSteps := h
	withSteps.NextSteps = []string{"cleanup"}
	if err := ValidateHandoff(withSteps, 0); err == nil || !strings.Contains(err.Error(), "next_steps") {
		t.Fatalf("complete with next_steps must fail, got %v", err)
	}
}

func TestValidateHandoffBlocked(t *testing.T) {
	h := Handoff{
		Status:  HandoffBlocked,
		Summary: "cannot proceed without credentials",
		Blocker: "missing API key for provider X",
	}
	if err := ValidateHandoff(h, 0); err != nil {
		t.Fatalf("valid blocked handoff must pass: %v", err)
	}
	// blocked 空 blocker → 拒绝。
	empty := h
	empty.Blocker = ""
	if err := ValidateHandoff(empty, 0); err == nil || !strings.Contains(err.Error(), "blocker") {
		t.Fatalf("blocked without concrete blocker must fail, got %v", err)
	}
}

func TestValidateHandoffNormalization(t *testing.T) {
	// 空白差异是语义漂移，必须拒绝（summary / 列表项 / blocker 一视同仁）。
	h := validContinueHandoff()
	h.Summary = "  padded  "
	if err := ValidateHandoff(h, 0); err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("padded summary must fail, got %v", err)
	}
	h = validContinueHandoff()
	h.NextSteps = []string{" ok "}
	if err := ValidateHandoff(h, 0); err == nil || !strings.Contains(err.Error(), "next_steps") {
		t.Fatalf("padded next_steps entry must fail, got %v", err)
	}
	// 非法状态名。
	h = validContinueHandoff()
	h.Status = "running"
	if err := ValidateHandoff(h, 0); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("unknown status must fail, got %v", err)
	}
}

func TestValidateHandoffCharLimitBoundary(t *testing.T) {
	h := validContinueHandoff()
	encoded, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	size := len(encoded)
	// = 上限合法。
	if err := ValidateHandoff(h, size); err != nil {
		t.Fatalf("handoff at exact limit must pass: %v", err)
	}
	// +1 拒绝且报错含实测值。
	oversize := validContinueHandoff()
	oversize.Summary = oversize.Summary + strings.Repeat("x", 1)
	err = ValidateHandoff(oversize, size)
	if err == nil {
		t.Fatal("handoff over limit must fail")
	}
	if !strings.Contains(err.Error(), ">") || !strings.Contains(err.Error(), "max_chars") {
		t.Fatalf("oversize error must report measured size, got %v", err)
	}
}

func handoffRaw(t *testing.T, h Handoff) map[string]interface{} {
	t.Helper()
	encoded, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return raw
}

func TestDecodeHandoffHappyPath(t *testing.T) {
	raw := handoffRaw(t, validContinueHandoff())
	handoff, err := DecodeHandoff(raw, HandoffContinue, 0)
	if err != nil {
		t.Fatalf("valid raw handoff must decode: %v", err)
	}
	if handoff.Status != HandoffContinue || len(handoff.NextSteps) != 2 {
		t.Fatalf("decoded handoff mismatch: %+v", handoff)
	}
}

func TestDecodeHandoffKeySetIsExact(t *testing.T) {
	raw := handoffRaw(t, validContinueHandoff())
	// 多余字段拒绝。
	raw["extra"] = "noise"
	if _, err := DecodeHandoff(raw, HandoffContinue, 0); err == nil ||
		!strings.Contains(err.Error(), "unexpected field set") {
		t.Fatalf("extra key must fail with field-set error, got %v", err)
	}
	// 缺失字段拒绝。
	raw = handoffRaw(t, validContinueHandoff())
	delete(raw, "summary")
	if _, err := DecodeHandoff(raw, HandoffContinue, 0); err == nil ||
		!strings.Contains(err.Error(), "unexpected field set") {
		t.Fatalf("missing key must fail with field-set error, got %v", err)
	}
	// status 与期望不符拒绝。
	raw = handoffRaw(t, validContinueHandoff())
	if _, err := DecodeHandoff(raw, HandoffBlocked, 0); err == nil ||
		!strings.Contains(err.Error(), "does not match expected") {
		t.Fatalf("status mismatch must fail, got %v", err)
	}
	// 类型错误拒绝。
	raw = handoffRaw(t, validContinueHandoff())
	raw["evidence"] = "not-a-list"
	if _, err := DecodeHandoff(raw, HandoffContinue, 0); err == nil ||
		!strings.Contains(err.Error(), "evidence") {
		t.Fatalf("wrong-typed evidence must fail, got %v", err)
	}
	// nil 输入拒绝。
	if _, err := DecodeHandoff(nil, HandoffContinue, 0); err == nil {
		t.Fatal("nil input must fail")
	}
}

func TestDecodeHandoffNullListBecomesEmpty(t *testing.T) {
	raw := handoffRaw(t, validContinueHandoff())
	raw["evidence"] = nil
	handoff, err := DecodeHandoff(raw, HandoffContinue, 0)
	if err != nil {
		t.Fatalf("null evidence should decode as empty list: %v", err)
	}
	if handoff.Evidence == nil || len(handoff.Evidence) != 0 {
		t.Fatalf("null evidence must normalize to empty slice, got %+v", handoff.Evidence)
	}
}

func TestValidateHandoffOrWarnDegrades(t *testing.T) {
	valid := validContinueHandoff()
	clean, warning := ValidateHandoffOrWarn(&valid, 0)
	if warning != "" || clean == nil {
		t.Fatalf("valid handoff must pass through, got warning %q", warning)
	}
	broken := validContinueHandoff()
	broken.Status = HandoffBlocked // 非法组合（无 blocker）
	clean, warning = ValidateHandoffOrWarn(&broken, 0)
	if clean != nil || !strings.HasPrefix(warning, "handoff_invalid:") {
		t.Fatalf("invalid handoff must degrade to warning, got clean=%v warning=%q", clean, warning)
	}
	if _, warning := ValidateHandoffOrWarn(nil, 0); warning != "" {
		t.Fatalf("nil handoff must be a no-op, got %q", warning)
	}
}

func TestDecodeHandoffCandidateShapeDetection(t *testing.T) {
	// 纯文本不进入通道（绝大多数现状输出）。
	if got := decodeHandoffCandidate("did the thing, see diff"); got.raw != nil {
		t.Fatalf("plain text must not enter the handoff channel, got %+v", got.raw)
	}
	if got := decodeHandoffCandidate(""); got.raw != nil {
		t.Fatal("empty output must not enter the channel")
	}
	if got := decodeHandoffCandidate("[1,2,3]"); got.raw != nil {
		t.Fatal("non-object JSON must not enter the channel")
	}
	// JSON 对象进入通道。
	raw := handoffRaw(t, validContinueHandoff())
	encoded, _ := json.Marshal(raw)
	got := decodeHandoffCandidate(string(encoded))
	if got.raw == nil || got.status != HandoffContinue {
		t.Fatalf("JSON object must be detected as candidate, got %+v", got)
	}
}

func TestHandoffCloneDeepCopies(t *testing.T) {
	h := validContinueHandoff()
	clone := h.Clone()
	clone.NextSteps[0] = "mutated"
	if h.NextSteps[0] == "mutated" {
		t.Fatal("Clone must deep-copy slice fields")
	}
}
