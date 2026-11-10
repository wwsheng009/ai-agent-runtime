package chat

import (
	"fmt"
	"strings"
	"testing"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// The legacy key concatenates the whole message body into a new string for
// every map lookup. The struct key references the existing message strings;
// these benchmarks keep that allocation difference visible.
func BenchmarkCanonicalMessageSubstanceKey(b *testing.B) {
	message := runtimetypes.Message{
		Role:    "assistant",
		Content: strings.Repeat("x", 4096),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = canonicalMessageSubstanceKey(message)
	}
}

func BenchmarkCanonicalMessageSubstanceOf(b *testing.B) {
	message := runtimetypes.Message{
		Role:    "assistant",
		Content: strings.Repeat("x", 4096),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = canonicalMessageSubstanceOf(message)
	}
}

func BenchmarkIdentityAlignedAppendStart(b *testing.B) {
	const count = 256
	history := make([]runtimetypes.Message, count)
	tail := make([]runtimetypes.Message, count)
	for i := 0; i < count; i++ {
		message := *runtimetypes.NewAssistantMessage(strings.Repeat("x", 512) + string(rune('a'+i%26)))
		message.Metadata.Set(runtimetypes.MetadataKeyMessageID, fmt.Sprintf("msg-%d", i))
		history[i] = message
		tail[i] = message
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, anchored := identityAlignedAppendStart(history, tail); !anchored {
			b.Fatal("fixture did not anchor")
		}
	}
}
