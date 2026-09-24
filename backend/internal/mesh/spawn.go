package mesh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ============================================================================
// mesh/spawn —— 新窗口打开（架构 §5.7 / §8.2，实施方案 S9，偏差 D3）
//
// 一次 spawn 的完整链路（与 §8.2 的流程图一一对应）：
//
//  1. 读 nodes/：该会话已有 live 节点 → reused（返回它的 URL，不碰任何进程）；
//  2. 否则抢 leases/spawn-<sid>.lock（单飞）；抢不到 → 再读一次档案 → reused，
//     仍没有则等对方的进程起来（同一个 wait 预算）；
//  3. 抢到 → 锁内二次检查 → 解析工作区/端口/令牌 → detach 启动
//     `aicli resume <sid> --pprof --web-host 127.0.0.1 [--web-port <port>]
//     --web-token <tok>`（cwd = 会话工作区，stdout/stderr → 日志文件）；
//  4. 轮询 nodes/ 直到新节点 live（或 wait 超时）→ started / not_running；
//  5. 进程启动失败 → failed（附日志尾部 20 行）。
//
// 状态集合固定四值（§5.7），**不返回 starting**：wait_ms 内同步等待。
// 令牌只出现在返回值与子进程命令行里：不写绑定、不进 journal（Journal 已注册
// secrets 兜底脱敏）、不进日志尾部（回传前再脱敏一次，M7）。
// ============================================================================

// Spawn statuses (architecture §5.7). The set is closed: `starting` is
// deliberately absent — wait_ms absorbs the wait, see §8.2.
const (
	SpawnStatusReused     = "reused"
	SpawnStatusStarted    = "started"
	SpawnStatusNotRunning = "not_running"
	SpawnStatusFailed     = "failed"
)

// Spawn codes reported by the HTTP layer (§5.9 reason vocabulary).
const (
	SpawnCodeNotAllowed = "mesh_spawn_not_allowed"
	SpawnCodeTimeout    = "mesh_spawn_timeout"
	SpawnCodeFailed     = "mesh_spawn_failed"
	// SpawnCodeBinUnavailable is the "no usable aicli binary" failure: nothing
	// was found, or AICLI_BIN points somewhere unusable (see
	// ResolveSpawnExecutable — a broken override never falls back silently).
	SpawnCodeBinUnavailable   = "mesh_spawn_bin_unavailable"
	SpawnCodeWorkspaceMissing = "mesh_workspace_missing"
	SpawnCodeMeshDisabled     = "mesh_disabled"
)

// Spawn defaults (§5.7 / §5.9).
const (
	// DefaultSpawnWaitMS is the documented wait_ms default: the request is
	// synchronous inside this budget.
	DefaultSpawnWaitMS = 8000
	// DefaultSpawnPollMS is the readiness poll interval. Small enough that a
	// normal start (≈0.5s) is not padded, large enough to stay off the disk.
	DefaultSpawnPollMS = 120
	// SpawnLogTailLines is how many log lines a failure hands back (§8.2).
	SpawnLogTailLines = 20
	// SpawnTokenBytes is the entropy of the token handed to the child (hex).
	SpawnTokenBytes = 16
	// spawnLogReadLimit bounds how much of a launch log is read for the tail.
	spawnLogReadLimit = 64 << 10
	// spawnLogPrefix / spawnLogSuffix name the per-session launch log:
	// <aicli home>/logs/mesh-spawn-<sid>.log (§5.7).
	spawnLogPrefix = "mesh-spawn-"
	spawnLogSuffix = ".log"
	// SpawnLogFilePerm is the log file mode (0600: it may hold prompts).
	SpawnLogFilePerm = 0o600
)

