package memoryruntime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestDeterministicSummaryPreservesEntitySymbols(t *testing.T) {
	records := []memorystore.Record{
		{ID: "cpp", Kind: "fact", Text: "C++ uses the native compiler"},
		{ID: "csharp", Kind: "fact", Text: "C# uses the native compiler"},
		{ID: "version-dot", Kind: "fact", Text: "Server v1.2 is supported"},
		{ID: "version-dash", Kind: "fact", Text: "Server v1-2 is supported"},
	}
	text, ids, _ := deterministicSummary(records, 1000)
	require.ElementsMatch(t, []string{"cpp", "csharp", "version-dot", "version-dash"}, ids)
	for _, record := range records {
		require.Contains(t, text, record.Text)
	}
}

func TestSummaryBulletKeyNormalizationBoundary(t *testing.T) {
	tests := []struct {
		name  string
		left  memorystore.Record
		right memorystore.Record
		same  bool
	}{
		{
			name:  "case and whitespace duplicates",
			left:  memorystore.Record{Kind: " fact ", Text: " \tC++  uses\n the compiler. "},
			right: memorystore.Record{Kind: "FACT", Text: "c++ uses the compiler."},
			same:  true,
		},
		{
			name:  "language symbols",
			left:  memorystore.Record{Kind: "fact", Text: "Use C++"},
			right: memorystore.Record{Kind: "fact", Text: "Use C#"},
		},
		{
			name:  "version punctuation",
			left:  memorystore.Record{Kind: "fact", Text: "Support v1.2"},
			right: memorystore.Record{Kind: "fact", Text: "Support v1-2"},
		},
		{
			name:  "path punctuation",
			left:  memorystore.Record{Kind: "fact", Text: "Use /cache/a-b"},
			right: memorystore.Record{Kind: "fact", Text: "Use /cache/a/b"},
		},
		{
			name:  "different kinds",
			left:  memorystore.Record{Kind: "fact", Text: "Use C++"},
			right: memorystore.Record{Kind: "decision", Text: "Use C++"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right := summaryBulletKey(tt.left), summaryBulletKey(tt.right)
			require.NotEmpty(t, left)
			require.NotEmpty(t, right)
			if tt.same {
				require.Equal(t, left, right)
			} else {
				require.NotEqual(t, left, right)
			}
		})
	}
	require.Empty(t, summaryBulletKey(memorystore.Record{Kind: "fact", Text: " \t\n "}))
}
