// Package toolctl implements the direct CLI surface from the tool-layer
// abstraction design. It reuses the existing toolkit implementations through
// the toolcore compatibility adapter; it does not add a second tool registry.
package toolctl

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

const (
	ExitOK          = 0
	ExitUsage       = 2
	ExitPolicy      = 3
	ExitExecution   = 4
	ExitTimeout     = 5
	ExitUnavailable = 6
)

// CLI is the testable direct surface. Writers may be replaced in tests.
type CLI struct {
	Stdout io.Writer
	Stderr io.Writer
}

// Run dispatches one toolctl invocation.
func (c *CLI) Run(args []string) int {
	if c == nil {
		return ExitExecution
	}
	if c.Stdout == nil {
		c.Stdout = io.Discard
	}
	if c.Stderr == nil {
		c.Stderr = io.Discard
	}
	if len(args) == 0 {
		c.usage()
		return ExitUsage
	}
	switch args[0] {
	case "list":
		return c.runList(args[1:])
	case "describe":
		return c.runDescribe(args[1:])
	case "run":
		return c.runTool(args[1:])
	case "serve":
		return c.runServe(args[1:])
	case "help", "-h", "--help":
		c.usage()
		return ExitOK
	default:
		fmt.Fprintf(c.Stderr, "unknown command %q\n", args[0])
		c.usage()
		return ExitUsage
	}
}

func (c *CLI) usage() {
	fmt.Fprint(c.Stdout, `toolctl - direct tool surface

Usage:
  toolctl list [--profile auto|readonly|standard|ops] [--json]
  toolctl describe <tool> [--json]
  toolctl run <tool> --args-json '<json>' [--json] [--allow-write] [--allow-command] [--yes]
  toolctl serve --mcp stdio|http [--listen 127.0.0.1:18765]

Common flags:
  --workspace <dir>       workspace root (default: current directory)
  --allowed-root <dir>    additional admitted root (repeatable)
  --tools a,b,c           explicit tool allowlist
  --permission-mode <m>   default|accept_edits|plan|dont_ask
  --yes                   accept ask decisions (never bypasses deny/sandbox)
  --allow-write           expose write/edit/apply_patch (implies accept_edits)
  --allow-command         expose the shell tool
`)
}

