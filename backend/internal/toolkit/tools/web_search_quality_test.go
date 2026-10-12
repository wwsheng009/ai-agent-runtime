package tools

import "testing"

func TestWebSearchQueryTermsKeepsCJKAndDropsStopWords(t *testing.T) {
	terms := webSearchQueryTerms("最近一周 GitHub Trending AI 项目 the and")
	want := map[string]bool{
		"最近一周":     true,
		"github":   true,
		"trending": true,
		"ai":       true,
		"项目":       true,
	}
	if len(terms) != len(want) {
		t.Fatalf("terms = %#v, want %d terms", terms, len(want))
	}
	for _, term := range terms {
		if !want[term] {
			t.Fatalf("unexpected term %q in %#v", term, terms)
		}
	}
}

func TestAssessWebSearchQualityMarksUnrelatedResultsLow(t *testing.T) {
	quality := assessWebSearchQuality("GitHub trending weekly AI repositories", []DuckDuckGoResult{
		{Title: "possess和possession的短语", URL: "https://zhidao.example/", Snippet: "英语词义"},
	})
	if quality.Tier != "low" || quality.Score != 0 {
		t.Fatalf("expected low quality, got %#v", quality)
	}
}

func TestAssessWebSearchQualityAcceptsRelevantResults(t *testing.T) {
	quality := assessWebSearchQuality("GitHub trending weekly AI repositories", []DuckDuckGoResult{
		{Title: "GitHub trending repositories", URL: "https://github.com/trending", Snippet: "weekly AI repositories"},
	})
	if quality.Tier != "ok" || quality.Score < webSearchQualityThreshold() {
		t.Fatalf("expected ok quality, got %#v", quality)
	}
}

func TestWebSearchQualityThresholdFromEnv(t *testing.T) {
	t.Setenv(webSearchQualityThresholdEnv, "0.75")
	if got := webSearchQualityThreshold(); got != 0.75 {
		t.Fatalf("threshold = %v, want 0.75", got)
	}
	t.Setenv(webSearchQualityThresholdEnv, "invalid")
	if got := webSearchQualityThreshold(); got != defaultWebSearchQualityThreshold {
		t.Fatalf("invalid threshold = %v, want default %v", got, defaultWebSearchQualityThreshold)
	}
}
