package goal

import "testing"

func TestIsContinueIntentMessage(t *testing.T) {
	for _, message := range []string{"继续", " 继续！ ", "please continue", "Resume..."} {
		if !IsContinueIntentMessage(message) {
			t.Fatalf("expected explicit continue intent: %q", message)
		}
	}
	for _, message := range []string{"", "看一下当前状态", "continue with the report", "resume later"} {
		if IsContinueIntentMessage(message) {
			t.Fatalf("must not re-arm on ambiguous message: %q", message)
		}
	}
}