// SpawnRequest is the §5.7 request body.
type SpawnRequest struct {
	SessionID string
	// Port is the preferred loopback port; 0 means "use the session binding,
	// then a random free port" (§13 Q3).
	Port int
	// WaitMS overrides the readiness budget (0 means DefaultSpawnWaitMS).
	WaitMS int
	// Origin is the caller label for the audit trail ("web" / "cli").
	Origin string
	// Takeover skips the reuse short-circuit and asks the child to reclaim the
	// session lease explicitly (architecture §4.4, `aicli-mesh open --takeover`).
	// The old node is never killed: it marks itself orphaned on its next
	// heartbeat and keeps running.
	Takeover bool `json:"takeover,omitempty"`
}

// SpawnLaunchSpec is one detached launch. The caller (tests, CLI, HTTP layer)
// can inject its own launcher through SpawnOptions.Launch.
type SpawnLaunchSpec struct {
	// Executable is the aicli binary to run.
	Executable string
	// Args is the argument vector without the program name.
	Args []string
	// Dir is the child's working directory ("" = inherit).
	Dir string
	// LogPath receives the child's stdout/stderr.
	LogPath string
	// Env is the child environment (nil = inherit ours verbatim).
	Env []string
}

// SpawnResult is the §5.7 response body plus diagnostics the CLI prints.
type SpawnResult struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	// Code is the §5.9 reason code (mesh_spawn_timeout / mesh_spawn_failed /
	// mesh_spawn_bin_unavailable / mesh_workspace_missing / mesh_disabled).
	// Status alone is not enough for callers: the frontend switches on the
	// code, never on the prose Reason.
	Code      string `json:"code,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Port      int    `json:"port,omitempty"`
	// URL is the §7.3 window URL, token inlined: only this field carries the
	// token原文 (M7: it is handed straight to the browser).
	URL string `json:"url,omitempty"`
	// Reason is a human-readable explanation for not_running / failed.
	Reason string `json:"reason,omitempty"`
	// LogTail is the last lines of the launch log (failed / not_running).
	LogTail []string `json:"log_tail,omitempty"`
	// Lease reports what the single-flight lock did: acquired / held /
	// degraded / unreadable / reused.
	Lease     string `json:"lease,omitempty"`
	Origin    string `json:"origin,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// SpawnOptions tunes Spawn. The zero value is usable: paths are resolved from
// the environment, the launcher is the real detached launch and the readiness
// wait is DefaultSpawnWaitMS.
type SpawnOptions struct {
	// Paths overrides the resolved mesh root.
	Paths Paths
	// Now overrides the clock.
	Now func() time.Time
	// SelfNodeID / PID identify the lease owner.
	SelfNodeID string
	PID        int
	// Executable overrides the aicli binary verbatim (tests, embedding
	// callers). When it is empty the binary is resolved by
	// ResolveSpawnExecutable: AICLI_BIN, else self, else sibling `aicli`, else
	// PATH.
	Executable string
	// WorkspaceFor resolves the working directory of a session. When it is nil
	// (or reports nothing) the session binding's last workspace is used.
	WorkspaceFor func(sessionID string) (string, bool)
	// Launch overrides the detached launcher (tests).
	Launch func(SpawnLaunchSpec) (int, error)
	// LogDir overrides the launch log directory.
	LogDir string
	// Wait overrides the readiness budget (0 means DefaultSpawnWaitMS).
	Wait time.Duration
	// FireAndForget reports `started` right after a successful launch instead
	// of waiting for the node record (`aicli-mesh open --no-wait`).
	FireAndForget bool
	// PollInterval overrides DefaultSpawnPollMS.
	PollInterval time.Duration
	// Token overrides the generated write token (tests).
	Token func() string
	// Journal records the audit trail (nil = no journaling).
	Journal *Journal
	// Context aborts the readiness wait (HTTP client disconnect).
	Context context.Context
	// HeartbeatTTL overrides the freshness window used for liveness.
	HeartbeatTTL time.Duration
}

