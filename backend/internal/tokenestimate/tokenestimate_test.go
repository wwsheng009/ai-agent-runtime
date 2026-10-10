package tokenestimate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Calibration corpus with token counts measured from real encoders
// (2026-10, tiktoken 0.14 cl100k_base / o200k_base). The estimator must stay
// close to these references; when tuning constants, re-run the measurement
// script before changing the table.
type calibrationSample struct {
	name   string
	text   string
	cl100k int
	o200k  int
}

func calibrationCorpus() []calibrationSample {
	return []calibrationSample{
		{
			"en_prose",
			"The quick brown fox jumps over the lazy dog. This paragraph exists to measure " +
				"how many characters a typical English sentence consumes per token when it is " +
				"processed by a byte pair encoding tokenizer. Internationalization and tokenization " +
				"are longer words that often split into several subword tokens.",
			53, 53,
		},
		{
			"zh_prose",
			"这是一段中文文本，用于测量分词器对中文的压缩比例。中文通常按字或词切分，" +
				"常见词汇可能被合并成一个 token，生僻字往往需要多个字节共同表示。" +
				"自动压缩的触发线必须足够可靠，否则长会话要么永远不压缩，要么过早丢失上下文。" +
				"因此兜底估算器需要针对不同文字系统分别校准。",
			150, 100,
		},
		{
			"mixed",
			"会话 session 的 token 估算 estimator 需要兼顾 English words、中文汉字、" +
				"数字 1234567890 与符号 -> {} [] () 的混合场景，不能只用 len/4。",
			55, 46,
		},
		{
			"go_code",
			"func estimatePromptMessageTokens(runtime *llm.LLMRuntime, messages []types.Message) int {\n" +
				"\tif runtime == nil || len(messages) == 0 {\n\t\treturn 0\n\t}\n" +
				"\treturn runtime.CountMessagesTokens(messages)\n}\n" +
				"// TODO(user): handle ctx.Err() before retrying with backoff.\n",
			62, 61,
		},
		{
			"json_schema",
			`{"name":"execute_shell_command","description":"Run a shell command through the ` +
				`managed bash MCP tool.","parameters":{"type":"object","properties":` +
				`{"command":{"type":"string","description":"Exact command to execute"}},` +
				`"required":["command"]}}`,
			47, 47,
		},
		{
			"markdown",
			"# Title\n\n- item one\n- item two\n\n```go\nfmt.Println(\"hello\")\n```\n\n" +
				"> quote *emphasis* **strong** `code` [link](https://example.com/path?a=1&b=2)\n",
			48, 49,
		},
		{"emoji", "🚀🔥✅❌👍🏽👨‍👩‍👧‍👦🎉", 37, 22},
		{"digits", "12345678901234567890 2026-10-10T12:34:56Z 1,234,567.89 0xDEADBEEF", 35, 35},
		{"paths", `C:\Users\vince\projects\ai\ai-agent-runtime\backend\internal\llm\runtime.go`, 23, 23},
		{"cyrillic", "Это пример русского текста для измерения токенизации.", 20, 11},
		{"vietnamese", "Đây là một đoạn văn tiếng Việt để đo tỷ lệ token hóa.", 29, 15},
		{"arabic", "هذا نص عربي لقياس نسبة الترميز.", 23, 10},
		{"thai", "นี่คือข้อความภาษาไทยสำหรับวัดอัตราการแบ่งโทเค็น", 44, 16},
		{"hindi", "यह टोकन अनुपात मापने के लिए हिंदी पाठ है।", 40, 15},
		{"french", "Ceci est un texte français pour mesurer la tokenisation.", 13, 12},
		{"cjk_rare", "饕餮魑魅魍魉龘靐齉爩", 27, 19},
		{"japanese", "これは日本語のテキストです。トークナイザの測定に使います。", 27, 21},
		{"korean", "이것은 한국어 텍스트입니다. 토크나이저 측정에 사용합니다.", 30, 21},
		{"uuid", "550e8400-e29b-41d4-a716-446655440000", 18, 18},
		{"digits5", "12345", 2, 2},
		{"digits10", "1234567890", 4, 4},
		{"digits20", "12345678901234567890", 7, 7},
		{"word5", "hello", 1, 1},
		{"word10", "tokenizing", 2, 2},
		{"word28", "counterrevolutionariesxyzabc", 5, 6},
		{"camel25", "getUserIDFromSessionToken", 5, 6},
		{"snake30", "get_user_id_from_session_token", 6, 6},
		{"punct10", "##########", 2, 2},
		{"underscore16", "________________", 1, 1},
		{"dashes5", "-----", 1, 1},
		{"arrow3", "-->", 1, 1},
		{"braces6", "{}[]()", 3, 3},
		{"fence6", "```go", 2, 2},
		{"space20", strings.Repeat(" ", 20), 1, 1},
		{"space40", strings.Repeat(" ", 40), 1, 1},
		{"nl1", "\n", 1, 1},
		{"nl8", strings.Repeat("\n", 8), 1, 1},
		{"code_comment", "// retry with exponential backoff when ctx.Err() != nil, see runtime.go:877", 18, 18},
	}
}

// Core corpora that drive real prompt/compact decisions; these must stay
// within a tighter tolerance than the long tail of rare scripts.
var calibrationCore = map[string]bool{
	"en_prose": true, "zh_prose": true, "mixed": true, "go_code": true,
	"json_schema": true, "markdown": true, "emoji": true, "digits": true,
	"uuid": true, "code_comment": true,
}

