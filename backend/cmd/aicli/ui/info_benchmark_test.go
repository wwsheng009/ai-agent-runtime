package ui

import (
	"strings"
	"testing"
)

func BenchmarkTableDocument100KiBCell(b *testing.B) {
	value := strings.Repeat("structured-render-value ", 100*1024/len("structured-render-value ")+1)
	headers := []string{"Name", "State", "Description"}
	rows := [][]string{{"renderer", "running", value}}
	b.ReportAllocs()
	b.SetBytes(int64(len(value)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = TableDocument(headers, rows, 80)
	}
}