// Spawn implements §5.7: reuse the live node of a session or start one.
func Spawn(req SpawnRequest, opts SpawnOptions) (result SpawnResult) {
	started := time.Now()
	paths := opts.paths()
	now := opts.now()
	sessionID := strings.TrimSpace(req.SessionID)
	result = SpawnResult{
		SchemaVersion: SchemaVersion,
		Status:        SpawnStatusFailed,
		SessionID:     sessionID,
		Origin:        strings.TrimSpace(req.Origin),
	}
	defer func() { result.ElapsedMS = time.Since(started).Milliseconds() }()

	if _, err := SanitizeKey(sessionID); err != nil {
		result.Reason = "invalid session_id"
		return result
	}
	if !paths.Enabled() {
		// Fail-closed: without a mesh root there is no node record to watch and
		// no URL to hand back, so "started" could not be verified (§4.7).
		result.Code = SpawnCodeMeshDisabled
		result.Reason = "mesh root unavailable (set AICLI_MESH_DIR / AICLI_HOME)"
		return result
	}

	// §4.4 显式接管：不复用旧节点（复用等于继续让它服务），直接拉起新进程；
	// 旧节点由租约易主发现，不会被杀。
	if !req.Takeover {
		if node, ok := liveNodeForSession(paths, sessionID, now, opts.heartbeatTTL()); ok {
			result.Status = SpawnStatusReused
			result.Lease = "reused"
			fillSpawnResultFromNode(&result, node, sessionID)
			opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
			return result
		}
	}

	owner := opts.owner()
	outcome := Acquire(paths, LeasePurposeSpawn, sessionID, owner, AcquireOptions{
		Now: now,
		TTL: DefaultLeaseTTL,
	})
	result.Lease = outcome.Reason
	if outcome.Acquired {
		defer Release(paths, LeasePurposeSpawn, sessionID, owner)
	}

	// 锁内二次检查（§5.7）：两个请求几乎同时到达时，后到的那个不再拉进程。
	// 接管请求例外：它就是要多起一个进程来顶掉旧节点。
	if !req.Takeover {
		if node, ok := liveNodeForSession(paths, sessionID, opts.now(), opts.heartbeatTTL()); ok {
			result.Status = SpawnStatusReused
			fillSpawnResultFromNode(&result, node, sessionID)
			opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
			return result
		}
	}
	// 接管：记下正被顶替的旧节点。等待新节点就绪时必须把它排除掉，否则会把
	// 旧窗口的 URL 当成结果返回——用户点开的正是那个要被替换的窗口。
	previousNodeID := ""
	if req.Takeover {
		if node, ok := liveNodeForSession(paths, sessionID, opts.now(), opts.heartbeatTTL()); ok {
			previousNodeID = node.NodeID
		}
	}

	if !outcome.Acquired {
		// 别人正在拉起：等它把档案写出来（同一个 wait 预算），出现即 reused。
		if node, ok := opts.waitForLiveNode(paths, sessionID); ok {
			result.Status = SpawnStatusReused
			fillSpawnResultFromNode(&result, node, sessionID)
			opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
			return result
		}
		result.Status = SpawnStatusNotRunning
		result.Code = SpawnCodeTimeout
		result.Reason = fmt.Sprintf("another process is starting session %s (spawn lease %s); retry in a moment",
			sessionID, outcome.Reason)
		opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
		return result
	}

	workspace, ok := opts.workspaceFor(sessionID)
	if !ok {
		result.Status = SpawnStatusFailed
		result.Code = SpawnCodeWorkspaceMissing
		result.Reason = fmt.Sprintf("cannot resolve the workspace of session %s", sessionID)
		opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
		return result
	}
	if workspace != "" {
		if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
			// 风险 R8：目录被删/移走时给出可读原因，而不是让子进程静默失败。
			result.Status = SpawnStatusFailed
			result.Code = SpawnCodeWorkspaceMissing
			result.Reason = fmt.Sprintf("workspace directory not found: %s", workspace)
			opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
			return result
		}
	}
	executable, execErr := opts.executable()
	if execErr != nil {
		// A broken AICLI_BIN fails loudly: silently falling through to another
		// candidate would launch a different build than the operator asked for.
		result.Status = SpawnStatusFailed
		result.Code = SpawnCodeBinUnavailable
		result.Reason = execErr.Error()
		opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
		return result
	}
	if executable == "" {
		result.Status = SpawnStatusFailed
		result.Code = SpawnCodeBinUnavailable
		result.Reason = "cannot locate the aicli executable (set AICLI_BIN or run from a full install)"
		opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
		return result
	}

	port := resolveSpawnPort(paths, sessionID, req.Port, req.Takeover)
	token := opts.token()
	logPath := opts.logPath(sessionID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		result.Status = SpawnStatusFailed
		result.Code = SpawnCodeFailed
		result.Reason = fmt.Sprintf("create log dir %s: %v", filepath.Dir(logPath), err)
		opts.appendSpawnAudit(JournalSpawnCompleted, result, "")
		return result
	}
	spec := SpawnLaunchSpec{
		Executable: executable,
		Args:       spawnArgs(sessionID, port, token),
		Dir:        workspace,
		LogPath:    logPath,
		Env:        spawnEnvForChild(opts.owner().NodeID, req.Takeover),
	}
	opts.appendSpawnAudit(JournalSpawnRequested, result, workspace)

	pid, err := opts.launch()(spec)
	if err != nil {
		result.Status = SpawnStatusFailed
		result.Code = SpawnCodeFailed
		result.Reason = fmt.Sprintf("launch %s: %v", executable, err)
		result.LogTail = redactSpawnTail(readSpawnLogTail(logPath, SpawnLogTailLines), token)
		opts.appendSpawnAudit(JournalSpawnCompleted, result, workspace)
		return result
	}
	result.PID = pid
	result.Port = port

	if opts.FireAndForget {
		result.Status = SpawnStatusStarted
		result.Port = port
		result.URL = spawnURLForEndpoint(port, sessionID, token)
		result.Reason = fmt.Sprintf("started pid %d without waiting for readiness (--no-wait); retry `aicli-mesh open %s` if the URL is not up yet",
			pid, sessionID)
		opts.appendSpawnAudit(JournalSpawnCompleted, result, workspace)
		return result
	}

	node, ok := opts.waitForLiveNodeExcluding(paths, sessionID, previousNodeID)
	if !ok {
		result.Status = SpawnStatusNotRunning
		result.Code = SpawnCodeTimeout
		result.Reason = fmt.Sprintf("no live node for session %s after %s (pid %d may still be starting)",
			sessionID, opts.waitBudget(), pid)
		result.LogTail = redactSpawnTail(readSpawnLogTail(logPath, SpawnLogTailLines), token)
		opts.appendSpawnAudit(JournalSpawnCompleted, result, workspace)
		return result
	}
	result.Status = SpawnStatusStarted
	fillSpawnResultFromNode(&result, node, sessionID)
	if result.PID == 0 {
		result.PID = node.PID
	}
	opts.appendSpawnAudit(JournalSpawnCompleted, result, workspace)
	return result
}

