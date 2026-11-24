package memoryeval

import (
	"context"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type RetrievalCase struct {
	Name         string
	Query        string
	ExpectedIDs  []string
	Limit        int
	Scope        memorystore.ScopeContext
	MinRecallAt1 float64
}

type RetrievalCaseResult struct {
	Name      string
	RankedIDs []string
	RecallAt1 float64
	RecallAt3 float64
	MRR       float64
	LatencyMS float64
	Pass      bool
}

type RetrievalReport struct {
	Cases         []RetrievalCaseResult
	MeanRecallAt1 float64
	MeanRecallAt3 float64
	MeanMRR       float64
	P50LatencyMS  float64
	P95LatencyMS  float64
	Pass          bool
	FailedCases   []string
}

func EvaluateRetrieval(
	ctx context.Context, store *memorystore.Store, cases []RetrievalCase,
) (RetrievalReport, error) {
	report := RetrievalReport{Cases: make([]RetrievalCaseResult, 0, len(cases))}
	report.Pass = true
	recall1 := make([]float64, 0, len(cases))
	recall3 := make([]float64, 0, len(cases))
	mrr := make([]float64, 0, len(cases))
	latencies := make([]float64, 0, len(cases))
	for _, evalCase := range cases {
		limit := evalCase.Limit
		if limit <= 0 {
			limit = 10
		}
		opCtx := ctx
		if evalCase.Scope.ProjectKey != "" || evalCase.Scope.OwnerID != "" ||
			evalCase.Scope.AgentID != "" || evalCase.Scope.SessionID != "" || evalCase.Scope.TaskID != "" {
			opCtx = memorystore.WithMemoryScope(ctx, evalCase.Scope)
		}
		started := time.Now()
		page, err := store.SearchRecords(opCtx, memorystore.RecordSearchQuery{
			Text: evalCase.Query, Limit: limit,
		})
		if err != nil {
			return RetrievalReport{}, err
		}
		latency := float64(time.Since(started)) / float64(time.Millisecond)
		ranked := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			ranked = append(ranked, item.Record.ID)
		}
		r1 := RecallAtK(ranked, evalCase.ExpectedIDs, 1)
		r3 := RecallAtK(ranked, evalCase.ExpectedIDs, 3)
		rr := ReciprocalRank(ranked, evalCase.ExpectedIDs)
		pass := evalCase.MinRecallAt1 <= 0 || r1 >= evalCase.MinRecallAt1
		if !pass {
			report.Pass = false
			report.FailedCases = append(report.FailedCases, evalCase.Name)
		}
		report.Cases = append(report.Cases, RetrievalCaseResult{
			Name: evalCase.Name, RankedIDs: ranked, RecallAt1: r1,
			RecallAt3: r3, MRR: rr, LatencyMS: latency, Pass: pass,
		})
		recall1 = append(recall1, r1)
		recall3 = append(recall3, r3)
		mrr = append(mrr, rr)
		latencies = append(latencies, latency)
	}
	report.MeanRecallAt1 = Mean(recall1)
	report.MeanRecallAt3 = Mean(recall3)
	report.MeanMRR = Mean(mrr)
	report.P50LatencyMS = Percentile(latencies, 0.50)
	report.P95LatencyMS = Percentile(latencies, 0.95)
	return report, nil
}

type InjectionCase struct {
	Name         string
	Query        string
	ExpectedIDs  []string
	Limit        int
	TokenBudget  int
	Scope        memorystore.ScopeContext
	MinRecallAt1 float64
}

func EvaluateInjection(
	ctx context.Context, store *memorystore.Store, cases []InjectionCase,
) (RetrievalReport, error) {
	report := RetrievalReport{Cases: make([]RetrievalCaseResult, 0, len(cases)), Pass: true}
	recall1 := make([]float64, 0, len(cases))
	recall3 := make([]float64, 0, len(cases))
	mrr := make([]float64, 0, len(cases))
	latencies := make([]float64, 0, len(cases))
	for _, evalCase := range cases {
		started := time.Now()
		selection, err := store.SelectForInjectV2(memorystore.InjectOptions{
			Query:       evalCase.Query,
			Limit:       evalCase.Limit,
			TokenBudget: evalCase.TokenBudget,
			Scope:       evalCase.Scope,
			SkipTrace:   true,
		})
		if err != nil {
			return RetrievalReport{}, err
		}
		latency := float64(time.Since(started)) / float64(time.Millisecond)
		ranked := append([]string(nil), selection.IDs...)
		r1 := RecallAtK(ranked, evalCase.ExpectedIDs, 1)
		r3 := RecallAtK(ranked, evalCase.ExpectedIDs, 3)
		rr := ReciprocalRank(ranked, evalCase.ExpectedIDs)
		pass := evalCase.MinRecallAt1 <= 0 || r1 >= evalCase.MinRecallAt1
		if !pass {
			report.Pass = false
			report.FailedCases = append(report.FailedCases, evalCase.Name)
		}
		report.Cases = append(report.Cases, RetrievalCaseResult{
			Name: evalCase.Name, RankedIDs: ranked, RecallAt1: r1,
			RecallAt3: r3, MRR: rr, LatencyMS: latency, Pass: pass,
		})
		recall1 = append(recall1, r1)
		recall3 = append(recall3, r3)
		mrr = append(mrr, rr)
		latencies = append(latencies, latency)
	}
	report.MeanRecallAt1 = Mean(recall1)
	report.MeanRecallAt3 = Mean(recall3)
	report.MeanMRR = Mean(mrr)
	report.P50LatencyMS = Percentile(latencies, 0.50)
	report.P95LatencyMS = Percentile(latencies, 0.95)
	return report, nil
}
