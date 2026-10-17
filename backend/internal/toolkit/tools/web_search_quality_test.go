package tools

import (
	"math"
	"testing"
)

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

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

// P3-3：title 命中权重高于 snippet、snippet 高于 URL-only。
func TestWebSearchQualityWeightsTitleAboveSnippetAboveURL(t *testing.T) {
	query := "github trending weekly ai repositories"
	titleOnly := assessWebSearchQuality(query, []DuckDuckGoResult{
		{Title: "GitHub trending weekly AI repositories"},
	})
	snippetOnly := assessWebSearchQuality(query, []DuckDuckGoResult{
		{Title: "无命中标题", Snippet: "github AI repositories this trending weekly"},
	})
	urlOnly := assessWebSearchQuality(query, []DuckDuckGoResult{
		{Title: "无命中标题", URL: "https://github.com/trending-ai/weekly-repositories"},
	})

	if !nearlyEqual(titleOnly.Signals["coverage"], 1) {
		t.Fatalf("title-only coverage = %v", titleOnly.Signals["coverage"])
	}
	if !nearlyEqual(snippetOnly.Signals["coverage"], webSearchQualitySnippetWeight) {
		t.Fatalf("snippet-only coverage = %v", snippetOnly.Signals["coverage"])
	}
	if !nearlyEqual(urlOnly.Signals["coverage"], webSearchQualityURLWeight) {
		t.Fatalf("url-only coverage = %v", urlOnly.Signals["coverage"])
	}
	if !(titleOnly.Score > snippetOnly.Score && snippetOnly.Score > urlOnly.Score) {
		t.Fatalf("score ordering broken: title=%v snippet=%v url=%v", titleOnly.Score, snippetOnly.Score, urlOnly.Score)
	}
}

// P3-3：无关结果恒为 0 且永不因调制因子虚高；低质语义不变。
func TestWebSearchQualityUnrelatedStaysZeroWithSignals(t *testing.T) {
	quality := assessWebSearchQuality("github trending weekly ai repositories", []DuckDuckGoResult{
		{Title: "完全无关", URL: "https://zhidao.example/1", Snippet: "别的领域"},
	})
	if quality.Score != 0 || quality.Tier != "low" {
		t.Fatalf("unrelated results must stay at 0: %#v", quality)
	}
	if !nearlyEqual(quality.Signals["domain_diversity"], 1) || !nearlyEqual(quality.Signals["snippet_presence"], 1) {
		t.Fatalf("signals = %#v", quality.Signals)
	}
}

// P3-3：域名多样性信号——同域重复压低多样性，多域名得高分。
func TestWebSearchQualityDomainDiversitySignal(t *testing.T) {
	singleDomain := assessWebSearchQuality("github trending", []DuckDuckGoResult{
		{Title: "GitHub Digest 1", Snippet: "trending repos", URL: "https://a.example/1"},
		{Title: "GitHub Digest 2", Snippet: "trending repos", URL: "https://a.example/2"},
		{Title: "GitHub Digest 3", Snippet: "trending repos", URL: "https://a.example/3"},
		{Title: "GitHub Digest 4", Snippet: "trending repos", URL: "https://a.example/4"},
		{Title: "GitHub Digest 5", Snippet: "trending repos", URL: "https://a.example/5"},
	})
	multiDomain := assessWebSearchQuality("github trending", []DuckDuckGoResult{
		{Title: "GitHub Digest 1", Snippet: "trending repos", URL: "https://a.example/1"},
		{Title: "GitHub Digest 2", Snippet: "trending repos", URL: "https://b.example/2"},
		{Title: "GitHub Digest 3", Snippet: "trending repos", URL: "https://c.example/3"},
		{Title: "GitHub Digest 4", Snippet: "trending repos", URL: "https://d.example/4"},
		{Title: "GitHub Digest 5", Snippet: "trending repos", URL: "https://e.example/5"},
	})
	if !nearlyEqual(singleDomain.Signals["domain_diversity"], 0.2) {
		t.Fatalf("single-domain diversity = %v", singleDomain.Signals["domain_diversity"])
	}
	if !nearlyEqual(multiDomain.Signals["domain_diversity"], 1) {
		t.Fatalf("multi-domain diversity = %v", multiDomain.Signals["domain_diversity"])
	}
	if !(multiDomain.Score > singleDomain.Score) {
		t.Fatalf("diversity must lift the score: single=%v multi=%v", singleDomain.Score, multiDomain.Score)
	}
}

// P3-3：短语命中仍直接 1.0（口径不变）。
func TestWebSearchQualityPhraseHitStillOne(t *testing.T) {
	quality := assessWebSearchQuality("github trending", []DuckDuckGoResult{
		{Title: "Weekly GitHub Trending Digest"},
	})
	if quality.Score != 1 || quality.Tier != "ok" {
		t.Fatalf("phrase hit must short-circuit to 1: %#v", quality)
	}
}
