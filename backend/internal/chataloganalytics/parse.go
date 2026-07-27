package chataloganalytics

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// chatSessionFile is a lightweight projection of chat_*.json (messages ignored).
type chatSessionFile struct {
	SessionID         string               `json:"session_id"`
	RuntimeSessionID  string               `json:"runtime_session_id"`
	Title             string               `json:"title"`
	InitialMessage    string               `json:"initial_message"`
	WorkingDirectory  string               `json:"working_directory"`
	ProjectPath       string               `json:"project_path"`
	StartTime         time.Time            `json:"start_time"`
	EndTime           time.Time            `json:"end_time"`
	LastObservedAt    time.Time            `json:"last_observed_at"`
	Status            string               `json:"status"`
	TerminationReason string               `json:"termination_reason"`
	Provider          string               `json:"provider"`
	Protocol          string               `json:"protocol"`
	Model             string               `json:"model"`
	BaseURL           string               `json:"base_url"`
	Stream            bool                 `json:"stream"`
	Messages          []chatSessionMessage `json:"messages"`
	DroppedMessages   int                  `json:"dropped_messages"`
	Summary           *chatSessionSummary  `json:"summary"`
}

type chatSessionMessage struct {
	Timestamp   time.Time       `json:"timestamp"`
	MessageType string          `json:"message_type"`
	TurnID      string          `json:"turn_id"`
	RequestID   string          `json:"request_id"`
	Content     json.RawMessage `json:"content"`
}

type chatEvidence struct {
	TraceToTurn       map[string]string
	ToolResultsByTurn map[string]int
	ToolErrorsByTurn  map[string]int
	ToolResults       int
	ToolErrors        int
}

type chatSessionSummary struct {
	TotalRequests         int            `json:"total_requests"`
	TotalResponses        int            `json:"total_responses"`
	TotalToolCalls        int            `json:"total_tool_calls"`
	TotalTokens           int            `json:"total_tokens"`
	AverageResponseTimeMs int64          `json:"average_response_time_ms"`
	TotalDurationMs       int64          `json:"total_duration_ms"`
	UsageInfo             map[string]int `json:"usage_info"`
}

func findChatLogFile(sessionDir string) (string, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return "", err
	}
	var best string
	var bestMod time.Time
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "chat_") || !strings.HasSuffix(lower, ".json") {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			continue
		}
		full := filepath.Join(sessionDir, name)
		if best == "" || info.ModTime().After(bestMod) {
			best = full
			bestMod = info.ModTime()
		}
	}
	return best, nil
}

