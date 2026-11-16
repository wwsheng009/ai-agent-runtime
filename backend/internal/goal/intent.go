package goal

import "strings"

// continueIntentPhrases is the conservative "user explicitly asks to continue"
// match table shared by the CLI and runtime-server goal drivers. Only short,
// unambiguous instructions qualify: after trimming whitespace and terminal
// punctuation the whole message must match. Queries such as "check status"
// must not re-arm autonomous continuation.
var continueIntentPhrases = map[string]struct{}{
	"继续": {}, "继续吧": {}, "继续执行": {}, "继续干活": {}, "继续工作": {}, "请继续": {},
	"接着": {}, "接着来": {}, "接着做": {}, "接着干": {}, "往下做": {}, "往下干": {},
	"恢复": {}, "恢复吧": {}, "恢复执行": {}, "干吧": {},
	"continue": {}, "continue please": {}, "please continue": {}, "go on": {},
	"keep going": {}, "carry on": {}, "proceed": {}, "resume": {}, "keep working": {},
}

// IsContinueIntentMessage reports whether a user message is an explicit
// continuation instruction. Normalization: trim, strip trailing punctuation,
// lowercase ASCII, then require an exact table match.
func IsContinueIntentMessage(message string) bool {
	normalized := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(message), "。．.!！?？~～… "))
	if normalized == "" {
		return false
	}
	_, ok := continueIntentPhrases[strings.ToLower(normalized)]
	return ok
}
