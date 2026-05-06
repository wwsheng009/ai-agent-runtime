package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/pkg/skillsapi"
)

type demoOptions struct {
	mode                       string
	baseURL                    string
	message                    string
	sessionID                  string
	parentSessionID            string
	agentID                    string
	agentIDsCSV                string
	agentAction                string
	agentType                  string
	agentModel                 string
	userID                     string
	tenantID                   string
	projectID                  string
	workspacePath              string
	adminToken                 string
	bearerToken                string
	planningMode               string
	maxSteps                   int
	timeout                    time.Duration
	agentTimeoutMs             int
	afterSeq                   int64
	limit                      int
	waitMs                     int
	stream                     bool
	forkContext                bool
	interrupt                  bool
	enableRouting              bool
	executePlannedSubagents    bool
	allowWritePlannedSubagents bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	opts, err := parseDemoOptions(args)
	if err != nil {
		return err
	}

	clientOptions := make([]skillsapi.Option, 0, 3)
	if strings.TrimSpace(opts.adminToken) != "" {
		clientOptions = append(clientOptions, skillsapi.WithAdminToken(strings.TrimSpace(opts.adminToken)))
	}
	if strings.TrimSpace(opts.bearerToken) != "" {
		clientOptions = append(clientOptions, skillsapi.WithHeader("Authorization", "Bearer "+strings.TrimSpace(opts.bearerToken)))
	}
	client := skillsapi.NewClient(opts.baseURL, clientOptions...)

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if strings.EqualFold(strings.TrimSpace(opts.mode), "session-agent") {
		return runSessionAgentDemo(ctx, client, opts, stdout)
	}

	req := skillsapi.AgentChatRequest{
		Messages: []skillsapi.Message{{
			Role:    "user",
			Content: opts.message,
		}},
		SessionID:                  opts.sessionID,
		UserID:                     opts.userID,
		TenantID:                   opts.tenantID,
		ProjectID:                  opts.projectID,
		WorkspacePath:              opts.workspacePath,
		MaxSteps:                   opts.maxSteps,
		EnableRouting:              opts.enableRouting,
		PlanningMode:               opts.planningMode,
		ExecutePlannedSubagents:    opts.executePlannedSubagents,
		AllowWritePlannedSubagents: opts.allowWritePlannedSubagents,
		Stream:                     opts.stream,
	}

	if opts.stream {
		return runStreamDemo(ctx, client, req, stdout, stderr)
	}
	return runNonStreamDemo(ctx, client, req, stdout)
}