func meanRelativeError(t *testing.T, profile Profile, column func(calibrationSample) int) (mean float64, worst calibrationSample, worstErr float64) {
	t.Helper()
	corpus := calibrationCorpus()
	total := 0.0
	for _, sample := range corpus {
		real := column(sample)
		estimate := Estimate(sample.text, profile)
		err := float64(estimate-real) / float64(real)
		if err < 0 {
			err = -err
		}
		total += err
		if err > worstErr {
			worst, worstErr = sample, err
		}
	}
	return total / float64(len(corpus)), worst, worstErr
}

func TestEstimateMatchesCalibrationCorpus(t *testing.T) {
	clMean, clWorst, clWorstErr := meanRelativeError(t, ProfileCL100k, func(s calibrationSample) int { return s.cl100k })
	o2Mean, o2Worst, o2WorstErr := meanRelativeError(t, ProfileO200k, func(s calibrationSample) int { return s.o200k })
	t.Logf("cl100k mean relative error %.3f (worst %s %.2f)", clMean, clWorst.name, clWorstErr)
	t.Logf("o200k  mean relative error %.3f (worst %s %.2f)", o2Mean, o2Worst.name, o2WorstErr)
	require.Less(t, clMean, 0.20, "cl100k mean relative error must stay below 20%%")
	require.Less(t, o2Mean, 0.20, "o200k mean relative error must stay below 20%%")

	for _, sample := range calibrationCorpus() {
		if !calibrationCore[sample.name] {
			continue
		}
		for _, tc := range []struct {
			profile Profile
			real    int
		}{
			{ProfileCL100k, sample.cl100k},
			{ProfileO200k, sample.o200k},
		} {
			estimate := Estimate(sample.text, tc.profile)
			relative := float64(estimate-tc.real) / float64(tc.real)
			require.Greaterf(t, relative, -0.30,
				"%s/%s underestimated: estimate=%d real=%d", sample.name, tc.profile.Name, estimate, tc.real)
			require.Lessf(t, relative, 0.30,
				"%s/%s overestimated: estimate=%d real=%d", sample.name, tc.profile.Name, estimate, tc.real)
		}
	}
}

// Anthropic references measured with @anthropic-ai/tokenizer (2026-10).
func TestEstimateMatchesAnthropicReferences(t *testing.T) {
	references := []struct {
		name string
		text string
		real int
	}{
		{"en_prose", calibrationCorpus()[0].text, 53},
		{"zh_prose", calibrationCorpus()[1].text, 125},
		{"json_schema", calibrationCorpus()[4].text, 51},
		{"emoji", calibrationCorpus()[6].text, 30},
		{"cyrillic", calibrationCorpus()[9].text, 25},
		{"cjk_rare", calibrationCorpus()[15].text, 25},
	}
	mean := 0.0
	for _, reference := range references {
		estimate := Estimate(reference.text, ProfileAnthropic)
		err := float64(estimate-reference.real) / float64(reference.real)
		if err < 0 {
			err = -err
		}
		mean += err
	}
	mean /= float64(len(references))
	t.Logf("anthropic mean relative error %.3f", mean)
	require.Less(t, mean, 0.25, "anthropic mean relative error must stay below 25%%")
}

func TestEstimateMessageFraming(t *testing.T) {
	profile := ProfileO200k
	message := types.Message{Role: "user", Content: "hello"}
	// 3 framing + 1 role + 1 content (reply priming is added per request).
	require.Equal(t, 5, EstimateMessages([]types.Message{message}, profile))

	two := []types.Message{{Role: "system", Content: "sys"}, message}
	// 2*3 framing + role/content = 10; + reply priming = 13, matching
	// gpt-tokenizer's encodeChat.
	require.Equal(t, 10, EstimateMessages(two, profile))
	require.Equal(t, 13, EstimateMessages(two, profile)+profile.ReplyPriming)
}

func TestEstimateScriptOrdering(t *testing.T) {
	profile := ProfileO200k
	ascii := Estimate("abcdefghijklmnopqrstuvwxyz", profile)
	chinese := Estimate("这是一个中文句子用于对比", profile)
	require.Greater(t, chinese, ascii, "dense scripts must cost more per rune than ASCII words")
}

func TestProfileForModelMapping(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     string
	}{
		{"openai", "gpt-4o-mini", ProfileO200k.Name},
		{"openai", "gpt-5.4-mini", ProfileO200k.Name},
		{"openai", "gpt-4-turbo", ProfileCL100k.Name},
		{"openai", "gpt-3.5-turbo", ProfileCL100k.Name},
		{"anthropic", "claude-sonnet-4", ProfileAnthropic.Name},
		{"deepseek", "deepseek-chat", ProfileGeneric.Name},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, ProfileFor(tc.provider, tc.model).Name,
			fmt.Sprintf("%s/%s", tc.provider, tc.model))
	}
}

func TestEstimateEmptyAndWhitespace(t *testing.T) {
	require.Zero(t, Estimate("", ProfileO200k))
	require.Zero(t, Estimate(" ", ProfileO200k), "a single space merges into the next word token")
	require.Equal(t, 1, Estimate(strings.Repeat(" ", 40), ProfileO200k))
}