// spawnArgs is the §5.7 command line. --web-port is only added for a concrete
// port: `--web-port 0` is rejected by the flag parser ("must be between 1 and
// 65535"), and omitting it leaves the child free to reuse the session binding's
// sticky port (S3), which resolveSpawnPort already preferred anyway.
func spawnArgs(sessionID string, port int, token string) []string {
	args := []string{
		"resume", sessionID,
		"--pprof",
		"--web-host", "127.0.0.1",
	}
	if port >= 1 && port <= 65535 {
		args = append(args, "--web-port", fmt.Sprintf("%d", port))
	}
	if strings.TrimSpace(token) != "" {
		args = append(args, "--web-token", token)
	}
	return args
}

// NewSpawnToken generates the write token handed to the spawned node (§9.1).
// It returns "" when the system RNG fails: the child then generates its own
// token and the caller reads it back from the node record.
func NewSpawnToken() string {
	buf := make([]byte, SpawnTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// liveNodeForSession returns the live node currently serving sessionID.
//
// "Live" is the same rule as everywhere else in the mesh (heartbeat fresh and
// the owner process alive), and it is evaluated through BuildView so spawn,
// `ls` and the HTTP layer never disagree about who owns a session (§7.5).
func liveNodeForSession(paths Paths, sessionID string, now time.Time, ttl time.Duration) (NodeView, bool) {
	return liveNodeForSessionExcluding(paths, sessionID, now, ttl, "")
}

// liveNodeForSessionExcluding is liveNodeForSession minus one node id. A
// takeover spawn passes the node it is replacing: the old node is still live
// until its next heartbeat, and handing its URL back would defeat the takeover.
func liveNodeForSessionExcluding(paths Paths, sessionID string, now time.Time, ttl time.Duration, excludeNodeID string) (NodeView, bool) {
	if !paths.Enabled() || strings.TrimSpace(sessionID) == "" {
		return NodeView{}, false
	}
	view := BuildView(paths, ViewOptions{Now: now, HeartbeatTTL: ttl})
	for _, node := range view.Nodes {
		if node.State != NodeStateLive {
			continue
		}
		if excludeNodeID != "" && node.NodeID == excludeNodeID {
			continue
		}
		if sessionIDOf(node) != sessionID {
			continue
		}
		return node, true
	}
	return NodeView{}, false
}

// fillSpawnResultFromNode fills the node-derived half of a result: the node id,
// the port and the §7.3 URL. The token is read from the record (the child may
// have ignored ours) and never logged.
func fillSpawnResultFromNode(result *SpawnResult, node NodeView, sessionID string) {
	if result == nil {
		return
	}
	result.NodeID = node.NodeID
	if node.PID != 0 {
		result.PID = node.PID
	}
	if node.Endpoint != nil && node.Endpoint.Port > 0 {
		result.Port = node.Endpoint.Port
	}
	token, _ := recordToken(node)
	result.URL = spawnNodeURL(node, sessionID, token)
}

// spawnNodeURL builds the §7.3 window URL of a live node:
// `http://host:port/web?token=<tok>&session=<sid>`.
func spawnNodeURL(node NodeView, sessionID, token string) string {
	if node.Endpoint == nil {
		return ""
	}
	base := strings.TrimSpace(node.Endpoint.BaseURL)
	if base == "" && node.Endpoint.Port > 0 {
		base = fmt.Sprintf("http://%s:%d", fallbackHost(node.Endpoint.Host), node.Endpoint.Port)
	}
	if base == "" {
		return ""
	}
	return spawnURLForEndpointPort(base, node.Endpoint.Port, sessionID, token)
}

// spawnURLForEndpoint builds a URL for a port we picked ourselves (the
// --no-wait path, where the node record does not exist yet).
func spawnURLForEndpoint(port int, sessionID, token string) string {
	if port < 1 || port > 65535 {
		return ""
	}
	return spawnURLForEndpointPort("", port, sessionID, token)
}

func spawnURLForEndpointPort(base string, port int, sessionID, token string) string {
	if base == "" {
		base = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	target := joinURLPath(base, "/web")
	var query []string
	if strings.TrimSpace(token) != "" {
		query = append(query, "token="+queryEscape(token))
	}
	if strings.TrimSpace(sessionID) != "" {
		query = append(query, "session="+queryEscape(sessionID))
	}
	if len(query) == 0 {
		return target
	}
	return target + "?" + strings.Join(query, "&")
}

// resolveSpawnPort picks the loopback port: explicit request > session binding
// preference > 0 (random free port, the child's own default). A takeover spawn
// ignores the binding preference: the node being taken over is still listening
// on that port (architecture §4.4), so the new node must pick a free one.
func resolveSpawnPort(paths Paths, sessionID string, requested int, takeover bool) int {
	if requested >= 1 && requested <= 65535 {
		return requested
	}
	if takeover {
		return 0
	}
	if binding, ok := LoadBinding(paths, sessionID); ok && binding.Preferred != nil {
		if port := binding.Preferred.Port; port >= 1 && port <= 65535 {
			return port
		}
	}
	return 0
}

// readSpawnLogTail returns the last n lines of a launch log. A missing or
// unreadable log is not an error: the caller still gets the status and reason.
func readSpawnLogTail(path string, n int) []string {
	path = strings.TrimSpace(path)
	if path == "" || n <= 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	offset := int64(0)
	if info.Size() > spawnLogReadLimit {
		offset = info.Size() - spawnLogReadLimit
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(buf, offset); err != nil && len(buf) > 0 {
		// A short read still yields usable text (the file may be rotating).
		_ = err
	}
	text := strings.ReplaceAll(string(buf), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	// A truncated head may cut a line in half; drop it when we skipped bytes.
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimRight(line, "\r"); trimmed != "" || len(out) > 0 {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// redactSpawnTail scrubs the write token out of a log tail before it leaves the
// process (M7: the child may echo its own command line).
func redactSpawnTail(lines []string, token string) []string {
	token = strings.TrimSpace(token)
	if token == "" || len(lines) == 0 {
		return lines
	}
	hint := HintToken(token)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, strings.ReplaceAll(line, token, hint))
	}
	return out
}

// defaultSpawnLogDir is `<aicli home>/logs` (AICLI_HOME honoured, matching the
// mesh root rules of paths.go).
func defaultSpawnLogDir() string {
	if home := strings.TrimSpace(os.Getenv(EnvHome)); home != "" {
		if expanded := ExpandUserPath(home); expanded != "" {
			return filepath.Join(expanded, "logs")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".aicli", "logs")
	}
	return ""
}

// SpawnExecutableEnv is the environment override for the aicli binary a spawn
// launches. It is honoured by the CLI, by the Web host and by every node they
// start (the child inherits our environment verbatim).
const SpawnExecutableEnv = "AICLI_BIN"

// Spawn executable resolution sources (`doctor` spawn-executable check): the
// rule that picked the binary, so a multi-install machine can tell which
// aicli gets spawned.
const (
	SpawnExecutableSourceEnv     = "AICLI_BIN"
	SpawnExecutableSourceSelf    = "self"
	SpawnExecutableSourceSibling = "sibling"
	SpawnExecutableSourcePath    = "PATH"
)

// Test seams: os.Executable and PATH belong to the running process, so the
// resolution rules can only be exercised by substituting them.
var (
	spawnExecutablePath = os.Executable
	spawnStatFile       = os.Stat
	spawnLookPath       = exec.LookPath
)

// SpawnExecutableResolution is the aicli binary a spawn would launch plus the
// rule that picked it. Path is "" when nothing was found.
type SpawnExecutableResolution struct {
	Path   string
	Source string
}

// ResolveSpawnExecutable reports which aicli binary `aicli-mesh open` and
// POST /web/api/mesh/spawn would launch, and which rule picked it. `doctor`
// prints it: with several aicli installs on one machine, "which binary gets
// spawned" is the first thing to check.
//
// Order: AICLI_BIN, else self (only when the current executable *is* aicli),
// else the sibling aicli<ext> (the aicli-mesh CLI case), else PATH.
//
// AICLI_BIN is authoritative: when it is set but unusable (missing, a
// directory, unreadable) resolution fails instead of quietly falling through.
// The override means "launch this exact binary", so a silent fallback would
// run a different build than the operator asked for.
//
// The name match is exact (basename `aicli`, case-insensitive, extension
// stripped) and the sibling lookup is hard-coded to aicli<ext>: a renamed
// binary (aicli-2x.exe) is neither "self" nor the sibling it looks for, so a
// rename needs AICLI_BIN (prefer an absolute path) or a co-located aicli.exe.
func ResolveSpawnExecutable() (SpawnExecutableResolution, error) {
	if override := strings.TrimSpace(os.Getenv(SpawnExecutableEnv)); override != "" {
		info, err := spawnStatFile(override)
		if err != nil || info.IsDir() {
			return SpawnExecutableResolution{}, fmt.Errorf(
				"%s=%s is not a usable aicli binary (%s); fix or unset it — the override never falls back to self/sibling/PATH",
				SpawnExecutableEnv, override, spawnStatReason(err, info))
		}
		return SpawnExecutableResolution{Path: override, Source: SpawnExecutableSourceEnv}, nil
	}
	if exe, err := spawnExecutablePath(); err == nil && strings.TrimSpace(exe) != "" {
		if spawnSelfIsAICLI(exe) {
			return SpawnExecutableResolution{Path: exe, Source: SpawnExecutableSourceSelf}, nil
		}
		sibling := filepath.Join(filepath.Dir(exe), "aicli"+filepath.Ext(exe))
		if info, statErr := spawnStatFile(sibling); statErr == nil && !info.IsDir() {
			return SpawnExecutableResolution{Path: sibling, Source: SpawnExecutableSourceSibling}, nil
		}
	}
	if path, err := spawnLookPath("aicli"); err == nil && strings.TrimSpace(path) != "" {
		return SpawnExecutableResolution{Path: path, Source: SpawnExecutableSourcePath}, nil
	}
	return SpawnExecutableResolution{}, nil
}

// spawnSelfIsAICLI reports whether exe is an aicli binary itself: basename
// `aicli`, case-insensitive, extension stripped.
func spawnSelfIsAICLI(exe string) bool {
	return strings.ToLower(strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))) == "aicli"
}

// spawnStatReason turns a stat result into a short reason for the AICLI_BIN
// error text (the operator needs to know *why* the override is unusable).
func spawnStatReason(err error, info os.FileInfo) string {
	switch {
	case err != nil:
		return err.Error()
	case info != nil && info.IsDir():
		return "is a directory"
	default:
		return "unusable"
	}
}

// ---------------------------------------------------------------------------
// SpawnOptions accessors (zero value = production defaults)
// ---------------------------------------------------------------------------

func (o SpawnOptions) paths() Paths {
	// An explicitly labelled layout is honoured even when it is fail-closed
	// (Source=unset): the caller said "this is the mesh root I resolved", and
	// ResolvePaths() itself returns exactly that shape when there is no home.
	if o.Paths.Enabled() || o.Paths.Source != "" {
		return o.Paths
	}
	return ResolvePaths()
}

func (o SpawnOptions) now() time.Time {
	if o.Now != nil {
		if at := o.Now(); !at.IsZero() {
			return at
		}
	}
	return NowUTC()
}

func (o SpawnOptions) heartbeatTTL() time.Duration {
	if o.HeartbeatTTL > 0 {
		return o.HeartbeatTTL
	}
	return DefaultHeartbeatTTL
}

func (o SpawnOptions) waitBudget() time.Duration {
	if o.Wait > 0 {
		return o.Wait
	}
	return time.Duration(DefaultSpawnWaitMS) * time.Millisecond
}

func (o SpawnOptions) pollInterval() time.Duration {
	if o.PollInterval > 0 {
		return o.PollInterval
	}
	return time.Duration(DefaultSpawnPollMS) * time.Millisecond
}

func (o SpawnOptions) context() context.Context {
	if o.Context != nil {
		return o.Context
	}
	return context.Background()
}

func (o SpawnOptions) owner() LeaseOwner {
	pid := o.PID
	if pid <= 0 {
		pid = os.Getpid()
	}
	return LeaseOwner{NodeID: strings.TrimSpace(o.SelfNodeID), PID: pid}
}

// executable returns the binary to launch: an explicit override verbatim
// (tests, embedding callers), otherwise the resolved aicli binary. A broken
// AICLI_BIN is an error, never a silent fallback.
func (o SpawnOptions) executable() (string, error) {
	if exe := strings.TrimSpace(o.Executable); exe != "" {
		return exe, nil
	}
	resolution, err := ResolveSpawnExecutable()
	if err != nil {
		return "", err
	}
	return resolution.Path, nil
}

func (o SpawnOptions) launch() func(SpawnLaunchSpec) (int, error) {
	if o.Launch != nil {
		return o.Launch
	}
	return launchDetached
}

func (o SpawnOptions) token() string {
	if o.Token != nil {
		return strings.TrimSpace(o.Token())
	}
	return NewSpawnToken()
}

func (o SpawnOptions) logPath(sessionID string) string {
	dir := strings.TrimSpace(o.LogDir)
	if dir == "" {
		dir = defaultSpawnLogDir()
	}
	if dir == "" {
		return ""
	}
	key, err := SanitizeKey(sessionID)
	if err != nil {
		key = "unknown"
	}
	return filepath.Join(dir, spawnLogPrefix+key+spawnLogSuffix)
}

// workspaceFor resolves the child's working directory: the caller's resolver
// first (the HTTP layer knows the session manager), then the session binding's
// last workspace. The bool reports "we have an answer at all" — an empty
// workspace with ok=true means "let the child resolve it from its own cwd".
func (o SpawnOptions) workspaceFor(sessionID string) (string, bool) {
	if o.WorkspaceFor != nil {
		if dir, ok := o.WorkspaceFor(sessionID); ok {
			return strings.TrimSpace(dir), true
		}
	}
	if binding, ok := LoadBinding(o.paths(), sessionID); ok {
		if dir := strings.TrimSpace(binding.LastWorkspacePath); dir != "" {
			return dir, true
		}
	}
	// No evidence at all: inherit the caller's cwd rather than refusing (the
	// child resolves the session workspace itself, as `aicli resume` always did).
	return "", true
}

// waitForLiveNode polls the node records until the session has a live node,
// the budget lapses or the context is cancelled.
func (o SpawnOptions) waitForLiveNode(paths Paths, sessionID string) (NodeView, bool) {
	return o.waitForLiveNodeExcluding(paths, sessionID, "")
}

// waitForLiveNodeExcluding is waitForLiveNode minus one node id: a takeover
// spawn must not mistake the node it is replacing for its own child.
func (o SpawnOptions) waitForLiveNodeExcluding(paths Paths, sessionID, excludeNodeID string) (NodeView, bool) {
	deadline := time.Now().Add(o.waitBudget())
	interval := o.pollInterval()
	for {
		if node, ok := liveNodeForSessionExcluding(paths, sessionID, o.now(), o.heartbeatTTL(), excludeNodeID); ok {
			return node, true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return NodeView{}, false
		}
		if remaining < interval {
			interval = remaining
		}
		timer := time.NewTimer(interval)
		select {
		case <-o.context().Done():
			timer.Stop()
			return NodeView{}, false
		case <-timer.C:
		}
	}
}

// appendSpawnAudit records one spawn entry. Details never carry the token; the
// journal scrubs registered secrets as a second line of defence (M7).
func (o SpawnOptions) appendSpawnAudit(kind string, result SpawnResult, workspace string) {
	journal := o.Journal
	if journal == nil {
		return
	}
	detail := map[string]any{
		"status":  result.Status,
		"session": result.SessionID,
		"origin":  result.Origin,
	}
	if result.NodeID != "" {
		detail["node_id"] = result.NodeID
	}
	if result.PID != 0 {
		detail["pid"] = result.PID
	}
	if result.Port != 0 {
		detail["port"] = result.Port
	}
	if result.Lease != "" {
		detail["lease"] = result.Lease
	}
	if workspace != "" {
		detail["workspace"] = workspace
	}
	if kind == JournalSpawnCompleted {
		detail["elapsed_ms"] = result.ElapsedMS
	}
	journal.Append(kind, result.SessionID, detail)
}