type commonOptions struct {
	workspace  string
	allowed    stringList
	profile    string
	tools      string
	exclude    string
	json       bool
	yes        bool
	allowWrite bool
	allowCmd   bool
	mode       string
	argsJSON   string
	args       stringList
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func bindCommon(fs *flag.FlagSet, opts *commonOptions) {
	fs.StringVar(&opts.workspace, "workspace", "", "workspace root")
	fs.Var(&opts.allowed, "allowed-root", "additional admitted root")
	fs.StringVar(&opts.profile, "profile", "auto", "profile: auto|readonly|standard|ops")
	fs.StringVar(&opts.tools, "tools", "", "comma-separated tool allowlist")
	fs.StringVar(&opts.exclude, "exclude-tools", "", "comma-separated tool denylist")
	fs.BoolVar(&opts.json, "json", false, "emit JSON")
	fs.BoolVar(&opts.yes, "yes", false, "accept ask decisions")
	fs.BoolVar(&opts.allowWrite, "allow-write", false, "allow write tools")
	fs.BoolVar(&opts.allowCmd, "allow-command", false, "allow the shell tool")
	fs.StringVar(&opts.mode, "permission-mode", "", "permission mode: default|accept_edits|plan|dont_ask (default: accept_edits with --allow-write, else default)")
}

func (c *CLI) runList(args []string) int {
	var opts commonOptions
	fs := flag.NewFlagSet("toolctl list", flag.ContinueOnError)
	fs.SetOutput(c.Stderr)
	bindCommon(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	surface, err := c.buildSurface(&opts)
	if err != nil {
		fmt.Fprintln(c.Stderr, err)
		return ExitUsage
	}
	defs := surface.describeAll()
	if opts.json {
		return writeJSON(c.Stdout, defs, c.Stderr)
	}
	for _, def := range defs {
		fmt.Fprintf(c.Stdout, "%-24s %s\n", def.Name, def.Description)
	}
	return ExitOK
}

func (c *CLI) runDescribe(args []string) int {
	args = moveLeadingPositionalLast(args)
	var opts commonOptions
	fs := flag.NewFlagSet("toolctl describe", flag.ContinueOnError)
	fs.SetOutput(c.Stderr)
	bindCommon(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(c.Stderr, "describe requires exactly one tool name")
		return ExitUsage
	}
	surface, err := c.buildSurface(&opts)
	if err != nil {
		fmt.Fprintln(c.Stderr, err)
		return ExitUsage
	}
	def, ok := surface.describe(fs.Arg(0))
	if !ok {
		fmt.Fprintf(c.Stderr, "tool %q not found\n", fs.Arg(0))
		return ExitPolicy
	}
	if opts.json {
		return writeJSON(c.Stdout, def, c.Stderr)
	}
	encoded, _ := json.MarshalIndent(def, "", "  ")
	fmt.Fprintln(c.Stdout, string(encoded))
	return ExitOK
}

func (c *CLI) runTool(args []string) int {
	args = moveLeadingPositionalLast(args)
	var opts commonOptions
	fs := flag.NewFlagSet("toolctl run", flag.ContinueOnError)
	fs.SetOutput(c.Stderr)
	bindCommon(fs, &opts)
	fs.StringVar(&opts.argsJSON, "args-json", "", "arguments as a JSON object")
	fs.Var(&opts.args, "arg", "argument key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(c.Stderr, "run requires exactly one tool name")
		return ExitUsage
	}
	surface, err := c.buildSurface(&opts)
	if err != nil {
		fmt.Fprintln(c.Stderr, err)
		return ExitUsage
	}
	name := fs.Arg(0)
	handler, ok := surface.registry.Resolve(name)
	if !ok {
		fmt.Fprintf(c.Stderr, "tool %q not found\n", name)
		return ExitPolicy
	}
	canonical := surface.canonicalName(name)
	arguments, err := parseArguments(opts.argsJSON, opts.args)
	if err != nil {
		fmt.Fprintln(c.Stderr, err)
		return ExitUsage
	}
	decision, err := surface.authorize(canonical, handler.Definition(), arguments, &opts)
	if err != nil {
		fmt.Fprintln(c.Stderr, err)
		return ExitPolicy
	}
	arguments, err = policy.ApplyPatchedArgs(arguments, decision.PatchedArgs)
	if err != nil {
		fmt.Fprintf(c.Stderr, "%s: %v\n", toolcore.ErrInvalidArgument, err)
		return ExitUsage
	}
	ctx := toolctx.WithWorkspaceRoot(context.Background(), surface.workspace)
	result, execErr := handler.Execute(ctx, &toolcore.Call{
		Args: arguments,
		Request: toolcore.RequestInfo{
			Surface:    "cli",
			SessionID:  "toolctl",
			ToolCallID: "cli-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		},
	})
	if execErr != nil {
		result = toolcore.Error(toolcore.ErrInternal, "retry the call", execErr)
	}
	output := resultView{Tool: canonical}
	if result != nil {
		output.OK = result.Success && execErr == nil
		output.Output = result.Content
		output.MIMEType = result.MIMEType
		output.Metadata = result.Metadata
		output.ErrorCode = toolcore.NormalizeErrorCode(result.ErrorCode)
		output.NextAction = result.NextAction
		output.Retriable = result.Retriable
		if len(result.Data) > 0 {
			output.Output = base64.StdEncoding.EncodeToString(result.Data)
			if output.MIMEType == "" {
				output.MIMEType = "application/octet-stream"
			}
		}
		if output.ErrorCode == "" {
			output.ErrorCode = metadataErrorCode(result.Metadata)
		}
		output.ArtifactRef = metadataString(result.Metadata, "artifact_ref")
	}
	if opts.json {
		if code := writeJSON(c.Stdout, output, c.Stderr); code != ExitOK {
			return code
		}
	} else if output.Output != "" {
		fmt.Fprintln(c.Stdout, output.Output)
	}
	if !output.OK {
		if output.NextAction != "" {
			fmt.Fprintf(c.Stderr, "%s: %s\nnext: %s\n", output.ErrorCode, output.Output, output.NextAction)
		}
		return exitCodeFor(output.ErrorCode)
	}
	return ExitOK
}

type resultView struct {
	Tool     string                 `json:"tool"`
	OK       bool                   `json:"ok"`
	Output   string                 `json:"output,omitempty"`
	MIMEType string                 `json:"mime_type,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	// The documented CLI envelope always carries these fields so a consumer can
	// read them without distinguishing success from failure shapes.
	ErrorCode   string `json:"error_code"`
	NextAction  string `json:"next_action"`
	Retriable   bool   `json:"retriable"`
	ArtifactRef string `json:"artifact_ref"`
}

func writeJSON(out io.Writer, value any, errOut io.Writer) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(errOut, "encode result: %v\n", err)
		return ExitExecution
	}
	_, _ = out.Write(encoded)
	_, _ = out.Write([]byte("\n"))
	return ExitOK
}

func parseArguments(rawJSON string, pairs stringList) (map[string]interface{}, error) {
	arguments := map[string]interface{}{}
	if strings.TrimSpace(rawJSON) != "" {
		if err := json.Unmarshal([]byte(rawJSON), &arguments); err != nil {
			return nil, fmt.Errorf("parse --args-json: %w", err)
		}
	}
	for _, pair := range pairs {
		key, raw, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid --arg %q (want key=value)", pair)
		}
		arguments[strings.TrimSpace(key)] = parseScalar(raw)
	}
	return arguments, nil
}

func parseScalar(raw string) interface{} {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var value interface{}
	if err := json.Unmarshal([]byte(trimmed), &value); err == nil {
		return value
	}
	return raw
}

func metadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func metadataErrorCode(metadata map[string]interface{}) string {
	return toolcore.NormalizeErrorCode(metadataString(metadata, "error_code"))
}

func exitCodeFor(code string) int {
	switch toolcore.NormalizeErrorCode(code) {
	case toolcore.ErrInvalidArgument, toolcore.ErrUnsupported:
		return ExitUsage
	case toolcore.ErrToolNotFound:
		return ExitPolicy
	case toolcore.ErrToolDenied, toolcore.ErrCapabilityDenied, toolcore.ErrPathDenied,
		toolcore.ErrCommandDenied, toolcore.ErrApprovalRequired, toolcore.ErrArtifactScopeDenied:
		return ExitPolicy
	case toolcore.ErrTimeout, toolcore.ErrCancelled:
		return ExitTimeout
	case toolcore.ErrRateLimited, toolcore.ErrRemoteUnavailable, toolcore.ErrProtocol, toolcore.ErrRemoteTooLarge:
		return ExitUnavailable
	default:
		return ExitExecution
	}
}

type toolSurface struct {
	workspace  string
	profile    string
	registry   *toolcore.Registry
	tools      map[string]toolkit.Tool
	visible    map[string]bool
	aliases    map[string]string
	sandbox    *executor.Sandbox
	allowWrite bool
	allowExec  bool
}

func (s *toolSurface) canonicalName(name string) string {
	name = strings.TrimSpace(name)
	if canonical, ok := s.aliases[name]; ok {
		return canonical
	}
	return name
}

// authorize runs the shared policy engine. The surface only narrows the tool
// set; it must not implement a second permission table (design §8.4).
func (s *toolSurface) authorize(name string, def toolcore.Definition, args map[string]interface{}, opts *commonOptions) (policy.Decision, error) {
	if !s.visible[name] {
		return policy.Decision{}, fmt.Errorf("%s: tool %q is not enabled by the current profile", toolcore.ErrToolNotFound, name)
	}
	modeName := strings.TrimSpace(opts.mode)
	if modeName == "" {
		// --allow-write is the explicit mutation opt-in for this surface. The
		// default engine mode asks for every write, which would make the flag
		// itself a no-op. Accept-edits keeps the opt-in meaningful while the
		// engine's hard gates (sensitive write, destructive shell) still apply.
		if opts.allowWrite {
			modeName = string(policy.ModeAcceptEdits)
		} else {
			modeName = string(policy.ModeDefault)
		}
	}
	mode, ok := policy.ParseMode(modeName)
	if !ok {
		return policy.Decision{}, fmt.Errorf("%s: unknown permission mode %q", toolcore.ErrInvalidArgument, modeName)
	}
	capabilities := make([]policy.Capability, 0, len(def.Capabilities))
	for _, capability := range def.Capabilities {
		switch capability {
		case toolcore.CapReadFS:
			capabilities = append(capabilities, policy.CapReadOnly)
		case toolcore.CapWriteFS:
			capabilities = append(capabilities, policy.CapWriteFS)
		case toolcore.CapExecShell:
			capabilities = append(capabilities, policy.CapExecShell)
		case toolcore.CapNetwork:
			capabilities = append(capabilities, policy.CapNetwork)
		}
	}
	engine := &policy.Engine{
		Mode:   mode,
		Policy: policy.NewToolExecutionPolicy(s.visibleNames(), false),
		AskHandler: cliApprovalHandler{
			accept: opts.yes,
			errOut: nil,
		},
	}
	engine.Policy.PathAnchorRoot = s.workspace
	engine.Policy.Sandbox = s.sandbox
	decision, err := engine.Evaluate(toolctx.WithWorkspaceRoot(context.Background(), s.workspace), policy.EvalRequest{
		ToolName:     name,
		Args:         args,
		Capabilities: capabilities,
		Mode:         mode,
		ToolInfo: &skill.ToolInfo{
			Name:        name,
			Description: def.Description,
			Metadata:    def.Metadata,
			Enabled:     true,
		},
	})
	if err != nil {
		return decision, err
	}
	if decision.Type == policy.DecisionDeny {
		code := toolcore.ErrToolDenied
		switch {
		case decision.Stage == policy.StagePolicy &&
			(strings.Contains(decision.Reason, "sandbox") || strings.Contains(decision.Reason, "path")):
			code = toolcore.ErrPathDenied
		}
		return decision, fmt.Errorf("%s: %s", code, decision.Reason)
	}
	return decision, nil
}

// visibleNames returns the model-visible tool allowlist for the engine.
func (s *toolSurface) visibleNames() []string {
	names := make([]string, 0, len(s.visible))
	for name, visible := range s.visible {
		if visible {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// cliApprovalHandler implements --yes as "approve ordinary asks". Hard asks
// (root/home removal, sensitive writes) stay denied: the CLI has no interactive
// confirmation channel, and silently approving them would bypass the safety
// boundary the engine deliberately keeps.
type cliApprovalHandler struct {
	accept bool
	errOut io.Writer
}

func (h cliApprovalHandler) RequestApproval(_ context.Context, req policy.ApprovalRequest) (policy.ApprovalResponse, error) {
	if req.HardAsk {
		return policy.ApprovalResponse{
			Allowed: false,
			Reason:  "hard_ask_requires_explicit_confirmation",
		}, nil
	}
	if !h.accept {
		return policy.ApprovalResponse{
			Allowed: false,
			Reason:  "approval_required; pass --yes to accept ordinary asks",
		}, nil
	}
	return policy.ApprovalResponse{Allowed: true, Reason: "cli --yes"}, nil
}

func (s *toolSurface) describe(name string) (toolDescription, bool) {
	name = s.canonicalName(name)
	if !s.visible[name] {
		return toolDescription{}, false
	}
	handler, ok := s.registry.Resolve(name)
	if !ok {
		return toolDescription{}, false
	}
	return describeHandler(handler), true
}

func (s *toolSurface) describeAll() []toolDescription {
	defs := s.registry.Definitions()
	out := make([]toolDescription, 0, len(defs))
	for _, def := range defs {
		if !s.visible[def.Name] {
			continue
		}
		out = append(out, toolDescription{
			Name:         def.Name,
			Aliases:      def.Aliases,
			Description:  def.Description,
			Version:      def.Version,
			Capabilities: capabilityStrings(def.Capabilities),
			DirectCall:   def.DirectCall,
			InputSchema:  def.InputSchema,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type toolDescription struct {
	Name         string          `json:"name"`
	Aliases      []string        `json:"aliases,omitempty"`
	Description  string          `json:"description"`
	Version      string          `json:"version"`
	Capabilities []string        `json:"capabilities,omitempty"`
	DirectCall   bool            `json:"direct_call"`
	InputSchema  json.RawMessage `json:"input_schema"`
}

func describeHandler(handler toolcore.Handler) toolDescription {
	def := handler.Definition()
	return toolDescription{
		Name:         def.Name,
		Aliases:      append([]string(nil), def.Aliases...),
		Description:  def.Description,
		Version:      def.Version,
		Capabilities: capabilityStrings(def.Capabilities),
		DirectCall:   def.DirectCall,
		InputSchema:  append(json.RawMessage(nil), def.InputSchema...),
	}
}

func capabilityStrings(capabilities []toolcore.Capability) []string {
	out := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		out = append(out, string(capability))
	}
	return out
}

type definitionOverride struct {
	inner toolcore.Handler
	def   toolcore.Definition
}

func (h *definitionOverride) Definition() toolcore.Definition { return h.def }
func (h *definitionOverride) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	return h.inner.Execute(ctx, call)
}

// moveLeadingPositionalLast lets `toolctl run view --json` parse the same way
// as `toolctl run --json view`. Go's flag package stops at the first positional
// argument, and the documented CLI form puts the tool first.
func moveLeadingPositionalLast(args []string) []string {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return args
	}
	out := append([]string(nil), args[1:]...)
	out = append(out, args[0])
	return out
}

func (c *CLI) buildSurface(opts *commonOptions) (*toolSurface, error) {
	if opts == nil {
		opts = &commonOptions{}
	}
	workspace := strings.TrimSpace(opts.workspace)
	if workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve current directory: %w", err)
		}
		workspace = cwd
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace %q is not accessible: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace %q is not a directory", abs)
	}
	workspace = filepath.Clean(abs)

	allowed := []string{workspace}
	for _, root := range opts.allowed {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve allowed root %q: %w", root, err)
		}
		allowed = append(allowed, filepath.Clean(rootAbs))
	}
	allowed = dedupeStrings(allowed)

	profile := strings.ToLower(strings.TrimSpace(opts.profile))
	if profile == "" {
		profile = "auto"
	}
	switch profile {
	case "auto", "readonly", "standard", "ops", "custom":
	default:
		return nil, fmt.Errorf("unknown profile %q (want auto|readonly|standard|ops|custom)", profile)
	}

	sandbox := executor.NewSandbox(&executor.SandboxConfig{
		Enabled:          true,
		AllowedPaths:     allowed,
		EnvWhitelist:     []string{"PATH", "HOME", "USER", "TMP", "TEMP", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "LANG", "LC_ALL"},
		MaxExecutionTime: 2 * time.Minute,
	})

	constructed := constructToolsFromCatalog(profile)
	for name, tool := range constructed {
		if setter, ok := tool.(interface{ SetSandbox(*executor.Sandbox) }); ok {
			setter.SetSandbox(sandbox)
		}
		if setter, ok := tool.(interface{ SetBasePath(string) }); ok {
			setter.SetBasePath(workspace)
		}
		if tool == nil {
			delete(constructed, name)
		}
	}

	inventory := map[string]toolcore.CoreTool{}
	aliases := map[string]string{}
	for _, item := range toolcore.V1CoreInventory() {
		if item.ControlPlane {
			continue
		}
		inventory[item.Name] = item
		for _, alias := range item.Aliases {
			aliases[alias] = item.Name
		}
	}

	allowWrite := opts.allowWrite
	allowExec := opts.allowCmd
	if profile == "readonly" {
		allowWrite = false
		allowExec = false
	}
	visible := map[string]bool{}
	for name := range constructed {
		if isWriteTool(name) && !allowWrite {
			continue
		}
		if name == "shell" && !allowExec {
			continue
		}
		// Local-only tools keep the historical MCP surface, but capability
		// families still need their explicit opt-in: file-writing helpers need
		// --allow-write, exec helpers need --allow-command.
		switch name {
		case "download", "openai_image_generate":
			if !allowWrite {
				continue
			}
		case "aicli_exec":
			if !allowExec {
				continue
			}
		}
		visible[name] = true
	}
	if list := parseCSV(opts.tools); len(list) > 0 {
		allowedNames := map[string]bool{}
		for _, name := range list {
			if canonical, ok := aliases[name]; ok {
				name = canonical
			}
			allowedNames[name] = true
		}
		for name := range visible {
			if !allowedNames[name] {
				delete(visible, name)
			}
		}
	}
	if list := parseCSV(opts.exclude); len(list) > 0 {
		for _, name := range list {
			if canonical, ok := aliases[name]; ok {
				name = canonical
			}
			delete(visible, name)
		}
	}

	registry := toolcore.NewRegistry()
	for name, tool := range constructed {
		handler, err := toolkit.NewToolcoreHandler(tool)
		if err != nil {
			return nil, err
		}
		def := handler.Definition()
		if item, ok := inventory[name]; ok {
			def.Aliases = append([]string(nil), item.Aliases...)
			def.Capabilities = append([]toolcore.Capability(nil), item.Capabilities...)
			def.DefaultOutputBudget = toolcoreoutput.ToolBudget(name)
		}
		if isWriteTool(name) {
			def.Capabilities = []toolcore.Capability{toolcore.CapWriteFS}
		}
		if name == "shell" {
			def.Capabilities = []toolcore.Capability{toolcore.CapExecShell}
		}
		def.DirectCall = true
		if err := registry.Register(&definitionOverride{inner: handler, def: def}); err != nil {
			return nil, err
		}
	}
	return &toolSurface{
		workspace:  workspace,
		profile:    profile,
		registry:   registry,
		tools:      constructed,
		visible:    visible,
		aliases:    aliases,
		sandbox:    sandbox,
		allowWrite: allowWrite,
		allowExec:  allowExec,
	}, nil
}

func isWriteTool(name string) bool {
	switch name {
	case "write", "edit", "append_write", "multiedit", "apply_patch":
		return true
	default:
		return false
	}
}

// constructToolsFromCatalog builds the toolkit instances for this surface from
// the single builtin construction catalog (internal/tools). No second table is
// allowed here: a tool added to the catalog becomes visible to every surface
// through this function, and a tool removed from it disappears everywhere.
//
// The default profile is the v1 read-only core. Network/image/stateful helpers
// are only constructed for the explicit ops/custom profiles, so `toolctl` does
// not silently widen the default surface.
func constructToolsFromCatalog(profile string) map[string]toolkit.Tool {
	constructed := map[string]toolkit.Tool{}
	for _, spec := range runtimetools.BuiltinToolkitCatalog() {
		// The catalog contains one row per registered instance, including the
		// compatibility aliases (bash, execute_shell_command, read, rg). The
		// toolcore registry already carries those as aliases of the canonical
		// tool, so constructing them again here would collide on registration.
		// Keep one instance per canonical identity.
		if _, exists := constructed[spec.Canonical]; exists {
			continue
		}
		if !profileIncludesTool(profile, spec.Canonical) {
			continue
		}
		if toolctlUnsupportedCatalogTool(spec.Canonical) {
			continue
		}
		if spec.Condition != nil && !spec.Condition(nil) {
			continue
		}
		tool := spec.New(nil)
		if tool == nil {
			continue
		}
		constructed[spec.Canonical] = tool
	}
	return constructed
}

// toolctlUnsupportedCatalogTool reports catalog rows that need runtime state
// this surface does not bind yet. artifact_read reads toolctx.ArtifactStore,
// which is session-scoped; toolctl has no artifact store, so exposing it would
// only produce a permanent "artifact store unavailable" failure.
func toolctlUnsupportedCatalogTool(name string) bool {
	return name == "artifact_read"
}

// profileIncludesTool answers which catalog tools belong to each surface
// profile. It is a visibility policy, never an authorization decision.
func profileIncludesTool(profile, name string) bool {
	switch profile {
	case "readonly":
		return isReadOnlyCoreTool(name)
	case "ops", "custom":
		return true
	default: // "auto" and "standard" keep the v1 core surface.
		return isCoreTool(name)
	}
}

// isCoreTool reports membership in the v1 core migration matrix.
func isCoreTool(name string) bool {
	for _, tool := range toolcore.V1CoreInventory() {
		if tool.Name != name {
			continue
		}
		// Local-only capabilities are not part of the default v1 core; they
		// require the explicit ops/custom profile.
		return !tool.LocalOnly
	}
	return false
}

// isReadOnlyCoreTool restricts the surface to non-mutating, non-exec core tools.
func isReadOnlyCoreTool(name string) bool {
	for _, tool := range toolcore.V1CoreInventory() {
		if tool.Name != name {
			continue
		}
		for _, capability := range tool.Capabilities {
			if capability == toolcore.CapWriteFS || capability == toolcore.CapExecShell {
				return false
			}
		}
		return true
	}
	return false
}

func parseCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.Clean(value)
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (c *CLI) runServe(args []string) int {
	var opts commonOptions
	fs := flag.NewFlagSet("toolctl serve", flag.ContinueOnError)
	fs.SetOutput(c.Stderr)
	bindCommon(fs, &opts)
	transportName := fs.String("mcp", "stdio", "MCP transport: stdio|http")
	listen := fs.String("listen", "127.0.0.1:18765", "HTTP listen address")
	tokenEnv := fs.String("token-env", "AICLI_REMOTE_TOKEN", "environment variable holding the bearer token")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	surface, err := c.buildSurface(&opts)
	if err != nil {
		fmt.Fprintln(c.Stderr, err)
		return ExitUsage
	}
	mcpServer := newSurfaceMCPServer(surface)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch strings.ToLower(strings.TrimSpace(*transportName)) {
	case "stdio":
		if err := mcpServer.Start(ctx); err != nil {
			fmt.Fprintf(c.Stderr, "serve stdio: %v\n", err)
			return ExitExecution
		}
		return ExitOK
	case "http":
		return c.serveSurfaceHTTP(ctx, surface, *listen, *tokenEnv, *tlsCert, *tlsKey)
	default:
		fmt.Fprintf(c.Stderr, "unsupported MCP transport %q (want stdio|http)\n", *transportName)
		return ExitUsage
	}
}

func bearerAuthMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		presented := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if header == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
