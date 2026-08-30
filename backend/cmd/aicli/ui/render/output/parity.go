package output

import "fmt"

// ============================================================================
// Phase 4：physical + capture 双跑与 parity（17.3/6.6）
// ============================================================================

// ParityKind 描述 parity 检查的种类。
type ParityKind string

const (
	ParityBatchWire         ParityKind = "batch_wire"         // physical batches 与 wire capture bytes 一致
	ParitySemantic          ParityKind = "semantic"           // physical 与 capture 的 semantic payload 一致
	ParityVirtualProjection ParityKind = "virtual_projection" // capture/virtual mirror 的投影一致
)

// ParityResult 是单次 parity 检查的结果。
type ParityResult struct {
	Kind        ParityKind
	Pass        bool
	Skipped     bool
	SkipReason  string // semantic=unavailable 等
	Sequence    uint64
	BatchID     string
	ExpectedLen int // wire/语义字节数
	ActualLen   int
	Mismatches  []ParityMismatch // 最多报告前 N 个差异
}

// ParityMismatch 是 parity 差异的稳定描述。
type ParityMismatch struct {
	Index    int
	Expected string
	Actual   string
}

// SkipWithReason 是 producer 无法提供可信 semantic payload 时的显式跳过
// （6.6：skipped-with-reason，不从 wire bytes 猜测后判成功）。
func SkipWithReason(reason string) ParityResult {
	return ParityResult{Skipped: true, SkipReason: reason}
}

// MaxParityMismatchReports 限制单次检查报告的差异数量。
const MaxParityMismatchReports = 8

// ParityBatch 比较 physical bytes 与 capture wire bytes。
// expected=physical（primary receipt 侧已成功提交的 bytes），
// actual=capture 记录的完整 payload（full_available 模式）。
func CheckBatchParity(seq uint64, batchID string, expected, actual []byte) ParityResult {
	res := ParityResult{
		Kind:        ParityBatchWire,
		Pass:        true,
		Sequence:    seq,
		BatchID:     batchID,
		ExpectedLen: len(expected),
		ActualLen:   len(actual),
	}
	if len(expected) != len(actual) {
		res.Pass = false
	}
	limit := MaxParityMismatchReports
	for i := 0; i < len(expected) && i < len(actual); i++ {
		if expected[i] != actual[i] {
			res.Pass = false
			if len(res.Mismatches) < limit {
				res.Mismatches = append(res.Mismatches, ParityMismatch{
					Index:    i,
					Expected: byteSlicePreview(expected, i),
					Actual:   byteSlicePreview(actual, i),
				})
			}
		}
	}
	if len(expected) != len(actual) && len(res.Mismatches) == 0 {
		// 长度不等但公共前缀一致：补一个长度差异记录。
		res.Mismatches = append(res.Mismatches, ParityMismatch{
			Index:    minInt(len(expected), len(actual)),
			Expected: fmt.Sprintf("<len %d>", len(expected)),
			Actual:   fmt.Sprintf("<len %d>", len(actual)),
		})
	}
	return res
}

// ParitySemantic 比较 physical 与 capture 的 SemanticPayload。
// 两者 SchemaVersion 不同或任一方为 nil 时 skipped-with-reason
// （不从 wire bytes 猜测语义）。
func CheckSemanticParity(seq uint64, batchID string, expected, actual *SemanticPayload) ParityResult {
	res := ParityResult{Kind: ParitySemantic, Pass: true, Sequence: seq, BatchID: batchID}
	if expected == nil {
		return SkipWithReason("physical semantic unavailable")
	}
	if actual == nil {
		return SkipWithReason("capture semantic unavailable")
	}
	if expected.SchemaVersion != actual.SchemaVersion {
		return SkipWithReason(fmt.Sprintf(
			"semantic schema mismatch: physical %d != capture %d",
			expected.SchemaVersion, actual.SchemaVersion))
	}
	res.ExpectedLen = len(expected.PlainText)
	res.ActualLen = len(actual.PlainText)
	if expected.PlainText != actual.PlainText {
		res.Pass = false
		res.Mismatches = append(res.Mismatches, ParityMismatch{
			Index:    0,
			Expected: textPreview(expected.PlainText),
			Actual:   textPreview(actual.PlainText),
		})
	}
	if expected.SummaryHash != "" && actual.SummaryHash != "" && expected.SummaryHash != actual.SummaryHash {
		res.Pass = false
		res.Mismatches = append(res.Mismatches, ParityMismatch{
			Index:    1,
			Expected: "hash " + expected.SummaryHash,
			Actual:   "hash " + actual.SummaryHash,
		})
	}
	return res
}

// ParityVirtualProjection 比较两条 virtual projection 快照的行内容
// （如 physical 侧渲染期望行 vs virtual mirror 投影行）。scrollback 参与
// 比较；Validity=Unknown 的投影 skipped-with-reason。
func CheckVirtualProjectionParity(seq uint64, batchID string, expected, actual VirtualProjectionSnapshot) ParityResult {
	res := ParityResult{
		Kind:     ParityVirtualProjection,
		Pass:     true,
		Sequence: seq,
		BatchID:  batchID,
	}
	if expected.Validity == ProjectionUnknown || actual.Validity == ProjectionUnknown {
		return SkipWithReason("projection unknown (partial/abort)")
	}
	expText := flattenProjection(expected)
	actText := flattenProjection(actual)
	res.ExpectedLen = len(expText)
	res.ActualLen = len(actText)
	if !sameStrings(expText, actText) {
		res.Pass = false
		const limit = MaxParityMismatchReports
		lines := 0
		for i := 0; i < len(expText) && i < len(actText) && lines < limit; i++ {
			if expText[i] != actText[i] {
				res.Mismatches = append(res.Mismatches, ParityMismatch{
					Index:    i,
					Expected: textPreview(expText[i]),
					Actual:   textPreview(actText[i]),
				})
				lines++
			}
		}
		if len(expText) != len(actText) && lines == 0 {
			res.Mismatches = append(res.Mismatches, ParityMismatch{
				Index:    minInt(len(expText), len(actText)),
				Expected: fmt.Sprintf("<%d lines>", len(expText)),
				Actual:   fmt.Sprintf("<%d lines>", len(actText)),
			})
		}
	}
	return res
}

// sameStrings 比较两字符串切片（nil 与空视为不同，除非都为空）。
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// flattenProjection 把 projection 的 rows+scrollback 压平为比较串。
func flattenProjection(p VirtualProjectionSnapshot) []string {
	out := make([]string, 0, len(p.Scrollback)+len(p.Rows))
	out = append(out, p.Scrollback...)
	out = append(out, p.Rows...)
	return out
}

// byteSlicePreview 生成字节差异预览（保持可读性）。
func byteSlicePreview(b []byte, at int) string {
	start := at - 2
	if start < 0 {
		start = 0
	}
	end := at + 3
	if end > len(b) {
		end = len(b)
	}
	return fmt.Sprintf("...%q...", b[start:end])
}

// textPreview 截断长文本供差异展示。
func textPreview(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// minInt 返回较小者（adapter 中已有，但 output 包内独立定义避免依赖）。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