func loadChatSessionFile(path string) (*chatSessionFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed chatSessionFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func rollupFromChatFile(dir SessionDir, chatPath string, file *chatSessionFile) SessionRollup {
	rollup := SessionRollup{
		SessionID: dir.SessionID,
		Directory: dir.Directory,
		RelPath:   dir.RelPath,
		Source:    "summary",
	}
	if file == nil {
		rollup.StartTime = sessionStartHint(dir)
		return rollup
	}
	if strings.TrimSpace(file.SessionID) != "" {
		rollup.SessionID = file.SessionID
	}
	rollup.StartTime = file.StartTime
	if rollup.StartTime.IsZero() {
		rollup.StartTime = sessionStartHint(dir)
	}
	rollup.EndTime = file.EndTime
	rollup.LastObservedAt = file.LastObservedAt
	rollup.Status = strings.TrimSpace(file.Status)
	rollup.RuntimeSessionID = strings.TrimSpace(file.RuntimeSessionID)
	if title := normalizeSessionTitle(file.Title); title != "" {
		rollup.Title = title
		rollup.TitleSource = "chat_log"
	} else if title := normalizeSessionTitle(file.InitialMessage); title != "" {
		rollup.Title = title
		rollup.TitleSource = "initial_message"
	}
	rollup.Project = inferSessionProject(file)
	rollup.Provider = strings.TrimSpace(file.Provider)
	rollup.Protocol = strings.TrimSpace(file.Protocol)
	rollup.Model = strings.TrimSpace(file.Model)
	rollup.BaseURL = strings.TrimSpace(file.BaseURL)
	rollup.Stream = file.Stream
	rollup.DroppedMessages = file.DroppedMessages
	if file.Summary != nil {
		rollup.TotalRequests = file.Summary.TotalRequests
		rollup.TotalResponses = file.Summary.TotalResponses
		rollup.TotalToolCalls = file.Summary.TotalToolCalls
		rollup.TotalTokens = file.Summary.TotalTokens
		rollup.AverageResponseTimeMs = file.Summary.AverageResponseTimeMs
		rollup.TotalDurationMs = file.Summary.TotalDurationMs
	}
	return rollup
}

func normalizeSessionTitle(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	const maxRunes = 120
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

func inferSessionProject(file *chatSessionFile) string {
	if file == nil {
		return ""
	}
	if project := normalizeProjectCandidate(file.ProjectPath); project != "" {
		return projectRootFor(project)
	}

	candidates := make([]string, 0, 8)
	if workingDirectory := normalizeProjectCandidate(file.WorkingDirectory); workingDirectory != "" {
		candidates = append(candidates, workingDirectory)
	}
	for _, message := range file.Messages {
		if message.MessageType != "tool_call" || len(message.Content) == 0 {
			continue
		}
		var content interface{}
		if json.Unmarshal(message.Content, &content) != nil {
			continue
		}
		collectProjectCandidates(content, &candidates)
	}
	if len(candidates) == 0 {
		return ""
	}

	counts := make(map[string]int, len(candidates))
	firstSeen := make(map[string]int, len(candidates))
	for index, candidate := range candidates {
		project := projectRootFor(candidate)
		if project == "" {
			continue
		}
		key := strings.ToLower(project)
		counts[key]++
		if _, exists := firstSeen[key]; !exists {
			firstSeen[key] = index
		}
		candidates[index] = project
	}

	bestProject := ""
	bestCount := 0
	bestIndex := len(candidates)
	for _, project := range candidates {
		if project == "" {
			continue
		}
		key := strings.ToLower(project)
		count := counts[key]
		first := firstSeen[key]
		if count > bestCount || (count == bestCount && first < bestIndex) {
			bestProject = project
			bestCount = count
			bestIndex = first
		}
	}
	return bestProject
}

func collectProjectCandidates(value interface{}, candidates *[]string) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if normalizedKey == "workdir" || normalizedKey == "cwd" {
				if candidate := normalizeProjectCandidate(stringValue(child)); candidate != "" {
					*candidates = append(*candidates, candidate)
				}
			}
			collectProjectCandidates(child, candidates)
		}
	case []interface{}:
		for _, child := range typed {
			collectProjectCandidates(child, candidates)
		}
	}
}

func normalizeProjectCandidate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "..." || value == "\u2026" || !filepath.IsAbs(value) {
		return ""
	}
	return filepath.Clean(value)
}

func projectRootFor(candidate string) string {
	candidate = normalizeProjectCandidate(candidate)
	if candidate == "" {
		return ""
	}
	dir := candidate
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return filepath.Clean(current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return filepath.Clean(dir)
}

func parseChatEvidence(file *chatSessionFile) chatEvidence {
	evidence := chatEvidence{
		TraceToTurn:       make(map[string]string),
		ToolResultsByTurn: make(map[string]int),
		ToolErrorsByTurn:  make(map[string]int),
	}
	if file == nil {
		return evidence
	}
	for _, message := range file.Messages {
		turnID := strings.TrimSpace(message.TurnID)
		if message.MessageType == "request" || message.MessageType == "response" {
			var content struct {
				TraceID       string `json:"trace_id"`
				LogicalTurnID string `json:"logical_turn_id"`
			}
			if json.Unmarshal(message.Content, &content) == nil {
				traceID := strings.TrimSpace(content.TraceID)
				if traceID == "" {
					traceID = strings.TrimSpace(content.LogicalTurnID)
				}
				if traceID != "" && turnID != "" {
					evidence.TraceToTurn[traceID] = turnID
				}
			}
		}
		if message.MessageType != "tool_result" {
			continue
		}

		var envelope struct {
			Result map[string]interface{} `json:"result"`
		}
		if json.Unmarshal(message.Content, &envelope) != nil || envelope.Result == nil {
			continue
		}
		evidence.ToolResults++
		if turnID != "" {
			evidence.ToolResultsByTurn[turnID]++
		}
		if toolResultFailed(envelope.Result) {
			evidence.ToolErrors++
			if turnID != "" {
				evidence.ToolErrorsByTurn[turnID]++
			}
		}
	}
	return evidence
}

func toolResultFailed(result map[string]interface{}) bool {
	if value, ok := result["ok"].(bool); ok && !value {
		return true
	}
	if value, ok := result["success"].(bool); ok && !value {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(result["outcome"]))) {
	case "failed", "error", "denied", "cancelled", "timeout":
		return true
	}
	return strings.TrimSpace(stringValue(result["error"])) != ""
}