func parseDemoOptions(args []string) (demoOptions, error) {
	var opts demoOptions
	fs := flag.NewFlagSet("skillsapi-demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&opts.mode, "mode", "chat", "demo mode: chat or session-agent")
	fs.StringVar(&opts.baseURL, "url", "http://127.0.0.1:8101", "skills api base url")
	fs.StringVar(&opts.message, "message", "", "user message to send")
	fs.StringVar(&opts.sessionID, "session-id", "", "existing session id")
	fs.StringVar(&opts.parentSessionID, "parent-session-id", "", "parent session id for session-agent mode")
	fs.StringVar(&opts.agentID, "agent-id", "", "child agent/session id for session-agent mode")
	fs.StringVar(&opts.agentIDsCSV, "agent-ids", "", "comma-separated child agent/session ids for batch wait")
	fs.StringVar(&opts.agentAction, "agent-action", "spawn", "session-agent action: spawn, status, input, wait, events, close, resume")
	fs.StringVar(&opts.agentType, "agent-type", "", "child agent type for session-agent spawn")
	fs.StringVar(&opts.agentModel, "agent-model", "", "child agent model for session-agent spawn")
	fs.StringVar(&opts.userID, "user-id", "skillsapi-demo", "user id")
	fs.StringVar(&opts.tenantID, "tenant-id", "", "tenant id")
	fs.StringVar(&opts.projectID, "project-id", "", "project id")
	fs.StringVar(&opts.workspacePath, "workspace-path", "", "workspace path")
	fs.StringVar(&opts.adminToken, "admin-token", "", "admin token")
	fs.StringVar(&opts.bearerToken, "bearer-token", "", "bearer token")
	fs.StringVar(&opts.planningMode, "planning-mode", "", "planning mode")
	fs.IntVar(&opts.maxSteps, "max-steps", 0, "max agent steps")
	fs.DurationVar(&opts.timeout, "timeout", 60*time.Second, "request timeout")
	fs.IntVar(&opts.agentTimeoutMs, "agent-timeout-ms", 30000, "wait timeout passed to session-agent wait")
	fs.Int64Var(&opts.afterSeq, "after-seq", 0, "starting event sequence for session-agent events")
	fs.IntVar(&opts.limit, "limit", 20, "result limit for session-agent events")
	fs.IntVar(&opts.waitMs, "wait-ms", 0, "long-poll wait milliseconds for session-agent events")
	fs.BoolVar(&opts.stream, "stream", false, "use streaming mode")
	fs.BoolVar(&opts.forkContext, "fork-context", false, "copy parent session history when spawning a child agent")
	fs.BoolVar(&opts.interrupt, "interrupt", false, "interrupt a busy child agent before sending new input")
	fs.BoolVar(&opts.enableRouting, "enable-routing", true, "enable skill routing")
	fs.BoolVar(&opts.executePlannedSubagents, "execute-planned-subagents", false, "auto execute planned subagents")
	fs.BoolVar(&opts.allowWritePlannedSubagents, "allow-write-planned-subagents", false, "allow planned writer subagents")

	if err := fs.Parse(args); err != nil {
		return demoOptions{}, err
	}

	switch strings.ToLower(strings.TrimSpace(opts.mode)) {
	case "", "chat":
		if strings.TrimSpace(opts.message) == "" {
			return demoOptions{}, fmt.Errorf("message is required")
		}
	case "session-agent":
		switch strings.ToLower(strings.TrimSpace(opts.agentAction)) {
		case "spawn":
		case "status", "close", "resume":
			if strings.TrimSpace(opts.parentSessionID) == "" {
				return demoOptions{}, fmt.Errorf("parent-session-id is required")
			}
			if strings.TrimSpace(opts.agentID) == "" {
				return demoOptions{}, fmt.Errorf("agent-id is required")
			}
		case "events":
			if strings.TrimSpace(opts.parentSessionID) == "" {
				return demoOptions{}, fmt.Errorf("parent-session-id is required")
			}
		case "input":
			if strings.TrimSpace(opts.parentSessionID) == "" {
				return demoOptions{}, fmt.Errorf("parent-session-id is required")
			}
			if strings.TrimSpace(opts.agentID) == "" {
				return demoOptions{}, fmt.Errorf("agent-id is required")
			}
			if strings.TrimSpace(opts.message) == "" {
				return demoOptions{}, fmt.Errorf("message is required for session-agent input")
			}
		case "wait":
			if strings.TrimSpace(opts.parentSessionID) == "" {
				return demoOptions{}, fmt.Errorf("parent-session-id is required")
			}
		default:
			return demoOptions{}, fmt.Errorf("unsupported session-agent action: %s", opts.agentAction)
		}
	default:
		return demoOptions{}, fmt.Errorf("unsupported mode: %s", opts.mode)
	}
	return opts, nil
}

func runSessionAgentDemo(ctx context.Context, client *skillsapi.Client, opts demoOptions, stdout io.Writer) error {
	switch strings.ToLower(strings.TrimSpace(opts.agentAction)) {
	case "spawn":
		return runSessionAgentSpawnDemo(ctx, client, opts, stdout)
	case "status":
		resp, err := client.GetSessionAgentStatus(ctx, opts.parentSessionID, opts.agentID)
		if err != nil {
			return err
		}
		return printLines(stdout, summarizeSessionAgentStatus(opts.parentSessionID, &resp.Agent))
	case "input":
		req := skillsapi.SendSessionAgentInputRequest{
			Message: opts.message,
		}
		if opts.interrupt {
			req.Interrupt = &opts.interrupt
		}
		resp, err := client.SendSessionAgentInput(ctx, opts.parentSessionID, opts.agentID, req)
		if err != nil {
			return err
		}
		return printLines(stdout, summarizeSessionAgentStatus(opts.parentSessionID, &resp.Agent))
	case "wait":
		req := skillsapi.WaitSessionAgentsRequest{
			TimeoutMs: opts.agentTimeoutMs,
		}
		if strings.TrimSpace(opts.agentID) != "" {
			req.ID = strings.TrimSpace(opts.agentID)
		}
		if ids := parseCSVList(opts.agentIDsCSV); len(ids) > 0 {
			req.IDs = ids
		}
		resp, err := client.WaitSessionAgents(ctx, opts.parentSessionID, req)
		if err != nil {
			return err
		}
		return printLines(stdout, summarizeSessionAgentWaitResult(opts.parentSessionID, &resp.Result))
	case "events":
		params := skillsapi.ListSessionAgentEventsParams{
			AfterSeq: opts.afterSeq,
			Limit:    opts.limit,
			WaitMs:   opts.waitMs,
		}
		var (
			resp *skillsapi.ListSessionAgentEventsResponse
			err  error
		)
		if strings.TrimSpace(opts.agentID) == "" {
			resp, err = client.ListSessionAgentMailboxEvents(ctx, opts.parentSessionID, params)
		} else {
			resp, err = client.ListSessionAgentEvents(ctx, opts.parentSessionID, opts.agentID, params)
		}
		if err != nil {
			return err
		}
		return printLines(stdout, summarizeSessionAgentEvents(opts.parentSessionID, &resp.Result))
	case "close":
		resp, err := client.CloseSessionAgent(ctx, opts.parentSessionID, opts.agentID)
		if err != nil {
			return err
		}
		return printLines(stdout, summarizeSessionAgentStatus(opts.parentSessionID, &resp.Agent))
	case "resume":
		resp, err := client.ResumeSessionAgent(ctx, opts.parentSessionID, opts.agentID)
		if err != nil {
			return err
		}
		return printLines(stdout, summarizeSessionAgentStatus(opts.parentSessionID, &resp.Agent))
	default:
		return fmt.Errorf("unsupported session-agent action: %s", opts.agentAction)
	}
}

func runSessionAgentSpawnDemo(ctx context.Context, client *skillsapi.Client, opts demoOptions, stdout io.Writer) error {
	parentSessionID := strings.TrimSpace(opts.parentSessionID)
	autoCreatedParent := false
	if parentSessionID == "" {
		created, err := client.CreateSession(ctx, skillsapi.CreateSessionRequest{
			UserID: opts.userID,
			Title:  "skillsapi-demo parent",
		})
		if err != nil {
			return err
		}
		parentSessionID = created.Session.ID
		autoCreatedParent = true
	}

	req := skillsapi.SpawnSessionAgentRequest{
		Message:   opts.message,
		AgentType: opts.agentType,
		Model:     opts.agentModel,
	}
	if opts.forkContext {
		req.ForkContext = &opts.forkContext
	}

	resp, err := client.SpawnSessionAgent(ctx, parentSessionID, req)
	if err != nil {
		return err
	}

	lines := make([]string, 0, 8)
	if autoCreatedParent {
		lines = append(lines, "created_parent_session="+parentSessionID)
	}
	lines = append(lines, summarizeSessionAgentStatus(parentSessionID, &resp.Agent)...)
	return printLines(stdout, lines)
}

func runNonStreamDemo(ctx context.Context, client *skillsapi.Client, req skillsapi.AgentChatRequest, stdout io.Writer) error {
	resp, err := client.AgentChat(ctx, req)
	if err != nil {
		return err
	}
	decoded, err := resp.DecodeResult()
	if err != nil {
		return err
	}
	if decoded == nil {
		return fmt.Errorf("empty result")
	}

	lines := make([]string, 0, 16)
	if resp.SessionID != "" || resp.AgentID != "" {
		lines = append(lines, fmt.Sprintf("session=%s agent=%s status=%s", resp.SessionID, resp.AgentID, resp.Status))
	}
	lines = append(lines, summarizeResult(decoded)...)
	_, err = fmt.Fprintln(stdout, strings.Join(lines, "\n"))
	return err
}

func runStreamDemo(ctx context.Context, client *skillsapi.Client, req skillsapi.AgentChatRequest, stdout, stderr io.Writer) error {
	stream, err := client.AgentChatStream(ctx, req)
	if err != nil {
		return err
	}
	defer stream.Close()

	printer := streamDemoPrinter{stdout: stdout, stderr: stderr}
	return stream.Consume(skillsapi.StreamHandlers{
		OnEvent: func(decoded *skillsapi.DecodedStreamEvent) error {
			return printer.handleEvent(decoded)
		},
		OnMeta: func(meta *skillsapi.StreamMetaPayload) error {
			return printer.printLine(fmt.Sprintf("[meta] source=%s kind=%s status=%s", meta.Source, meta.Kind, meta.Status))
		},
		OnPlanning: func(planning *skillsapi.StreamPlanningPayload) error {
			return printer.printLine(fmt.Sprintf("[planning] mode=%s steps=%d tasks=%d attempted=%t", planning.Mode, planning.StepCount, planning.SubagentTaskCount, planning.Attempted))
		},
		OnOrchestration: func(orchestration *skillsapi.StreamOrchestrationPayload) error {
			return printer.printLine(fmt.Sprintf("[orchestration] source=%s route_matched=%t planning_attempted=%t", orchestration.Source, orchestration.RouteMatched, orchestration.PlanningAttempted))
		},
		OnResult: func(result *skillsapi.StreamResultPayload) error {
			if err := printer.ensureTextNewline(); err != nil {
				return err
			}
			return printer.printLine("[result]\n" + strings.Join(summarizeResult(&result.AgentChatResult), "\n"))
		},
		OnDone: func(done *skillsapi.StreamDonePayload) error {
			if err := printer.ensureTextNewline(); err != nil {
				return err
			}
			return printer.printLine(fmt.Sprintf("[done] status=%s session=%s", done.Status, done.SessionID))
		},
		OnError: func(errPayload *skillsapi.StreamErrorPayload) error {
			return fmt.Errorf("stream error from %s: %s", errPayload.Source, errPayload.Message)
		},
	})
}

type streamDemoPrinter struct {
	stdout    io.Writer
	stderr    io.Writer
	wroteText bool
}

func (p *streamDemoPrinter) handleEvent(decoded *skillsapi.DecodedStreamEvent) error {
	if decoded == nil || decoded.Chunk == nil {
		return nil
	}

	switch decoded.Event {
	case "chunk":
		if decoded.Chunk.Type == "text" {
			if _, err := fmt.Fprint(p.stdout, decoded.Chunk.Content); err != nil {
				return err
			}
			p.wroteText = true
		}
	case "reasoning":
		reasoning, err := decoded.Chunk.DecodeReasoning()
		if err != nil {
			return err
		}
		if reasoning != nil {
			return p.printLine(fmt.Sprintf("[reasoning] %s", strings.TrimSpace(reasoning.Content)))
		}
	case "tool_start", "tool_call", "tool_end":
		tool, err := decoded.Chunk.DecodeTool()
		if err != nil {
			return err
		}
		if tool == nil {
			return nil
		}
		parts := []string{
			fmt.Sprintf("[%s] %s", decoded.Event, tool.Name),
		}
		if args := compactJSON(tool.Args); args != "" {
			parts = append(parts, "args="+args)
		}
		if phase := decoded.Chunk.MetadataString("phase"); phase != "" {
			parts = append(parts, "phase="+phase)
		}
		if tool.Content != "" {
			parts = append(parts, "content="+strings.TrimSpace(tool.Content))
		}
		return p.printLine(strings.Join(parts, " "))
	}
	return nil
}

func (p *streamDemoPrinter) ensureTextNewline() error {
	if !p.wroteText {
		return nil
	}
	p.wroteText = false
	_, err := fmt.Fprintln(p.stdout)
	return err
}

func (p *streamDemoPrinter) printLine(line string) error {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	_, err := fmt.Fprintln(p.stderr, line)
	return err
}

func summarizeResult(result *skillsapi.AgentChatResult) []string {
	if result == nil {
		return []string{"result=<nil>"}
	}

	lines := []string{
		fmt.Sprintf("kind=%s source=%s success=%t", result.Kind, result.Source, result.Success),
	}
	if result.Skill != "" {
		lines = append(lines, "skill="+result.Skill)
	}
	if result.Model != "" {
		lines = append(lines, "model="+result.Model)
	}
	if output := strings.TrimSpace(result.Output); output != "" {
		lines = append(lines, "output="+output)
	}
	if result.Reasoning != "" {
		lines = append(lines, "reasoning="+strings.TrimSpace(result.Reasoning))
	}

	if usage, err := result.DecodeUsage(); err == nil && usage != nil {
		lines = append(lines, fmt.Sprintf("usage prompt=%d completion=%d total=%d", usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens))
	}
	if duration, err := result.DecodeDuration(); err == nil && duration != nil {
		lines = append(lines, fmt.Sprintf("duration elapsed=%s start=%s end=%s", duration.Elapsed(), duration.Start.Format(time.RFC3339), duration.End.Format(time.RFC3339)))
	}
	if state, err := result.DecodeState(); err == nil && state != nil {
		lines = append(lines, fmt.Sprintf("state step=%d running=%t errors=%d", state.CurrentStep, state.Running, len(state.Errors)))
	}
	if orchestration, err := result.DecodeOrchestration(); err == nil && orchestration != nil {
		lines = append(lines, fmt.Sprintf("orchestration route_attempted=%t route_matched=%t planning_attempted=%t subagent_tasks=%d", orchestration.RouteAttempted, orchestration.RouteMatched, orchestration.PlanningAttempted, orchestration.SubagentTaskCount))
	}
	if planning, err := result.DecodePlanning(); err == nil && planning != nil {
		lines = append(lines, fmt.Sprintf("planning mode=%s steps=%d tasks=%d requested=%t eligible=%t attempted=%t", planning.Mode, planning.StepCount, planning.SubagentTaskCount, planning.SubagentExecutionRequested, planning.SubagentExecutionEligible, planning.SubagentExecutionAttempted))
	}
	if subagents, err := result.DecodeSubagentSummary(); err == nil && subagents != nil {
		lines = append(lines, fmt.Sprintf("subagents count=%d successful=%d failed=%d patches=%d roles=%s", subagents.Count, subagents.Successful, subagents.Failed, subagents.PatchCount, strings.Join(subagents.Roles, ",")))
	}
	if toolCalls, err := result.DecodeToolCalls(); err == nil && len(toolCalls) > 0 {
		names := make([]string, 0, len(toolCalls))
		for _, call := range toolCalls {
			names = append(names, call.Name)
		}
		lines = append(lines, "tool_calls="+strings.Join(names, ","))
	}
	if finishReason := result.MetadataString("finish_reason"); finishReason != "" {
		lines = append(lines, "metadata.finish_reason="+finishReason)
	}
	if cached, ok := result.MetadataBool("cached"); ok {
		lines = append(lines, fmt.Sprintf("metadata.cached=%t", cached))
	}

	return lines
}

func compactJSON(value interface{}) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func printLines(stdout io.Writer, lines []string) error {
	_, err := fmt.Fprintln(stdout, strings.Join(lines, "\n"))
	return err
}

func summarizeSessionAgentStatus(parentSessionID string, agent *skillsapi.SessionAgent) []string {
	if agent == nil {
		return []string{"agent=<nil>"}
	}

	sessionID := strings.TrimSpace(agent.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(agent.ID)
	}
	resolvedParent := strings.TrimSpace(agent.ParentSessionID)
	if resolvedParent == "" {
		resolvedParent = strings.TrimSpace(parentSessionID)
	}

	lines := []string{
		fmt.Sprintf("parent_session=%s", resolvedParent),
		fmt.Sprintf("agent_session=%s status=%s exists=%t", sessionID, agent.Status, agent.Exists),
	}
	if agent.AgentType != "" {
		lines = append(lines, "agent_type="+agent.AgentType)
	}
	if agent.Created {
		lines = append(lines, "created=true")
	}
	if agent.Queued {
		lines = append(lines, "queued=true")
	}
	if agent.SessionState != "" {
		lines = append(lines, "session_state="+agent.SessionState)
	}
	if agent.MessageCount > 0 {
		lines = append(lines, fmt.Sprintf("message_count=%d", agent.MessageCount))
	}
	if agent.CurrentTurnID != "" {
		lines = append(lines, "current_turn_id="+agent.CurrentTurnID)
	}
	if agent.PendingToolName != "" {
		lines = append(lines, "pending_tool="+agent.PendingToolName)
	}
	if agent.PendingToolCallID != "" {
		lines = append(lines, "pending_tool_call_id="+agent.PendingToolCallID)
	}
	if agent.LastMessageRole != "" {
		lines = append(lines, "last_message_role="+agent.LastMessageRole)
	}
	if preview := strings.TrimSpace(agent.LastMessagePreview); preview != "" {
		lines = append(lines, "last_message_preview="+preview)
	}
	if output := strings.TrimSpace(agent.Output); output != "" {
		lines = append(lines, "output="+output)
	}
	if errText := strings.TrimSpace(agent.Error); errText != "" {
		lines = append(lines, "error="+errText)
	}
	if agent.PendingApproval {
		lines = append(lines, "pending_approval=true")
	}
	if agent.PendingQuestion {
		lines = append(lines, "pending_question=true")
	}
	if agent.TimedOut {
		lines = append(lines, "timed_out=true")
	}
	return lines
}

func summarizeSessionAgentWaitResult(parentSessionID string, result *skillsapi.SessionAgentWaitResult) []string {
	if result == nil {
		return []string{"wait_result=<nil>"}
	}

	lines := []string{
		fmt.Sprintf("parent_session=%s", strings.TrimSpace(parentSessionID)),
		fmt.Sprintf("matched_id=%s matched_session_id=%s ready=%d pending=%d latest_seq=%d timed_out=%t", result.MatchedID, result.MatchedSessionID, result.ReadyCount, result.PendingCount, result.LatestSeq, result.TimedOut),
	}
	if result.Event != nil {
		lines = append(lines, formatSessionAgentEvent("mailbox_event", *result.Event))
	}
	if result.Agent != nil {
		lines = append(lines, "matched_agent_status="+result.Agent.Status)
		if sessionID := strings.TrimSpace(result.Agent.SessionID); sessionID != "" {
			lines = append(lines, "matched_agent_session="+sessionID)
		}
		if output := strings.TrimSpace(result.Agent.Output); output != "" {
			lines = append(lines, "matched_agent_output="+output)
		}
	}
	if len(result.Agents) > 0 {
		parts := make([]string, 0, len(result.Agents))
		for _, agent := range result.Agents {
			sessionID := strings.TrimSpace(agent.SessionID)
			if sessionID == "" {
				sessionID = strings.TrimSpace(agent.ID)
			}
			parts = append(parts, sessionID+":"+agent.Status)
		}
		lines = append(lines, "agents="+strings.Join(parts, ","))
	}
	return lines
}

func summarizeSessionAgentEvents(parentSessionID string, result *skillsapi.SessionAgentEventsResult) []string {
	if result == nil {
		return []string{"events_result=<nil>"}
	}

	lines := []string{
		fmt.Sprintf("parent_session=%s", strings.TrimSpace(parentSessionID)),
		fmt.Sprintf("agent_session=%s count=%d latest_seq=%d timed_out=%t", result.SessionID, result.Count, result.LatestSeq, result.TimedOut),
	}
	for _, event := range result.Events {
		lines = append(lines, formatSessionAgentEvent("event", event))
	}
	return lines
}

func formatSessionAgentEvent(prefix string, event skillsapi.SessionAgentEvent) string {
	parts := []string{
		fmt.Sprintf("%s seq=%d", strings.TrimSpace(prefix), event.Seq),
		"type=" + event.Type,
	}
	if event.SessionID != "" {
		parts = append(parts, "session="+event.SessionID)
	}
	if !event.Timestamp.IsZero() {
		parts = append(parts, "time="+event.Timestamp.Format(time.RFC3339))
	}
	if event.AgentName != "" {
		parts = append(parts, "agent="+event.AgentName)
	}
	if event.ToolName != "" {
		parts = append(parts, "tool="+event.ToolName)
	}
	if payload := compactJSON(event.Payload); payload != "" && payload != "null" {
		parts = append(parts, "payload="+payload)
	}
	return strings.Join(parts, " ")
}

func parseCSVList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		items = append(items, part)
	}
	return items
}