func stringValue(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// ParseDebugRequestFinished parses debug.log lines containing [llm-debug] request_finished.
func ParseDebugRequestFinished(path string) ([]StepUsage, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	steps := make([]StepUsage, 0, 32)
	started := make(map[string]StepUsage)
	scanner := bufio.NewScanner(file)
	// allow long debug lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "[llm-debug]") {
			continue
		}
		if strings.Contains(line, "request_started") {
			step, ok := parseRequestStartedLine(line)
			if ok {
				started[debugStepKey(step.TraceID, step.Step)] = step
			}
			continue
		}
		if !strings.Contains(line, "request_finished") {
			continue
		}
		step, ok := parseRequestFinishedLine(line)
		if !ok {
			continue
		}
		if prior, exists := started[debugStepKey(step.TraceID, step.Step)]; exists {
			step.StartedAt = prior.StartedAt
			step.ContextPromptTokens = prior.ContextPromptTokens
			step.ContextWindowTokens = prior.ContextWindowTokens
			step.PromptBudget = prior.PromptBudget
			if !step.StartedAt.IsZero() && !step.Timestamp.IsZero() {
				step.DurationMs = step.Timestamp.Sub(step.StartedAt).Milliseconds()
			}
		}
		step.ContextUtilization = deriveContextUtilization(step)
		steps = append(steps, step)
	}
	if err := scanner.Err(); err != nil {
		return steps, err
	}
	return steps, nil
}

func deriveContextUtilization(step StepUsage) float64 {
	if step.ContextWindowTokens <= 0 {
		return 0
	}
	promptTokens := step.ContextPromptTokens
	if promptTokens <= 0 {
		promptTokens = step.PromptTokens
		if derivedInput := step.TotalTokens - step.CompletionTokens; derivedInput > promptTokens {
			promptTokens = derivedInput
		}
	}
	if promptTokens <= 0 {
		return 0
	}
	return float64(promptTokens) / float64(step.ContextWindowTokens)
}

func debugStepKey(traceID string, step int) string {
	return strings.TrimSpace(traceID) + ":" + strconv.Itoa(step)
}

func parseRequestStartedLine(line string) (StepUsage, bool) {
	timestamp, fields, ok := parseDebugLine(line, "request_started")
	if !ok {
		return StepUsage{}, false
	}
	step := StepUsage{StartedAt: timestamp}
	step.TraceID = fields["trace_id"]
	step.Step = atoiDefault(fields["step"])
	step.ContextPromptTokens = atoiDefault(fields["context_prompt_tokens"])
	step.ContextWindowTokens = atoiDefault(fields["context_window_tokens"])
	step.PromptBudget = atoiDefault(fields["prompt_budget"])
	return step, step.TraceID != "" || step.Step > 0
}

func parseRequestFinishedLine(line string) (StepUsage, bool) {
	timestamp, fields, ok := parseDebugLine(line, "request_finished")
	if !ok {
		return StepUsage{}, false
	}
	step := StepUsage{Timestamp: timestamp, Success: true}

	for key, value := range fields {
		switch key {
		case "trace_id":
			step.TraceID = value
		case "step":
			if n, err := strconv.Atoi(value); err == nil {
				step.Step = n
			}
		case "success":
			step.Success = value == "true" || value == "1" || value == "yes"
		case "usage_prompt_tokens":
			step.PromptTokens = atoiDefault(value)
		case "usage_completion_tokens":
			step.CompletionTokens = atoiDefault(value)
		case "usage_total_tokens":
			step.TotalTokens = atoiDefault(value)
		case "usage_cached_tokens":
			step.CachedTokens = atoiDefault(value)
		case "usage_cache_read_tokens":
			step.CacheReadTokens = atoiDefault(value)
		case "usage_cache_read_reported":
			step.CacheReadReported = value == "true" || value == "1" || value == "yes"
		case "usage_cache_hit_ratio":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				step.CacheHitRatio = f
			}
		case "usage_cache_status":
			step.CacheStatus = value
		case "usage_reasoning_tokens":
			step.ReasoningTokens = atoiDefault(value)
		case "usage_source":
			step.UsageSource = value
		case "error":
			if strings.TrimSpace(value) != "" {
				step.Success = false
				step.ErrorCategory = classifyLLMError(value)
			}
		}
	}

	if step.TotalTokens == 0 && (step.PromptTokens > 0 || step.CompletionTokens > 0) {
		step.TotalTokens = step.PromptTokens + step.CompletionTokens
	}
	step.UsageAvailable = step.UsageSource != "" || step.TotalTokens > 0 || step.PromptTokens > 0 || step.CompletionTokens > 0
	return step, true
}

func parseDebugLine(line, marker string) (time.Time, map[string]string, bool) {
	payload := strings.TrimSpace(line)
	if payload == "" {
		return time.Time{}, nil, false
	}
	var timestamp time.Time
	if strings.HasPrefix(payload, "[") {
		if end := strings.Index(payload, "]"); end > 1 {
			tsText := strings.TrimSpace(payload[1:end])
			for _, layout := range []string{"2006-01-02 15:04:05.000", "2006-01-02 15:04:05"} {
				if parsed, err := time.ParseInLocation(layout, tsText, time.Local); err == nil {
					timestamp = parsed
					break
				}
			}
		}
	}
	idx := strings.Index(payload, marker)
	if idx < 0 {
		return timestamp, nil, false
	}
	fields := splitDebugFields(strings.TrimSpace(payload[idx+len(marker):]))
	return timestamp, fields, len(fields) > 0
}

func splitDebugFields(payload string) map[string]string {
	fields := make(map[string]string)
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return fields
	}

	for pos := 0; pos < len(payload); {
		for pos < len(payload) && (payload[pos] == ' ' || payload[pos] == '\t') {
			pos++
		}
		keyStart := pos
		for pos < len(payload) && payload[pos] != '=' && payload[pos] != ' ' && payload[pos] != '\t' {
			pos++
		}
		if pos >= len(payload) || payload[pos] != '=' {
			for pos < len(payload) && payload[pos] != ' ' && payload[pos] != '\t' {
				pos++
			}
			continue
		}
		key := strings.TrimSpace(payload[keyStart:pos])
		pos++
		valueStart := pos
		if pos < len(payload) && (payload[pos] == '"' || payload[pos] == '\'') {
			quote := payload[pos]
			pos++
			escaped := false
			for pos < len(payload) {
				char := payload[pos]
				pos++
				if escaped {
					escaped = false
					continue
				}
				if char == '\\' {
					escaped = true
					continue
				}
				if char == quote {
					break
				}
			}
		} else {
			for pos < len(payload) && payload[pos] != ' ' && payload[pos] != '\t' {
				pos++
			}
		}
		value := strings.TrimSpace(payload[valueStart:pos])
		value = strings.Trim(value, "\"'")
		if key != "" {
			if _, exists := fields[key]; !exists {
				fields[key] = value
			}
		}
	}
	return fields
}

func classifyLLMError(message string) string {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "context canceled"), strings.Contains(lower, "cancelled"), strings.Contains(lower, "interrupted"):
		return "cancelled"
	case strings.Contains(lower, "model_not_found"), strings.Contains(lower, "model not found"), strings.Contains(lower, "http 404"):
		return "model_not_found"
	case strings.Contains(lower, "http 401"), strings.Contains(lower, "http 403"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "api key"):
		return "authentication"
	case strings.Contains(lower, "http 429"), strings.Contains(lower, "rate limit"):
		return "rate_limit"
	case strings.Contains(lower, "http 502"), strings.Contains(lower, "http 503"), strings.Contains(lower, "http 504"), strings.Contains(lower, "upstream"):
		return "upstream_unavailable"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return "timeout"
	case strings.Contains(lower, "tls"), strings.Contains(lower, "certificate"), strings.Contains(lower, "connection reset"), strings.Contains(lower, "read tcp"):
		return "network"
	case strings.Contains(lower, "context length"), strings.Contains(lower, "context window"), strings.Contains(lower, "too many tokens"):
		return "context_limit"
	case strings.Contains(lower, "bad request"), strings.Contains(lower, "http 400"), strings.Contains(lower, "invalid argument"):
		return "invalid_request"
	default:
		return "unknown"
	}
}

func atoiDefault(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func applyDebugSteps(rollup *SessionRollup, steps []StepUsage) {
	if rollup == nil {
		return
	}
	var prompt, completion, total, cached, reasoning int
	var success, errors, withUsage int
	usageSources := make(map[string]struct{})
	for _, step := range steps {
		prompt += step.PromptTokens
		completion += step.CompletionTokens
		total += step.TotalTokens
		cached += step.CachedTokens
		reasoning += step.ReasoningTokens
		if step.UsageAvailable {
			withUsage++
		}
		if source := strings.TrimSpace(step.UsageSource); source != "" {
			usageSources[source] = struct{}{}
		}
		if step.Success {
			success++
		} else {
			errors++
		}
	}
	rollup.HasDebugUsage = len(steps) > 0
	rollup.LLMRequests = len(steps)
	rollup.LLMRequestsWithUsage = withUsage
	rollup.LLMSuccesses = success
	rollup.LLMErrors = errors
	rollup.PromptTokens = prompt
	rollup.CompletionTokens = completion
	rollup.CachedTokens = cached
	rollup.ReasoningTokens = reasoning
	if rollup.TotalTokens == 0 && total > 0 {
		rollup.TotalTokens = total
		if rollup.Source == "summary" {
			rollup.Source = "mixed"
		} else if rollup.Source == "" {
			rollup.Source = "debug"
		}
	} else if rollup.Source == "" {
		rollup.Source = "debug"
	}

	expectedRequests := rollup.TotalRequests
	if expectedRequests <= 0 {
		expectedRequests = len(steps)
	}
	if expectedRequests > 0 {
		rollup.UsageCoverage = float64(withUsage) / float64(expectedRequests)
		if rollup.UsageCoverage > 1 {
			rollup.UsageCoverage = 1
		}
	}
	rollup.UsageComplete = expectedRequests > 0 && withUsage >= expectedRequests
	switch {
	case len(usageSources) > 1:
		rollup.UsageQuality = "mixed"
	case len(usageSources) == 1:
		for source := range usageSources {
			rollup.UsageQuality = source
		}
	case rollup.TotalTokens > 0:
		rollup.UsageQuality = "summary_only"
	default:
		rollup.UsageQuality = "missing"
	}

	rollup.ReconciliationStatus = "unavailable"
	if rollup.TotalTokens > 0 && total > 0 {
		rollup.ReconciliationDelta = rollup.TotalTokens - total
		if rollup.ReconciliationDelta == 0 {
			rollup.ReconciliationStatus = "matched"
		} else if len(steps) < expectedRequests {
			rollup.ReconciliationStatus = "partial"
		} else {
			rollup.ReconciliationStatus = "mismatch"
		}
	}

	rollup.PartialReasons = rollup.PartialReasons[:0]
	if len(steps) < expectedRequests {
		rollup.PartialReasons = append(rollup.PartialReasons, "llm_request_history_incomplete")
	}
	if !rollup.UsageComplete {
		rollup.PartialReasons = append(rollup.PartialReasons, "usage_missing")
	}
	if rollup.DroppedMessages > 0 {
		rollup.PartialReasons = append(rollup.PartialReasons, "chat_messages_dropped")
	}
	if rollup.ReconciliationStatus == "mismatch" {
		rollup.PartialReasons = append(rollup.PartialReasons, "usage_reconciliation_mismatch")
	}
	rollup.Partial = len(rollup.PartialReasons) > 0
}

func buildTurns(steps []StepUsage, evidence chatEvidence) []TurnUsage {
	index := make(map[string]int)
	turns := make([]TurnUsage, 0, 8)

	for _, step := range steps {
		traceID := strings.TrimSpace(step.TraceID)
		key := traceID
		if key == "" {
			key = "unknown"
		}
		idx, exists := index[key]
		if !exists {
			idx = len(turns)
			index[key] = idx
			turnID := evidence.TraceToTurn[traceID]
			if turnID == "" {
				turnID = traceID
			}
			turns = append(turns, TurnUsage{TurnID: turnID, TraceID: traceID})
		}
		turn := &turns[idx]
		turn.LLMRequests++
		if step.Success {
			turn.LLMSuccesses++
		} else {
			turn.LLMErrors++
			turn.ErrorCategory = step.ErrorCategory
		}
		turn.Usage.PromptTokens += step.PromptTokens
		turn.Usage.CompletionTokens += step.CompletionTokens
		turn.Usage.TotalTokens += step.TotalTokens
		turn.Usage.CachedTokens += step.CachedTokens
		turn.Usage.ReasoningTokens += step.ReasoningTokens
		turn.MaxContextUtilization = max(turn.MaxContextUtilization, step.ContextUtilization)
		start := step.StartedAt
		if start.IsZero() {
			start = step.Timestamp
		}
		if turn.StartedAt.IsZero() || (!start.IsZero() && start.Before(turn.StartedAt)) {
			turn.StartedAt = start
		}
		if step.Timestamp.After(turn.EndedAt) {
			turn.EndedAt = step.Timestamp
		}
		if step.Success {
			if turn.LLMErrors > 0 {
				turn.Outcome = "recovered"
			} else {
				turn.Outcome = "success"
			}
		} else if step.ErrorCategory == "cancelled" {
			turn.Outcome = "cancelled"
		} else {
			turn.Outcome = "failed"
		}
	}

	sort.SliceStable(turns, func(i, j int) bool {
		return turns[i].StartedAt.Before(turns[j].StartedAt)
	})
	for idx := range turns {
		turn := &turns[idx]
		turn.Ordinal = idx + 1
		if !turn.StartedAt.IsZero() && !turn.EndedAt.IsZero() {
			turn.DurationMs = turn.EndedAt.Sub(turn.StartedAt).Milliseconds()
		}
		turn.UsageCoverage = usageCoverageForSteps(steps, turn.TraceID)
		turn.UsageQuality = usageQualityForSteps(steps, turn.TraceID)
		turn.ToolResultsObserved = evidence.ToolResultsByTurn[turn.TurnID]
		turn.ToolErrors = evidence.ToolErrorsByTurn[turn.TurnID]
	}
	return turns
}

func usageQualityForSteps(steps []StepUsage, traceID string) string {
	sources := make(map[string]struct{})
	available := 0
	total := 0
	for _, step := range steps {
		if strings.TrimSpace(step.TraceID) != strings.TrimSpace(traceID) {
			continue
		}
		total++
		if step.UsageAvailable {
			available++
		}
		if step.UsageSource != "" {
			sources[step.UsageSource] = struct{}{}
		}
	}
	if available == 0 {
		return "missing"
	}
	if available < total || len(sources) > 1 {
		return "mixed"
	}
	for source := range sources {
		return source
	}
	return "reported"
}

func usageCoverageForSteps(steps []StepUsage, traceID string) float64 {
	available := 0
	total := 0
	for _, step := range steps {
		if strings.TrimSpace(step.TraceID) != strings.TrimSpace(traceID) {
			continue
		}
		total++
		if step.UsageAvailable {
			available++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(available) / float64(total)
}

func loadSessionRollup(dir SessionDir, enrichDebug bool) (SessionRollup, []StepUsage, chatEvidence, error) {
	chatPath, err := findChatLogFile(dir.Path)
	if err != nil {
		return SessionRollup{}, nil, chatEvidence{}, err
	}

	var rollup SessionRollup
	evidence := parseChatEvidence(nil)
	if chatPath != "" {
		file, loadErr := loadChatSessionFile(chatPath)
		if loadErr != nil {
			// still allow debug-only sessions
			rollup = SessionRollup{
				SessionID: dir.SessionID,
				Directory: dir.Directory,
				RelPath:   dir.RelPath,
				StartTime: sessionStartHint(dir),
				Source:    "summary",
			}
		} else {
			rollup = rollupFromChatFile(dir, chatPath, file)
			evidence = parseChatEvidence(file)
		}
	} else {
		rollup = SessionRollup{
			SessionID: dir.SessionID,
			Directory: dir.Directory,
			RelPath:   dir.RelPath,
			StartTime: sessionStartHint(dir),
			Source:    "debug",
		}
	}

	debugPath := filepath.Join(dir.Path, "debug.log")
	var steps []StepUsage
	if enrichDebug {
		parsed, parseErr := ParseDebugRequestFinished(debugPath)
		if parseErr != nil {
			return rollup, nil, evidence, parseErr
		}
		steps = parsed
		applyDebugSteps(&rollup, steps)
		turns := buildTurns(steps, evidence)
		rollup.TurnCount = len(turns)
		for _, turn := range turns {
			switch turn.Outcome {
			case "failed", "cancelled":
				rollup.FailedTurns++
			case "recovered":
				rollup.RecoveredTurns++
			}
		}
		rollup.ToolResultsObserved = evidence.ToolResults
		rollup.ToolErrors = evidence.ToolErrors
	} else if _, statErr := os.Stat(debugPath); statErr == nil {
		// cheap existence flag without full parse
		rollup.HasDebugUsage = false
	}

	return rollup, steps, evidence, nil
}
