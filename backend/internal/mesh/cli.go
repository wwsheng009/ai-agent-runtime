// aicli-mesh CLI: the mesh's first consumer and operations entry point
// (architecture §7).
//
// Design notes that are easy to get wrong later:
//
//  1. The CLI is *not* a node: it never writes a record, a binding or a lease.
//     Everything here is read-only except `gc --apply`, which only deletes
//     files that are provably dead (never a live pid: rule R3).
//  2. It works with zero aicli processes running: all commands read files.
//  3. It reuses the same aggregation layer as the Web control plane
//     (`BuildView`), so `aicli-mesh ls --json` and
//     `GET /web/api/mesh/peers` cannot drift apart (§7.1).
//  4. Output contract (§7.3): human tables by default, `--json` for scripts,
//     stable exit codes 0..6.
//
// This file deliberately uses the standard library only (package invariant),
// which is why it carries a tiny interspersed-flag parser: the documented
// usage puts flags after positional arguments (`show <target> --json`), which
// the stdlib flag package cannot express.

package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Exit codes of the CLI contract (§7.3).
const (
	// ExitOK is success.
	ExitOK = 0
	// ExitUsage is a usage / argument error.
	ExitUsage = 1
	// ExitNotFound is a target that does not exist or cannot be resolved to
	// exactly one node / session (ambiguity is reported with its candidates).
	ExitNotFound = 2
	// ExitUnreachable is a target that exists but cannot be reached: no
	// loopback endpoint, or a failed liveness probe.
	ExitUnreachable = 3
	// ExitConflict is a busy or contested target (session claimed by another
	// live node, invoke busy).
	ExitConflict = 4
	// ExitFailure is a failed operation (local I/O error, target returned an
	// error, timeout) and is also what `doctor` returns when it finds problems.
	ExitFailure = 5
	// ExitRefused is a policy refusal (non-loopback, write without an explicit
	// allow, cross-workspace write under a restrictive switch).
	ExitRefused = 6
)

// CLIVersion is the fallback version string when the binary was built without
// the -X main.version injection.
const CLIVersion = "dev"

// CLI is the aicli-mesh command line. Stdout/Stderr default to os.Stdout /
// os.Stderr; Now and Paths are test seams.
type CLI struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
	// Now overrides the clock (zero value means NowUTC()).
	Now func() time.Time
	// Paths overrides the resolved mesh root (tests / embedded callers).
	Paths *Paths
	// Spawn overrides the spawn implementation of `open` (tests / embedded
	// callers; the zero value means the real mesh.Spawn). Everything else in
	// this CLI is read-only, so this is the only seam that can start a process.
	Spawn func(SpawnRequest, SpawnOptions) SpawnResult
}

// Run executes one command line (without the program name) and returns the
// process exit code.
func (c *CLI) Run(args []string) int {
	if len(args) == 0 {
		c.printUsage(c.errOut())
		return ExitUsage
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	switch command {
	case "ls", "ps":
		return c.runLs(rest)
	case "show":
		return c.runShow(rest)
	case "url":
		return c.runURL(rest)
	case "call":
		return c.runCall(rest)
	case "send":
		return c.runSend(rest)
	case "screen":
		return c.runScreen(rest)
	case "open":
		return c.runOpen(rest)
	case "new":
		return c.runNew(rest)
	case "stop":
		return c.runStop(rest)
	case "watch":
		return c.runWatch(rest)
	case "gc":
		return c.runGC(rest)
	case "doctor":
		return c.runDoctor(rest)
	case "version":
		return c.runVersion(rest)
	case "help", "-h", "--help":
		c.printUsage(c.out())
		return ExitOK
	default:
		fmt.Fprintf(c.errOut(), "未知子命令: %s\n\n", args[0])
		c.printUsage(c.errOut())
		return ExitUsage
	}
}

func (c *CLI) out() io.Writer {
	if c.Stdout == nil {
		return os.Stdout
	}
	return c.Stdout
}

func (c *CLI) errOut() io.Writer {
	if c.Stderr == nil {
		return os.Stderr
	}
	return c.Stderr
}

func (c *CLI) now() time.Time {
	if c.Now != nil {
		if at := c.Now(); !at.IsZero() {
			return at
		}
	}
	return NowUTC()
}

func (c *CLI) version() string {
	if strings.TrimSpace(c.Version) == "" {
		return CLIVersion
	}
	return c.Version
}

func (c *CLI) paths() Paths {
	if c.Paths != nil {
		return *c.Paths
	}
	return ResolvePaths()
}

func (c *CLI) spawn() func(SpawnRequest, SpawnOptions) SpawnResult {
	if c.Spawn != nil {
		return c.Spawn
	}
	return Spawn
}

func (c *CLI) printUsage(w io.Writer) {
	fmt.Fprint(w, `aicli-mesh —— aicli 节点网格工具（发现 / 运维）

用法:
  aicli-mesh ls [-a|--all] [--json] [--probe] [--live] [--workspace PATH]... [--sort age|session|workspace]
  aicli-mesh show <节点|会话> [--json] [--events N]
  aicli-mesh url <节点|会话> [--with-token] [--path PATH] [--json]
  aicli-mesh call <节点|会话> <op> [--args JSON] [--client-request-id ID] [--allow-write] [--timeout 130s] [--json]
  aicli-mesh send <节点|会话> <prompt> [--allow-write] [--timeout 130s] [--json]
  aicli-mesh screen <节点|会话> [--view tui] [--tail N] [--format json|text] [--json]
  aicli-mesh open <会话> [--port N] [--wait 8s] [--no-wait] [--takeover] [--bin PATH] [--json]
  aicli-mesh new [--workspace PATH] [--port N] [--wait 8s] [--no-wait] [--bin PATH] [--json]
  aicli-mesh stop <节点|会话> [--force] [--wait 30s] [--json]
  aicli-mesh watch [--since 10m] [--node ID] [--session ID] [--once] [--limit N] [--interval 500ms] [--json] [--no-color]
  aicli-mesh gc [--apply] [--stale-ttl 10m] [--keep-days 7] [--purge-legacy] [--prune-bindings] [--json]
  aicli-mesh doctor [--json]
  aicli-mesh version [--json]

目标解析: 节点 ID / 节点 ID 前缀 / 会话 ID / 会话 ID 前缀 / pid:<PID>。
歧义时列出候选并要求精确指定（不做「猜一个」）。

调用（call/send/screen）: op 白名单 node.info/status/screen/turn/sessions.list（只读）
  与 invoke/input/cancel/sessions.resume（写操作，**必须** --allow-write 显式允许）。
  调用方直接连目标的 loopback 端点，令牌取自目标档案（0600），不经过第三方。

拉起（open）: 复用该会话的活节点，或在其工作区拉起新进程，并打印 §7.3 窗口 URL
  （含令牌，交给浏览器自举）。与 POST /web/api/mesh/spawn 同一套实现；CLI 不是
  节点，单飞租约的 owner 记作 cli-<pid>。--no-wait 只报告已启动，不等就绪。
  --takeover 跳过复用并显式回收该会话的租约（§4.4）：旧节点继续运行，但会在
  下次心跳后把自己的档案标记为 orphaned（不会被杀；用 aicli-mesh show 查看）。
  --bin PATH 指定拉起的 aicli 二进制（必须存在；优先于 AICLI_BIN）。默认解析顺序：
  AICLI_BIN → 自身（仅当就叫 aicli）→ 同目录 aicli.exe → PATH；AICLI_BIN 指错时
  直接失败，绝不退回其它候选（改名部署见 docs/aicli/mesh-cli.md）。

新建（new）: 在一个工作区里新建一个会话——拉起 aicli chat（不带会话 ID，由子进程
  自己生成），等它的节点档案出现后打印新会话 ID 与窗口 URL。与 open 的分工：open
  是「让这个已有会话有窗口」（复用活节点、按会话单飞），new 是「开一个新会话」，
  因此从不复用，也不抢会话租约（此时还没有会话 ID 可抢）。工作区默认是当前目录
  （--workspace 指定，必须是已存在的目录）；--port/--wait/--bin/--no-wait 与 open
  同义，但 --no-wait 时会话 ID 还未知（子进程自己生成），稍后用 aicli-mesh ls 查看。

停止（stop）: 默认优雅——把 /exit 投给目标的 /web/api/input，让目标自己收尾
  （保存会话、注销档案、释放租约），并等进程消失；--force 才直接终止进程
  （不做收尾，残留档案由 gc 按「可证已死」回收）。停止是治理动作：**目标
  进程**必须显式开启 --mesh-allow-stop，否则一律 refused（CLI 无法绕过）。
  「目标已不在运行」按成功处理（幂等）；等不到进程消失时退出码 3。

观察（watch）: 直接 tail mesh/journal/*.ndjson，**不依赖任何节点存活**——进程全退
  之后仍可复盘（这是它与 /web/api/mesh/events 的分工）。默认先回放 --since 窗口
  （10m；0 表示全部），再实时尾随；--once 只回放（脚本/Agent 用）。--node/--session
  按节点/会话 ID（前缀即可）过滤。--json 在回放模式给
  {"schema_version":2,"events":[...],"counts":{...}}；实时模式改为逐行 NDJSON
  （流式，无法等收尾）。Ctrl-C 结束（退出码 0）。

退出码:
  0 成功            1 用法/参数错误      2 目标不存在或无法唯一确定
  3 目标不可达      4 冲突或忙碌         5 操作失败（doctor 发现问题时同码）
  6 被策略拒绝

默认不探活（毫秒级，纯读文件）；--probe 才发请求。

详情（show）: 单目标的可操作细节。档案里有写令牌时，输出直接给出令牌原文与
  /web?token=<令牌> 的打开地址（CLI 本机披露面；ls 与 HTTP 视图仍只给
  token_hint），--json 的 token / token_source / web_url 同源；纯 TUI 节点没有
  端点，只给令牌不给地址。输出含密钥，别贴到会被转发的地方。

列表（ls）: 默认只列**在线**（live）节点——日常问的是「现在谁在跑」；-a/--all 才列出
  全部档案（stale/stopped/unknown 一并可见，冲突也照常提示）。--live 是默认行为的
  显式写法（保留兼容，与 -a 互斥）。无论怎么过滤，counts 恒为全量普查口径（§5.4），
  过滤只裁剪 nodes[]。
`)
}

// ---------------------------------------------------------------------------
// Interspersed flag parser (stdlib flag cannot do "positional then flags")
// ---------------------------------------------------------------------------

// flagKind describes how one accepted flag consumes its argument.
type flagKind int

const (
	flagBool flagKind = iota
	flagValue
	flagMulti
)

// flagSpec is the accepted-flag table of one subcommand.
type flagSpec map[string]flagKind

// parsedArgs is the result of parsing: flags by name plus positional arguments.
type parsedArgs struct {
	flags map[string]string
	multi map[string][]string
	pos   []string
}

func parseArgs(args []string, spec flagSpec) (parsedArgs, error) {
	out := parsedArgs{flags: map[string]string{}, multi: map[string][]string{}}
	positionalOnly := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if positionalOnly || arg == "-" || !strings.HasPrefix(arg, "-") {
			out.pos = append(out.pos, arg)
			continue
		}
		if arg == "--" {
			positionalOnly = true
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		inline := ""
		hasInline := false
		if index := strings.Index(name, "="); index >= 0 {
			name, inline, hasInline = name[:index], name[index+1:], true
		}
		kind, ok := spec[name]
		if !ok {
			return out, fmt.Errorf("未知参数: %s", arg)
		}
		switch kind {
		case flagBool:
			value := "true"
			if hasInline {
				parsed, err := strconv.ParseBool(inline)
				if err != nil {
					return out, fmt.Errorf("参数 --%s 需要一个布尔值（收到 %q）", name, inline)
				}
				value = strconv.FormatBool(parsed)
			}
			out.flags[name] = value
		case flagValue, flagMulti:
			value := inline
			if !hasInline {
				if i+1 >= len(args) {
					return out, fmt.Errorf("参数 --%s 缺少取值", name)
				}
				i++
				value = args[i]
			}
			if kind == flagMulti {
				out.multi[name] = append(out.multi[name], value)
			} else {
				out.flags[name] = value
			}
		}
	}
	return out, nil
}

func (a parsedArgs) boolean(name string) bool {
	value, ok := a.flags[name]
	if !ok {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func (a parsedArgs) str(name, fallback string) string {
	if value, ok := a.flags[name]; ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func (a parsedArgs) list(name string) []string {
	return a.multi[name]
}

func (a parsedArgs) intValue(name string, fallback int) (int, error) {
	value, ok := a.flags[name]
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("参数 --%s 需要一个整数（收到 %q）", name, value)
	}
	return parsed, nil
}

func (a parsedArgs) durationValue(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := a.flags[name]
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("参数 --%s 需要一个时长（如 10m / 7d 不合法，请用 168h；收到 %q）", name, value)
	}
	return parsed, nil
}

// printJSON writes one JSON document followed by a newline.
func (c *CLI) printJSON(payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(c.out(), string(data))
	return err
}

// fail prints a CLI error and returns the matching exit code.
func (c *CLI) fail(code int, format string, args ...any) int {
	fmt.Fprintf(c.errOut(), format+"\n", args...)
	return code
}

// ---------------------------------------------------------------------------
// Target resolution (§7.2): node id / prefix / session id / prefix / pid:<PID>
// ---------------------------------------------------------------------------

// targetError carries the exit code that matches the resolution failure.
type targetError struct {
	code int
	msg  string
}

func (e *targetError) Error() string { return e.msg }

func newTargetError(code int, format string, args ...any) *targetError {
	return &targetError{code: code, msg: fmt.Sprintf(format, args...)}
}

// matchTargetNodes 是**所有** `<节点|会话>` 参数共用的解析口径（§3 目标解析：
// show / url / call / send / screen 同源）。规则按优先级求值，第一个非空结果胜出：
//  1. `pid:<PID>`           exact pid（正整数）
//  2. node id               exact（大小写不敏感）
//  3. node id prefix
//  4. session id            exact
//  5. session id prefix
//
// 规则按顺序求值、第一个有命中的规则胜出，所以精确 node id 永远不会输给别人的前缀。
// 命中多条时由调用方决定「精确优先 / 歧义报错」（口径见 pickTarget 与
// ResolveCallTarget）。badRef=true 表示引用本身语法非法（空串或 `pid:` 非正整数），
// 调用方据此给出各自的用法错误；未命中时 nodes 为空且 badRef=false。
func matchTargetNodes(view MeshView, ref string) (nodes []NodeView, rule string, badRef bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "", true
	}
	if strings.HasPrefix(strings.ToLower(ref), "pid:") {
		raw := strings.TrimSpace(ref[len("pid:"):])
		pid, err := strconv.Atoi(raw)
		if err != nil || pid <= 0 {
			return nil, "", true
		}
		matches := make([]NodeView, 0, 1)
		for _, node := range view.Nodes {
			if node.PID == pid {
				matches = append(matches, node)
			}
		}
		return matches, "pid", false
	}
	rules := []struct {
		rule  string
		match func(NodeView) bool
	}{
		{"node", func(node NodeView) bool { return strings.EqualFold(node.NodeID, ref) }},
		{"node-prefix", func(node NodeView) bool { return hasFoldPrefix(node.NodeID, ref) }},
		{"session", func(node NodeView) bool {
			return node.Session != nil && strings.EqualFold(node.Session.ID, ref)
		}},
		{"session-prefix", func(node NodeView) bool {
			return node.Session != nil && hasFoldPrefix(node.Session.ID, ref)
		}},
	}
	for _, r := range rules {
		matches := make([]NodeView, 0, 2)
		for _, node := range view.Nodes {
			if r.match(node) {
				matches = append(matches, node)
			}
		}
		if len(matches) > 0 {
			return matches, r.rule, false
		}
	}
	return nil, "", false
}

// resolveTarget maps a user-supplied reference onto exactly one node.
// Zero matches is exit 2 ("目标不存在"); several matches is exit 2 as well
// ("无法唯一确定") but lists the candidates — the CLI never guesses.
func resolveTarget(view MeshView, ref string) (NodeView, error) {
	ref = strings.TrimSpace(ref)
	matches, _, badRef := matchTargetNodes(view, ref)
	if badRef {
		if ref == "" {
			return NodeView{}, newTargetError(ExitUsage, "缺少目标：需要节点 ID、会话 ID 或 pid:<PID>")
		}
		return NodeView{}, newTargetError(ExitUsage, "pid: 目标需要正整数（收到 %q）", ref)
	}
	if len(matches) == 0 {
		return NodeView{}, newTargetError(ExitNotFound, "找不到目标 %q（本机网格 %d 个节点）", ref, len(view.Nodes))
	}
	return pickTarget(matches, ref)
}

func hasFoldPrefix(value, prefix string) bool {
	return prefix != "" && len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix)
}

func pickTarget(matches []NodeView, ref string) (NodeView, error) {
	switch len(matches) {
	case 0:
		return NodeView{}, newTargetError(ExitNotFound, "找不到目标 %q", ref)
	case 1:
		return matches[0], nil
	default:
		lines := make([]string, 0, len(matches))
		for _, node := range matches {
			lines = append(lines, "  "+targetLabel(node))
		}
		return NodeView{}, newTargetError(ExitNotFound,
			"目标 %q 匹配到 %d 个节点，无法唯一确定：\n%s\n请使用完整节点 ID 或 pid:<PID> 精确指定",
			ref, len(matches), strings.Join(lines, "\n"))
	}
}

func targetLabel(node NodeView) string {
	label := node.NodeID
	if node.PID > 0 {
		label += fmt.Sprintf(" (pid %d", node.PID)
		if node.Session != nil && node.Session.ID != "" {
			label += ", session " + node.Session.ID
		}
		label += ")"
	} else if node.Session != nil && node.Session.ID != "" {
		label += " (session " + node.Session.ID + ")"
	}
	if node.State != NodeStateLive {
		label += " [" + string(node.State) + "]"
	}
	return label
}

// ---------------------------------------------------------------------------
// ls / ps
// ---------------------------------------------------------------------------

func (c *CLI) runLs(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"json":      flagBool,
		"probe":     flagBool,
		"live":      flagBool,
		"all":       flagBool,
		"a":         flagBool,
		"workspace": flagMulti,
		"sort":      flagValue,
		"help":      flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh ls: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) > 0 {
		return c.fail(ExitUsage, "aicli-mesh ls: 不接受位置参数（收到 %q）", parsed.pos[0])
	}
	sortKey := strings.ToLower(parsed.str("sort", "age"))
	switch sortKey {
	case "age", "session", "workspace":
	default:
		return c.fail(ExitUsage, "aicli-mesh ls: --sort 只接受 age|session|workspace（收到 %q）", sortKey)
	}
	// 默认只看在线：`ls` 回答的是「现在有哪些节点在跑」。-a/--all 才列全量
	// （stale/stopped/unknown 也列出）。--live 是默认行为的显式写法（保留兼容），
	// 与 -a 同时出现属于自相矛盾，按用法错误处理而不是静默取一个。
	showAll := parsed.boolean("all") || parsed.boolean("a")
	if showAll && parsed.boolean("live") {
		return c.fail(ExitUsage, "aicli-mesh ls: -a/--all 与 --live 互斥（默认即只看在线；--all 列出全部状态）")
	}
	paths := c.paths()
	opts := ViewOptions{Now: c.now(), Probe: parsed.boolean("probe")}
	filter := ViewFilter{Scope: FilterScopeAll, State: FilterStateLive}
	filtered := true
	if showAll {
		filter.State = FilterStateAll
		filtered = false
	}
	if workspaces := parsed.list("workspace"); len(workspaces) > 0 {
		filter.Workspace = workspaces
		filtered = true
	}
	if filtered {
		opts.Filter = &filter
	}
	view := BuildView(paths, opts)
	sortListedNodes(view.Nodes, sortKey)
	if parsed.boolean("json") {
		if err := c.printJSON(view); err != nil {
			return c.fail(ExitFailure, "aicli-mesh ls: 输出 JSON 失败: %v", err)
		}
		return ExitOK
	}
	c.renderLsTable(view, sortKey, parsed.boolean("probe"))
	return ExitOK
}

// sortListedNodes applies --sort to the *listed* slice only. `age` is the
// BuildView order (live first, newest heartbeat first) and stays untouched.
func sortListedNodes(nodes []NodeView, key string) {
	switch key {
	case "session":
		sort.SliceStable(nodes, func(i, j int) bool {
			left, right := sessionIDOf(nodes[i]), sessionIDOf(nodes[j])
			if left != right {
				if left == "" {
					return false
				}
				if right == "" {
					return true
				}
				return left < right
			}
			return nodeOrderLess(nodes[i], nodes[j])
		})
	case "workspace":
		sort.SliceStable(nodes, func(i, j int) bool {
			left, right := workspacePathOf(nodes[i]), workspacePathOf(nodes[j])
			if !strings.EqualFold(left, right) {
				if left == "" {
					return false
				}
				if right == "" {
					return true
				}
				return strings.ToLower(left) < strings.ToLower(right)
			}
			return nodeOrderLess(nodes[i], nodes[j])
		})
	}
}

func nodeOrderLess(left, right NodeView) bool {
	if leftRank, rightRank := stateRank(left.State), stateRank(right.State); leftRank != rightRank {
		return leftRank < rightRank
	}
	return heartbeatOf(left).After(heartbeatOf(right))
}

func sessionIDOf(node NodeView) string {
	if node.Session == nil {
		return ""
	}
	return node.Session.ID
}

func (c *CLI) renderLsTable(view MeshView, sortKey string, probed bool) {
	headers := []string{"STATE", "NODE", "PID", "SESSION", "WORKSPACE", "ADDR", "AGE", "OWN"}
	if probed {
		headers = append(headers, "REACH")
	}
	rows := make([][]string, 0, len(view.Nodes))
	for _, node := range view.Nodes {
		row := []string{
			string(node.State),
			node.NodeID,
			pidCell(node.PID),
			cellOrDash(truncateCell(sessionIDOf(node), 32)),
			cellOrDash(truncateCell(workspacePathOf(node), 28)),
			endpointCell(node),
			formatAge(node.AgeSec),
			ownershipCell(node),
		}
		if probed {
			row = append(row, string(node.Reachability))
		}
		rows = append(rows, row)
	}
	fmt.Fprint(c.out(), renderTable(headers, rows))
	counts := view.Counts
	// 总数用全量普查（§5.4）：过滤只裁剪 nodes[]，不能假装网格更小。
	total := counts.Live + counts.Stale + counts.Stopped + counts.Unknown
	fmt.Fprintf(c.out(), "\n共 %d 个节点：live=%d stale=%d stopped=%d unknown=%d conflict=%d（列出 %d，排序 %s）\n",
		total, counts.Live, counts.Stale, counts.Stopped, counts.Unknown, counts.Conflict, len(view.Nodes), sortKey)
	if hidden := total - len(view.Nodes); hidden > 0 {
		if view.Filter != nil && view.Filter.State == FilterStateLive {
			fmt.Fprintf(c.out(), "已隐藏 %d 个节点（默认只列在线；用 `aicli-mesh ls -a` 查看全部，counts 仍为全量口径）。\n", hidden)
		} else {
			fmt.Fprintf(c.out(), "已隐藏 %d 个节点（当前过滤条件；counts 仍为全量口径）。\n", hidden)
		}
	}
	if view.Root == "" {
		fmt.Fprintln(c.out(), "网格根目录未解析（fail-closed）：没有可读的节点档案。")
	} else {
		fmt.Fprintf(c.out(), "根目录 %s（%s）\n", view.Root, pathsSourceLabel(view))
	}
	if counts.Conflict > 0 {
		fmt.Fprintln(c.out(), "提示：存在 conflict（同一会话被多个存活节点占用），用 `aicli-mesh show <会话>` 查看详情。")
	}
}

func pathsSourceLabel(view MeshView) string {
	if view.Self != nil {
		return "self"
	}
	return "resolved"
}

func pidCell(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return strconv.Itoa(pid)
}

func cellOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func endpointCell(node NodeView) string {
	if node.Endpoint == nil || strings.TrimSpace(node.Endpoint.BaseURL) == "" {
		if node.Endpoint == nil {
			return "-"
		}
		return fmt.Sprintf("%s:%d", fallbackHost(node.Endpoint.Host), node.Endpoint.Port)
	}
	return node.Endpoint.BaseURL
}

func fallbackHost(host string) string {
	if strings.TrimSpace(host) == "" {
		return "127.0.0.1"
	}
	return host
}

func ownershipCell(node NodeView) string {
	if node.Ownership == "" || node.Ownership == OwnershipNone {
		return "-"
	}
	return string(node.Ownership)
}

// formatAge renders AgeSec as a compact age (or "-" when never reported).
func formatAge(ageSec int) string {
	if ageSec < 0 {
		return "-"
	}
	switch {
	case ageSec < 60:
		return fmt.Sprintf("%ds", ageSec)
	case ageSec < 3600:
		return fmt.Sprintf("%dm%02ds", ageSec/60, ageSec%60)
	case ageSec < 86400:
		return fmt.Sprintf("%dh%02dm", ageSec/3600, (ageSec%3600)/60)
	default:
		return fmt.Sprintf("%dd%02dh", ageSec/86400, (ageSec%86400)/3600)
	}
}

// ---------------------------------------------------------------------------
// Table rendering (UTF-8 aware: CJK glyphs occupy two cells)
// ---------------------------------------------------------------------------

func renderTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = displayWidth(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			if width := displayWidth(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}
	var builder strings.Builder
	writeRow := func(cells []string) {
		for i, cell := range cells {
			if i >= len(widths) {
				break
			}
			builder.WriteString(cell)
			if i < len(cells)-1 {
				builder.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+2))
			}
		}
		builder.WriteString("\n")
	}
	writeRow(headers)
	separators := make([]string, len(headers))
	for i := range headers {
		separators[i] = strings.Repeat("-", widths[i])
	}
	writeRow(separators)
	for _, row := range rows {
		writeRow(row)
	}
	return builder.String()
}

func displayWidth(value string) int {
	width := 0
	for _, r := range value {
		switch {
		case r == '\t':
			width += 4
		case isWideRune(r):
			width += 2
		default:
			width++
		}
	}
	return width
}

// isWideRune reports whether r renders two cells wide in a monospace terminal
// (CJK / Hangul / fullwidth forms). Approximate on purpose: exact wcwidth
// tables are not worth a dependency here.
func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6:
		return true
	}
	return false
}

func truncateCell(value string, max int) string {
	if max <= 0 || displayWidth(value) <= max {
		return value
	}
	var builder strings.Builder
	width := 0
	for _, r := range value {
		runeWidth := 1
		if isWideRune(r) {
			runeWidth = 2
		}
		if width+runeWidth > max-1 {
			break
		}
		builder.WriteRune(r)
		width += runeWidth
	}
	builder.WriteString("…")
	return builder.String()
}

// ---------------------------------------------------------------------------
// show
// ---------------------------------------------------------------------------

// leaseView is the CLI's stable rendering of one lease file.
type leaseView struct {
	Purpose     string    `json:"purpose"`
	Key         string    `json:"key"`
	OwnerNodeID string    `json:"owner_node_id"`
	OwnerPID    int       `json:"owner_pid"`
	OwnerAlive  bool      `json:"owner_alive"`
	Expired     bool      `json:"expired"`
	ExpiresAt   time.Time `json:"expires_at"`
	TTLSec      int       `json:"ttl_sec"`
	Path        string    `json:"path,omitempty"`
}

// showReveal is the token-facing section of `show`: unlike `ls` / the HTTP
// views (which only ever publish auth.token_hint), `show` is a targeted local
// inspection, so it reports the record's write token and the browser bootstrap
// URL verbatim (§9.1). Fields stay empty when the node has no token or no
// loopback endpoint.
type showReveal struct {
	Token       string `json:"token,omitempty"`
	TokenSource string `json:"token_source,omitempty"`
	WebURL      string `json:"web_url,omitempty"`
}

// showResult is the `show --json` document.
type showResult struct {
	SchemaVersion int         `json:"schema_version"`
	Node          *NodeView   `json:"node"`
	Token         string      `json:"token,omitempty"`
	TokenSource   string      `json:"token_source,omitempty"`
	WebURL        string      `json:"web_url,omitempty"`
	Leases        []leaseView `json:"leases,omitempty"`
}

func (c *CLI) runShow(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"json":   flagBool,
		"events": flagValue,
		"help":   flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh show: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) != 1 {
		return c.fail(ExitUsage, "aicli-mesh show: 需要一个目标（节点 ID / 会话 ID / pid:<PID>）")
	}
	events, err := parsed.intValue("events", 10)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh show: %v", err)
	}
	if events < 0 {
		return c.fail(ExitUsage, "aicli-mesh show: --events 不能为负数")
	}
	paths := c.paths()
	view := BuildView(paths, ViewOptions{Now: c.now(), JournalTailLimit: events})
	node, err := resolveTarget(view, parsed.pos[0])
	if err != nil {
		return c.failTargetError(err)
	}
	leases := c.leasesForNode(paths, node)
	token, tokenSource := recordToken(node)
	reveal := showReveal{Token: token, TokenSource: tokenSource, WebURL: webURLWithToken(node, token)}
	if parsed.boolean("json") {
		if err := c.printJSON(showResult{
			SchemaVersion: SchemaVersion,
			Node:          &node,
			Token:         reveal.Token,
			TokenSource:   reveal.TokenSource,
			WebURL:        reveal.WebURL,
			Leases:        leases,
		}); err != nil {
			return c.fail(ExitFailure, "aicli-mesh show: 输出 JSON 失败: %v", err)
		}
		return ExitOK
	}
	c.renderShow(node, leases, events, reveal)
	return ExitOK
}

func (c *CLI) failTargetError(err error) int {
	if target, ok := err.(*targetError); ok {
		return c.fail(target.code, "aicli-mesh: %s", target.msg)
	}
	return c.fail(ExitFailure, "aicli-mesh: %v", err)
}

// leasesForNode returns the leases that concern this node: held by it, or keyed
// by its session.
func (c *CLI) leasesForNode(paths Paths, node NodeView) []leaseView {
	sessionID := sessionIDOf(node)
	out := make([]leaseView, 0, 2)
	for _, file := range ListLeases(paths) {
		if !file.OK {
			continue
		}
		lease := file.Lease
		if lease.OwnerNodeID != node.NodeID && (sessionID == "" || lease.Key != sessionID) {
			continue
		}
		out = append(out, leaseView{
			Purpose:     lease.Purpose,
			Key:         lease.Key,
			OwnerNodeID: lease.OwnerNodeID,
			OwnerPID:    lease.OwnerPID,
			OwnerAlive:  processAlive(lease.OwnerPID),
			Expired:     lease.Expired(c.now()),
			ExpiresAt:   lease.ExpiresAt(),
			TTLSec:      lease.TTLSec,
			Path:        file.Path,
		})
	}
	return out
}

func (c *CLI) renderShow(node NodeView, leases []leaseView, events int, reveal showReveal) {
	now := c.now()
	state := string(node.State)
	if node.State == NodeStateLive {
		state = fmt.Sprintf("live（心跳 %s 前）", formatAge(node.AgeSec))
	}
	fmt.Fprintf(c.out(), "节点 %s\n", node.NodeID)
	writeKV(c.out(), "  状态", state)
	if node.PID > 0 {
		writeKV(c.out(), "  进程", fmt.Sprintf("pid %d%s", node.PID, processOriginLabel(node)))
	}
	writeKV(c.out(), "  归属", string(node.Ownership))
	writeKV(c.out(), "  端点", endpointDetail(node))
	writeKV(c.out(), "  令牌", tokenDetail(node, reveal))
	if reveal.WebURL != "" {
		writeKV(c.out(), "  访问", reveal.WebURL)
	}
	if node.Session != nil {
		writeKV(c.out(), "  会话", fmt.Sprintf("%s %s state=%s busy=%t%s",
			node.Session.ID, quotedTitle(node.Session.Title), node.Session.State, node.Session.Busy, turnSuffix(node.Session.TurnID)))
	}
	if node.Workspace != nil && node.Workspace.Path != "" {
		writeKV(c.out(), "  工作区", fmt.Sprintf("%s（%s）", node.Workspace.Path, cellOrDash(node.Workspace.Name)))
	}
	if node.Binding != nil {
		writeKV(c.out(), "  绑定", bindingDetail(node.Binding, now))
	}
	if len(leases) == 0 {
		writeKV(c.out(), "  租约", "-")
	}
	for _, lease := range leases {
		writeKV(c.out(), "  租约", fmt.Sprintf("%s-%s owner=%s pid=%d %s%s",
			lease.Purpose, lease.Key, lease.OwnerNodeID, lease.OwnerPID,
			leaseStateLabel(lease), leasePathSuffix(lease)))
	}
	writeKV(c.out(), "  记录", cellOrDash(node.Path))
	if node.Err != "" {
		writeKV(c.out(), "  错误", node.Err)
	}
	if events > 0 {
		fmt.Fprintf(c.out(), "  日志尾部（最近 %d 条）\n", len(node.JournalTail))
		for _, line := range node.JournalTail {
			fmt.Fprintf(c.out(), "    %s\n", line)
		}
	}
}

func writeKV(w io.Writer, key, value string) {
	fmt.Fprintf(w, "%s  %s\n", key, value)
}

func processOriginLabel(node NodeView) string {
	if node.Process == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if node.Process.Version != "" {
		parts = append(parts, node.Process.Version)
	}
	if node.Process.Origin != "" {
		parts = append(parts, "origin="+node.Process.Origin)
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "，") + "）"
}

func quotedTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	return fmt.Sprintf("%q", title)
}

func turnSuffix(turnID string) string {
	if strings.TrimSpace(turnID) == "" {
		return ""
	}
	return " turn=" + turnID
}

func endpointDetail(node NodeView) string {
	if node.Endpoint == nil {
		return "无（纯 TUI 进程：可被发现，不可调用）"
	}
	detail := endpointCell(node)
	if node.Endpoint.WebBaseURL != "" {
		detail += "（web " + node.Endpoint.WebBaseURL + "）"
	}
	if node.Endpoint.ManifestURL != "" {
		detail += "（清单 " + node.Endpoint.ManifestURL + "）"
	}
	return detail
}

// tokenDetail is the `令牌` line of `show`: with a readable token the CLI
// prints the record's secret verbatim (the §9.1 disclosure surface chosen for
// `show`), annotated with the record's auth mode; without one it degrades to
// the redacted hint so readers can still tell "no token" from "required".
func tokenDetail(node NodeView, reveal showReveal) string {
	if reveal.Token == "" {
		return authDetail(node)
	}
	detail := reveal.Token
	if node.Auth != nil {
		segments := []string{fmt.Sprintf("required=%t", node.Auth.Required)}
		if node.Auth.Mode != "" {
			segments = append(segments, "mode="+node.Auth.Mode)
		}
		if reveal.TokenSource != "" {
			segments = append(segments, "source="+reveal.TokenSource)
		}
		detail += "（" + strings.Join(segments, ", ") + "）"
	}
	return detail
}

func authDetail(node NodeView) string {
	if node.Auth == nil {
		return "无（该节点不要求写令牌）"
	}
	detail := cellOrDash(node.Auth.TokenHint)
	if node.Auth.Required {
		detail += "（required=true"
	} else {
		detail += "（required=false"
	}
	if node.Auth.Mode != "" {
		detail += ", mode=" + node.Auth.Mode
	}
	return detail + "）"
}

func bindingDetail(binding *SessionBinding, now time.Time) string {
	detail := "-"
	if binding.Preferred != nil {
		detail = fmt.Sprintf("%s:%d", fallbackHost(binding.Preferred.Host), binding.Preferred.Port)
	}
	if binding.LastNodeID != "" {
		detail += " last_node=" + binding.LastNodeID
	}
	if !binding.UpdatedAt.IsZero() {
		detail += fmt.Sprintf(" 更新于 %s 前", formatAge(int(now.Sub(binding.UpdatedAt)/time.Second)))
	}
	return detail
}

func leaseStateLabel(lease leaseView) string {
	state := "有效"
	if lease.Expired {
		state = "已过期"
	}
	if !lease.OwnerAlive {
		state += "（持有者进程已退出）"
	}
	return state
}

func leasePathSuffix(lease leaseView) string {
	if lease.Path == "" {
		return ""
	}
	return " " + lease.Path
}

// ---------------------------------------------------------------------------
// url
// ---------------------------------------------------------------------------

// urlResult is the `url --json` document.
type urlResult struct {
	SchemaVersion int    `json:"schema_version"`
	NodeID        string `json:"node_id"`
	SessionID     string `json:"session_id,omitempty"`
	State         string `json:"state"`
	URL           string `json:"url"`
	WithToken     bool   `json:"with_token"`
	TokenSource   string `json:"token_source,omitempty"`
}

func (c *CLI) runURL(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"with-token": flagBool,
		"path":       flagValue,
		"json":       flagBool,
		"help":       flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh url: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) != 1 {
		return c.fail(ExitUsage, "aicli-mesh url: 需要一个目标（节点 ID / 会话 ID / pid:<PID>）")
	}
	paths := c.paths()
	view := BuildView(paths, ViewOptions{Now: c.now()})
	node, err := resolveTarget(view, parsed.pos[0])
	if err != nil {
		return c.failTargetError(err)
	}
	base, ok := nodeBaseURL(node)
	if !ok {
		return c.fail(ExitUnreachable, "aicli-mesh url: 目标 %s 没有 loopback 端点（纯 TUI 进程不可访问）", node.NodeID)
	}
	target := joinURLPath(base, parsed.str("path", "/web"))
	withToken := parsed.boolean("with-token")
	token, tokenSource := recordToken(node)
	if withToken && token != "" {
		target += "?token=" + queryEscape(token)
	}
	if withToken && token == "" {
		fmt.Fprintln(c.errOut(), "提示：目标节点没有写令牌（开发模式或未启用写保护），URL 不含令牌。")
	}
	if node.State != NodeStateLive {
		fmt.Fprintf(c.errOut(), "提示：目标当前状态为 %s，地址可能已失效。\n", node.State)
	}
	if parsed.boolean("json") {
		if err := c.printJSON(urlResult{
			SchemaVersion: SchemaVersion,
			NodeID:        node.NodeID,
			SessionID:     sessionIDOf(node),
			State:         string(node.State),
			URL:           target,
			WithToken:     withToken && token != "",
			TokenSource:   tokenSource,
		}); err != nil {
			return c.fail(ExitFailure, "aicli-mesh url: 输出 JSON 失败: %v", err)
		}
		return ExitOK
	}
	fmt.Fprintln(c.out(), target)
	return ExitOK
}

// recordToken reads the node's raw write token from its record. The CLI
// materialises the token for its two local disclosure surfaces only: `show`
// (token + window URL) and `url --with-token` (§9.1).
func recordToken(node NodeView) (string, string) {
	if node.Record == nil || node.Record.Auth == nil {
		return "", ""
	}
	return strings.TrimSpace(node.Record.Auth.Token), strings.TrimSpace(node.Record.Auth.TokenSource)
}

// nodeBaseURL returns the node's loopback HTTP base URL and whether it has a
// usable endpoint at all (a pure TUI node has none).
func nodeBaseURL(node NodeView) (string, bool) {
	if node.Endpoint == nil || (strings.TrimSpace(node.Endpoint.BaseURL) == "" && node.Endpoint.Port <= 0) {
		return "", false
	}
	base := strings.TrimSpace(node.Endpoint.BaseURL)
	if base == "" {
		base = fmt.Sprintf("http://%s:%d", fallbackHost(node.Endpoint.Host), node.Endpoint.Port)
	}
	return base, true
}

// webURLWithToken builds the §7.3 browser bootstrap URL (`<web_base_url>?token=…`)
// that `show` prints: empty when the node has no token or no loopback endpoint.
func webURLWithToken(node NodeView, token string) string {
	if strings.TrimSpace(token) == "" {
		return ""
	}
	base, ok := nodeBaseURL(node)
	if !ok {
		return ""
	}
	return joinURLPath(base, "/web") + "?token=" + queryEscape(token)
}

func joinURLPath(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return base + "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// queryEscape escapes a query value without pulling net/url into the hot path
// of the package (it is a one-liner for the token alphabet we emit).
func queryEscape(value string) string {
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~' {
			builder.WriteByte(b)
			continue
		}
		fmt.Fprintf(&builder, "%%%02X", b)
	}
	return builder.String()
}

// ---------------------------------------------------------------------------
// gc
// ---------------------------------------------------------------------------

// gc defaults (architecture §2.4 / §7.2).
const (
	// DefaultGCStaleTTL is the grace period before a dead process's leftovers
	// (node record, lease) become collectable. It is deliberately longer than
	// the heartbeat TTL: a short network hiccup must not let gc delete a node
	// that is merely quiet.
	DefaultGCStaleTTL = 10 * time.Minute
	// DefaultGCKeepDays is how long journals and (with --prune-bindings)
	// bindings survive after their node is gone.
	DefaultGCKeepDays = 7
)

// gcAction is one planned deletion. The plan is computed identically for the
// dry-run and the --apply pass, so the two outputs are comparable (§12.1).
type gcAction struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes,omitempty"`
}

// gcPlan is the `gc` document. Actions is the plan; Applied/Deleted/Errors only
// differ between dry-run and --apply.
type gcPlan struct {
	SchemaVersion int        `json:"schema_version"`
	DryRun        bool       `json:"dry_run"`
	Root          string     `json:"root,omitempty"`
	GeneratedAt   time.Time  `json:"generated_at"`
	StaleTTL      string     `json:"stale_ttl"`
	KeepDays      int        `json:"keep_days"`
	PurgeLegacy   bool       `json:"purge_legacy"`
	PruneBindings bool       `json:"prune_bindings"`
	Actions       []gcAction `json:"actions"`
	ActionCount   int        `json:"action_count"`
	ReclaimBytes  int64      `json:"reclaim_bytes"`
	Applied       bool       `json:"applied"`
	Deleted       int        `json:"deleted"`
	Errors        []string   `json:"errors,omitempty"`
}

func (c *CLI) runGC(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"apply":          flagBool,
		"json":           flagBool,
		"stale-ttl":      flagValue,
		"keep-days":      flagValue,
		"purge-legacy":   flagBool,
		"prune-bindings": flagBool,
		"help":           flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh gc: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) > 0 {
		return c.fail(ExitUsage, "aicli-mesh gc: 不接受位置参数（收到 %q）", parsed.pos[0])
	}
	staleTTL, err := parsed.durationValue("stale-ttl", DefaultGCStaleTTL)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh gc: %v", err)
	}
	if staleTTL < 0 {
		return c.fail(ExitUsage, "aicli-mesh gc: --stale-ttl 不能为负数")
	}
	keepDays, err := parsed.intValue("keep-days", DefaultGCKeepDays)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh gc: %v", err)
	}
	if keepDays < 0 {
		return c.fail(ExitUsage, "aicli-mesh gc: --keep-days 不能为负数")
	}
	now := c.now()
	paths := c.paths()
	plan := c.buildGCPlan(paths, gcOptions{
		Now:           now,
		StaleTTL:      staleTTL,
		KeepDays:      keepDays,
		PurgeLegacy:   parsed.boolean("purge-legacy"),
		PruneBindings: parsed.boolean("prune-bindings"),
	})
	apply := parsed.boolean("apply")
	plan.DryRun = !apply
	if apply {
		plan.Applied = true
		plan.Deleted, plan.Errors = applyGCActions(plan.Actions)
	}
	if parsed.boolean("json") {
		if err := c.printJSON(plan); err != nil {
			return c.fail(ExitFailure, "aicli-mesh gc: 输出 JSON 失败: %v", err)
		}
		return ExitOK
	}
	c.renderGC(plan)
	if len(plan.Errors) > 0 {
		return ExitFailure
	}
	return ExitOK
}

type gcOptions struct {
	Now           time.Time
	StaleTTL      time.Duration
	KeepDays      int
	PurgeLegacy   bool
	PruneBindings bool
}

func (c *CLI) buildGCPlan(paths Paths, opts gcOptions) gcPlan {
	plan := gcPlan{
		SchemaVersion: SchemaVersion,
		Root:          paths.Root,
		GeneratedAt:   opts.Now,
		StaleTTL:      opts.StaleTTL.String(),
		KeepDays:      opts.KeepDays,
		PurgeLegacy:   opts.PurgeLegacy,
		PruneBindings: opts.PruneBindings,
		Actions:       []gcAction{},
	}
	keepWindow := time.Duration(opts.KeepDays) * 24 * time.Hour
	view := BuildView(paths, ViewOptions{Now: opts.Now})
	aliveSessions := map[string]bool{}
	recordedSessions := map[string]bool{}
	for _, node := range view.Nodes {
		sessionID := sessionIDOf(node)
		if sessionID == "" {
			continue
		}
		recordedSessions[sessionID] = true
		if node.State == NodeStateLive {
			aliveSessions[sessionID] = true
		}
	}
	// 1. Dead node records: pid must be gone *and* the leftover must be older
	//    than the grace period. A live pid is never deleted (rule R3).
	for _, node := range view.Nodes {
		if node.State != NodeStateStale && node.State != NodeStateStopped {
			continue
		}
		if node.PID > 0 && processAlive(node.PID) {
			continue
		}
		if age := nodeAge(node, opts.Now); age < opts.StaleTTL {
			continue
		}
		if strings.TrimSpace(node.Path) == "" {
			continue
		}
		plan.Actions = append(plan.Actions, gcAction{
			Kind:   "node-record",
			Path:   node.Path,
			Reason: nodeRecordReason(node, opts.Now),
			Bytes:  fileSize(node.Path),
		})
	}
	// 2. Leases: expired by TTL, or held by a process that is gone.
	for _, file := range ListLeases(paths) {
		if !file.OK {
			// Unreadable / unknown-schema lease: never destroy what we cannot
			// interpret. `doctor` reports it instead.
			continue
		}
		lease := file.Lease
		ownerAlive := processAlive(lease.OwnerPID)
		switch {
		case lease.Expired(opts.Now):
			plan.Actions = append(plan.Actions, gcAction{
				Kind:   "lease",
				Path:   file.Path,
				Reason: fmt.Sprintf("租约已过期（%s-%s，TTL %s）", lease.Purpose, lease.Key, lease.TTL()),
				Bytes:  fileSize(file.Path),
			})
		case !ownerAlive && opts.Now.Sub(leaseRenewedAt(lease)) > opts.StaleTTL:
			plan.Actions = append(plan.Actions, gcAction{
				Kind:   "lease",
				Path:   file.Path,
				Reason: fmt.Sprintf("持有者进程已退出（%s-%s，pid %d）", lease.Purpose, lease.Key, lease.OwnerPID),
				Bytes:  fileSize(file.Path),
			})
		}
	}
	// 3. Journals: orphaned or belonging to a dead node, and older than
	//    --keep-days (audit value: keep history for a while). --keep-days 0
	//    means "collect immediately".
	for _, entry := range listJournalFiles(paths) {
		nodeID := strings.TrimSuffix(filepath.Base(entry.path), ".ndjson")
		record, known := nodeRecordByID(view, nodeID)
		orphan := !known
		deadNode := known && record.PID > 0 && !processAlive(record.PID)
		if !orphan && !deadNode {
			continue
		}
		if opts.Now.Sub(entry.modTime) < keepWindow {
			continue
		}
		reason := fmt.Sprintf("节点 %s 的日志（进程已退出）", nodeID)
		if orphan {
			reason = fmt.Sprintf("孤儿日志：没有 %s 的节点档案", nodeID)
		}
		plan.Actions = append(plan.Actions, gcAction{Kind: "journal", Path: entry.path, Reason: reason, Bytes: entry.size})
	}
	// 4. Bindings: a preference is only pruned when nothing live serves the
	//    session and the file has been untouched for --keep-days.
	if opts.PruneBindings {
		for _, entry := range listBindingFiles(paths) {
			sessionID := entry.sessionID
			if sessionID == "" || aliveSessions[sessionID] {
				continue
			}
			if opts.Now.Sub(entry.modTime) < keepWindow {
				continue
			}
			reason := fmt.Sprintf("会话 %s 的绑定（已无存活节点服务，%d 天未更新）", sessionID, opts.KeepDays)
			if !recordedSessions[sessionID] {
				reason = fmt.Sprintf("会话 %s 的绑定（没有任何节点档案引用）", sessionID)
			}
			plan.Actions = append(plan.Actions, gcAction{Kind: "binding", Path: entry.path, Reason: reason, Bytes: entry.size})
		}
	}
	// 5. Legacy directory (M8): the whole `web-ports/` tree is dead weight.
	if opts.PurgeLegacy {
		if legacy := legacyWebPortsDir(); legacy != "" {
			if info, err := os.Stat(legacy); err == nil && info.IsDir() {
				size, count := dirSize(legacy)
				plan.Actions = append(plan.Actions, gcAction{
					Kind:   "legacy-dir",
					Path:   legacy,
					Reason: fmt.Sprintf("旧目录已作废（%d 个文件；web-ports 由会话绑定取代，§2.4）", count),
					Bytes:  size,
				})
			}
		}
	}
	sort.SliceStable(plan.Actions, func(i, j int) bool {
		if plan.Actions[i].Kind != plan.Actions[j].Kind {
			return plan.Actions[i].Kind < plan.Actions[j].Kind
		}
		return plan.Actions[i].Path < plan.Actions[j].Path
	})
	plan.ActionCount = len(plan.Actions)
	for _, action := range plan.Actions {
		plan.ReclaimBytes += action.Bytes
	}
	return plan
}

func nodeAge(node NodeView, now time.Time) time.Duration {
	if node.HeartbeatAt != nil {
		if age := now.Sub(*node.HeartbeatAt); age > 0 {
			return age
		}
		return 0
	}
	if info, err := os.Stat(node.Path); err == nil {
		return now.Sub(info.ModTime())
	}
	return 0
}

func nodeRecordReason(node NodeView, now time.Time) string {
	switch {
	case node.State == NodeStateStopped:
		return "进程已声明停止（stopped）"
	case node.PID <= 0:
		return "档案没有 pid，且心跳已过期"
	default:
		return fmt.Sprintf("进程已退出（pid %d 不存在，心跳 %s 前）", node.PID, formatAge(node.AgeSec))
	}
}

func leaseRenewedAt(lease Lease) time.Time {
	if !lease.RenewedAt.IsZero() {
		return lease.RenewedAt
	}
	return lease.AcquiredAt
}

func nodeRecordByID(view MeshView, nodeID string) (NodeView, bool) {
	for _, node := range view.Nodes {
		if node.NodeID == nodeID {
			return node, true
		}
	}
	return NodeView{}, false
}

type dirEntryInfo struct {
	path      string
	size      int64
	modTime   time.Time
	sessionID string
}

func listJournalFiles(paths Paths) []dirEntryInfo {
	if !paths.Enabled() {
		return nil
	}
	entries, err := os.ReadDir(paths.Journal)
	if err != nil {
		return nil
	}
	out := make([]dirEntryInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ndjson") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, dirEntryInfo{
			path:    filepath.Join(paths.Journal, entry.Name()),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}
	return out
}

func listBindingFiles(paths Paths) []dirEntryInfo {
	if !paths.Enabled() {
		return nil
	}
	entries, err := os.ReadDir(paths.Bindings)
	if err != nil {
		return nil
	}
	out := make([]dirEntryInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(paths.Bindings, entry.Name())
		sessionID := ""
		if data, err := os.ReadFile(path); err == nil {
			var binding SessionBinding
			if json.Unmarshal(data, &binding) == nil {
				sessionID = strings.TrimSpace(binding.SessionID)
			}
		}
		out = append(out, dirEntryInfo{
			path:      path,
			size:      info.Size(),
			modTime:   info.ModTime(),
			sessionID: sessionID,
		})
	}
	return out
}

func applyGCActions(actions []gcAction) (int, []string) {
	deleted := 0
	var failures []string
	for _, action := range actions {
		var err error
		if action.Kind == "legacy-dir" {
			err = os.RemoveAll(action.Path)
		} else {
			err = os.Remove(action.Path)
		}
		if err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Sprintf("%s: %v", action.Path, err))
			continue
		}
		deleted++
	}
	return deleted, failures
}

func (c *CLI) renderGC(plan gcPlan) {
	if plan.DryRun {
		fmt.Fprintln(c.out(), "网格清理计划（dry-run：未改动任何文件）")
	} else {
		fmt.Fprintln(c.out(), "网格清理（--apply）")
	}
	if plan.Root == "" {
		fmt.Fprintln(c.out(), "  网格根目录未解析（fail-closed）：没有可清理的对象。")
		return
	}
	fmt.Fprintf(c.out(), "  根目录 %s\n", plan.Root)
	if len(plan.Actions) == 0 {
		fmt.Fprintln(c.out(), "  没有可清理的对象。")
		return
	}
	for _, action := range plan.Actions {
		fmt.Fprintf(c.out(), "  [%s] %s：%s\n", action.Kind, action.Path, action.Reason)
	}
	fmt.Fprintf(c.out(), "  共 %d 项，可回收 %s\n", plan.ActionCount, humanBytes(plan.ReclaimBytes))
	if plan.DryRun {
		fmt.Fprintln(c.out(), "  应用：aicli-mesh gc --apply（另有 --purge-legacy / --prune-bindings）")
	} else {
		fmt.Fprintf(c.out(), "  已删除 %d 项\n", plan.Deleted)
	}
	for _, failure := range plan.Errors {
		fmt.Fprintf(c.errOut(), "  失败：%s\n", failure)
	}
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

func dirSize(path string) (int64, int) {
	var size int64
	count := 0
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, statErr := entry.Info(); statErr == nil {
			size += info.Size()
		}
		count++
		return nil
	})
	return size, count
}

func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}

// legacyWebPortsDir resolves the retired `~/.aicli/web-ports/` directory
// (architecture §2.4). It follows the same home rules as the mesh root, minus
// the `mesh` suffix. Returns "" when no home is resolvable.
func legacyWebPortsDir() string {
	if home := strings.TrimSpace(os.Getenv(EnvHome)); home != "" {
		if expanded := ExpandUserPath(home); expanded != "" {
			return filepath.Join(expanded, "web-ports")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".aicli", "web-ports")
	}
	return ""
}

// ---------------------------------------------------------------------------
// doctor
// ---------------------------------------------------------------------------

// legacyFreshWindow is how recently a `web-ports/` file must have been touched
// to count as "an old-version process is probably still running" (§2.4).
const legacyFreshWindow = 10 * time.Minute

// doctorCheck is one self-check result: ok / warn / problem.
type doctorCheck struct {
	ID     string   `json:"id"`
	Status string   `json:"status"`
	Detail string   `json:"detail,omitempty"`
	Items  []string `json:"items,omitempty"`
}

// doctorReport is the `doctor --json` document.
type doctorReport struct {
	SchemaVersion int           `json:"schema_version"`
	Root          string        `json:"root,omitempty"`
	Source        string        `json:"source"`
	Now           time.Time     `json:"now"`
	Checks        []doctorCheck `json:"checks"`
	Problems      int           `json:"problems"`
	Warnings      int           `json:"warnings"`
}

func (c *CLI) runDoctor(args []string) int {
	parsed, err := parseArgs(args, flagSpec{"json": flagBool, "help": flagBool})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh doctor: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) > 0 {
		return c.fail(ExitUsage, "aicli-mesh doctor: 不接受位置参数（收到 %q）", parsed.pos[0])
	}
	report := c.buildDoctorReport(c.paths(), c.now())
	if parsed.boolean("json") {
		if err := c.printJSON(report); err != nil {
			return c.fail(ExitFailure, "aicli-mesh doctor: 输出 JSON 失败: %v", err)
		}
	} else {
		c.renderDoctor(report)
	}
	if report.Problems > 0 {
		return ExitFailure
	}
	return ExitOK
}

func (c *CLI) buildDoctorReport(paths Paths, now time.Time) doctorReport {
	report := doctorReport{
		SchemaVersion: SchemaVersion,
		Root:          paths.Root,
		Source:        paths.Source,
		Now:           now,
		Checks:        []doctorCheck{},
	}
	add := func(id, status, detail string, items []string) {
		report.Checks = append(report.Checks, doctorCheck{ID: id, Status: status, Detail: detail, Items: items})
		switch status {
		case "problem":
			report.Problems++
		case "warn":
			report.Warnings++
		}
	}

	// 1. Directory layout.
	if !paths.Enabled() {
		add("paths", "problem",
			"网格根目录未解析（AICLI_MESH_DIR / AICLI_HOME / home 均不可用）：网格处于 fail-closed 状态，读为空、写跳过", nil)
	} else {
		missing := make([]string, 0, 4)
		for _, dir := range []string{paths.Nodes, paths.Bindings, paths.Leases, paths.Journal} {
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				missing = append(missing, dir)
			}
		}
		detail := fmt.Sprintf("根目录 %s（来源 %s）", paths.Root, paths.Source)
		if len(missing) > 0 {
			detail += fmt.Sprintf("；尚未创建的目录 %d 个（首次写入时自动创建）", len(missing))
		}
		add("paths", "ok", detail, nil)
	}

	// 2. Permissions (Windows has no POSIX mode bits: profile ACL is the story).
	report.addPermissionCheck(paths, add)

	// 3. Records: readable? known schema? how many are dead?
	view := BuildView(paths, ViewOptions{Now: now})
	unreadable := make([]string, 0, 2)
	unknownSchema := make([]string, 0, 2)
	dead := make([]string, 0, 4)
	for _, node := range view.Nodes {
		switch {
		case node.Err != "":
			unreadable = append(unreadable, fmt.Sprintf("%s: %s", node.NodeID, node.Err))
		case node.State == NodeStateUnknown:
			unknownSchema = append(unknownSchema, node.NodeID)
		case node.State == NodeStateStale || node.State == NodeStateStopped:
			dead = append(dead, fmt.Sprintf("%s（%s，pid %d）", node.NodeID, node.State, node.PID))
		}
	}
	switch {
	case len(unreadable) > 0:
		add("nodes", "problem", fmt.Sprintf("%d 个节点档案无法读取", len(unreadable)), unreadable)
	case len(unknownSchema) > 0:
		add("nodes", "warn", fmt.Sprintf("%d 个档案的 schema 版本未知（不参与归属判定，也不会被改写）", len(unknownSchema)), unknownSchema)
	default:
		add("nodes", "ok", fmt.Sprintf("%d 个节点档案可读", len(view.Nodes)), nil)
	}
	if len(dead) > 0 {
		add("stale-nodes", "warn",
			fmt.Sprintf("%d 个节点已退出但档案仍在（`aicli-mesh gc` 可清理）", len(dead)), dead)
	} else {
		add("stale-nodes", "ok", "没有陈旧节点档案", nil)
	}

	// 4. Ownership: the same session claimed by two live nodes is the conflict
	//    the whole lease mechanism exists to prevent (§4.2).
	conflicts := make([]string, 0, 2)
	for _, node := range view.Nodes {
		if node.Ownership == OwnershipConflict {
			conflicts = append(conflicts, targetLabel(node))
		}
	}
	if len(conflicts) > 0 {
		add("ownership", "problem",
			fmt.Sprintf("%d 个存活节点声明同一会话（双占用）", len(conflicts)), conflicts)
	} else {
		add("ownership", "ok", "没有会话双占用", nil)
	}

	// 5. Leases: readable, and not held by a dead process.
	leaseIssues := make([]string, 0, 2)
	deadLeases := make([]string, 0, 2)
	for _, file := range ListLeases(paths) {
		if !file.OK {
			leaseIssues = append(leaseIssues, file.Path)
			continue
		}
		if !processAlive(file.Lease.OwnerPID) {
			deadLeases = append(deadLeases, fmt.Sprintf("%s-%s（pid %d）", file.Lease.Purpose, file.Lease.Key, file.Lease.OwnerPID))
		}
	}
	if len(leaseIssues) > 0 {
		add("leases", "warn", fmt.Sprintf("%d 个租约文件无法解析（保留不动，需人工确认）", len(leaseIssues)), leaseIssues)
	} else if len(deadLeases) > 0 {
		add("leases", "warn", fmt.Sprintf("%d 个租约的持有者已退出（`aicli-mesh gc` 可回收）", len(deadLeases)), deadLeases)
	} else {
		add("leases", "ok", "租约文件可读且持有者存活", nil)
	}

	// 6. Tokens: a record that claims auth.required must carry a token, or every
	//    remote write will 403.
	missingTokens := make([]string, 0, 2)
	for _, node := range view.Nodes {
		if node.Record == nil || node.Record.Auth == nil {
			continue
		}
		if node.Record.Auth.Required && strings.TrimSpace(node.Record.Auth.Token) == "" {
			missingTokens = append(missingTokens, node.NodeID)
		}
	}
	if len(missingTokens) > 0 {
		add("tokens", "warn", fmt.Sprintf("%d 个节点声明需要写令牌但档案中没有令牌（远程写操作会 403）", len(missingTokens)), missingTokens)
	} else {
		add("tokens", "ok", "所有声明需要令牌的节点都可读到令牌", nil)
	}

	// 7. Journals: torn lines are expected on a crash (the tail is appended
	//    without a lock), an unreadable file is not.
	torn := make([]string, 0, 2)
	unreadableJournals := make([]string, 0, 2)
	orphanJournals := make([]string, 0, 2)
	totalTorn := 0
	for _, entry := range listJournalFiles(paths) {
		nodeID := strings.TrimSuffix(filepath.Base(entry.path), ".ndjson")
		if _, known := nodeRecordByID(view, nodeID); !known {
			orphanJournals = append(orphanJournals, nodeID)
		}
		data, err := os.ReadFile(entry.path)
		if err != nil {
			unreadableJournals = append(unreadableJournals, entry.path)
			continue
		}
		if broken := countTornJournalLines(string(data)); broken > 0 {
			totalTorn += broken
			torn = append(torn, fmt.Sprintf("%s: %d 行", nodeID, broken))
		}
	}
	switch {
	case len(unreadableJournals) > 0:
		add("journal", "problem", fmt.Sprintf("%d 个日志文件无法读取", len(unreadableJournals)), unreadableJournals)
	case totalTorn > 0:
		add("journal", "warn", fmt.Sprintf("日志中有 %d 行无法解析（崩溃时的半行，读侧会跳过）", totalTorn), torn)
	default:
		add("journal", "ok", "日志文件完整可读", nil)
	}
	if len(orphanJournals) > 0 {
		add("journal-orphans", "warn",
			fmt.Sprintf("%d 个日志没有对应的节点档案（`aicli-mesh gc` 在 --keep-days 后清理）", len(orphanJournals)), orphanJournals)
	} else {
		add("journal-orphans", "ok", "没有孤儿日志", nil)
	}

	// 8. Legacy directory freshness (§2.4): an old-version process is invisible
	//    to the mesh, so a fresh `web-ports/` file is the only hint it exists.
	legacy := legacyWebPortsDir()
	if legacy == "" {
		add("legacy", "warn", "无法解析 home 目录，跳过旧目录检查", nil)
	} else if info, err := os.Stat(legacy); err != nil || !info.IsDir() {
		add("legacy", "ok", "没有旧目录残留（web-ports 已作废）", nil)
	} else {
		fresh, count := 0, 0
		_ = filepath.WalkDir(legacy, func(_ string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			count++
			if info, statErr := entry.Info(); statErr == nil && now.Sub(info.ModTime()) < legacyFreshWindow {
				fresh++
			}
			return nil
		})
		if fresh > 0 {
			add("legacy", "warn",
				fmt.Sprintf("旧目录 %s 中有 %d/%d 个文件在 %s 内更新过：可能有旧版本进程在运行（未被网格收录），建议升级或重启该进程",
					legacy, fresh, count, legacyFreshWindow), nil)
		} else {
			add("legacy", "warn",
				fmt.Sprintf("旧目录残留 %s（%d 个文件）：`aicli-mesh gc --purge-legacy` 可清理", legacy, count), nil)
		}
	}

	// 9. Spawn executable: which aicli binary `open` / POST /web/api/mesh/spawn
	//    would launch, and which rule picked it. With several installs on one
	//    machine this is the first thing to check when the wrong version comes
	//    up; a broken AICLI_BIN is a problem (explicit misconfiguration), a
	//    missing binary only a warning (spawn is optional for read-only use).
	switch resolution, resolveErr := ResolveSpawnExecutable(); {
	case resolveErr != nil:
		add("spawn-executable", "problem", fmt.Sprintf("拉起节点的可执行文件不可用：%v", resolveErr), nil)
	case resolution.Path == "":
		add("spawn-executable", "warn",
			"无法定位 aicli 可执行文件（设置 AICLI_BIN 或从完整安装运行）：`open` 与 POST /web/api/mesh/spawn 都会 failed", nil)
	default:
		add("spawn-executable", "ok",
			fmt.Sprintf("拉起节点使用 %s（来源 %s）", resolution.Path, resolution.Source), nil)
	}
	return report
}

func (r *doctorReport) addPermissionCheck(paths Paths, add func(id, status, detail string, items []string)) {
	if runtime.GOOS == "windows" {
		home, _ := os.UserHomeDir()
		if paths.Enabled() && home != "" && !pathWithin(home, paths.Root) {
			add("permissions", "warn", fmt.Sprintf(
				"网格根目录不在用户 Profile 内（%s）：Windows 没有 POSIX 权限位，档案保密性依赖目录 ACL，共享目录属于误用（R13）",
				paths.Root), nil)
			return
		}
		add("permissions", "ok", "Windows 下档案保密性依赖用户 Profile 权限（无 POSIX 权限位，§9.6）", nil)
		return
	}
	loose := make([]string, 0, 4)
	if info, err := os.Stat(paths.Nodes); err == nil && info.Mode().Perm()&0o077 != 0 {
		loose = append(loose, fmt.Sprintf("%s 权限 %04o（期望 0700）", paths.Nodes, info.Mode().Perm()))
	}
	for _, node := range ListNodeFiles(paths) {
		if info, err := os.Stat(node.Path); err == nil && info.Mode().Perm()&0o077 != 0 {
			loose = append(loose, fmt.Sprintf("%s 权限 %04o（期望 0600）", filepath.Base(node.Path), info.Mode().Perm()))
		}
	}
	if len(loose) > 0 {
		add("permissions", "warn", "档案权限比预期宽松（同机其它用户可读）", loose)
		return
	}
	add("permissions", "ok", "档案与目录权限符合预期（0600/0700）", nil)
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// countTornJournalLines counts lines that are not valid JSON objects. Blank
// lines are not torn (the writer appends "\n" per entry).
func countTornJournalLines(data string) int {
	broken := 0
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			broken++
		}
	}
	return broken
}

func (c *CLI) renderDoctor(report doctorReport) {
	fmt.Fprintln(c.out(), "aicli-mesh doctor")
	if report.Root == "" {
		fmt.Fprintf(c.out(), "  根目录 -（来源 %s）\n", report.Source)
	} else {
		fmt.Fprintf(c.out(), "  根目录 %s（来源 %s）\n", report.Root, report.Source)
	}
	for _, check := range report.Checks {
		marker := map[string]string{"ok": "[ok]  ", "warn": "[warn]", "problem": "[问题]"}[check.Status]
		fmt.Fprintf(c.out(), "  %s %-16s %s\n", marker, check.ID, check.Detail)
		for _, item := range check.Items {
			fmt.Fprintf(c.out(), "           - %s\n", item)
		}
	}
	fmt.Fprintf(c.out(), "\n  结论：问题 %d，告警 %d\n", report.Problems, report.Warnings)
	if report.Problems > 0 {
		fmt.Fprintln(c.out(), "  有问题需要处理（退出码 5）。")
	}
}

// ---------------------------------------------------------------------------
// version
// ---------------------------------------------------------------------------

// versionResult is the `version --json` document.
type versionResult struct {
	SchemaVersion       int    `json:"schema_version"`
	Name                string `json:"name"`
	Version             string `json:"version"`
	GoVersion           string `json:"go_version"`
	OS                  string `json:"os"`
	Arch                string `json:"arch"`
	MeshRoot            string `json:"mesh_root,omitempty"`
	MeshRootSource      string `json:"mesh_root_source"`
	RecordSchemaVersion int    `json:"record_schema_version"`
}

func (c *CLI) runVersion(args []string) int {
	parsed, err := parseArgs(args, flagSpec{"json": flagBool, "help": flagBool})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh version: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) > 0 {
		return c.fail(ExitUsage, "aicli-mesh version: 不接受位置参数（收到 %q）", parsed.pos[0])
	}
	paths := c.paths()
	result := versionResult{
		SchemaVersion:       SchemaVersion,
		Name:                "aicli-mesh",
		Version:             c.version(),
		GoVersion:           runtime.Version(),
		OS:                  runtime.GOOS,
		Arch:                runtime.GOARCH,
		MeshRoot:            paths.Root,
		MeshRootSource:      paths.Source,
		RecordSchemaVersion: SchemaVersion,
	}
	if parsed.boolean("json") {
		if err := c.printJSON(result); err != nil {
			return c.fail(ExitFailure, "aicli-mesh version: 输出 JSON 失败: %v", err)
		}
		return ExitOK
	}
	fmt.Fprintf(c.out(), "aicli-mesh %s（%s，%s/%s）\n", result.Version, result.GoVersion, result.OS, result.Arch)
	if paths.Root == "" {
		fmt.Fprintf(c.out(), "网格根目录 -（来源 %s）\n", paths.Source)
	} else {
		fmt.Fprintf(c.out(), "网格根目录 %s（来源 %s）\n", paths.Root, paths.Source)
	}
	fmt.Fprintf(c.out(), "记录 schema_version=%d\n", SchemaVersion)
	return ExitOK
}

// ---------------------------------------------------------------------------
// 网格调用子命令（S8，架构 §5.6 / §7.3）
//
// call / send / screen 是同一套调用编排的三个入口：call 是通用形式（op +
// --args），send 与 screen 是 op 的固定写法（invoke / screen），三者共用
// mesh.Call 与同一退出码映射。
// ---------------------------------------------------------------------------

// callResultSchemaVersion 让 --json 输出带 schema_version，与其它子命令一致。
type callJSONResult struct {
	SchemaVersion int `json:"schema_version"`
	*CallResult
}

func (c *CLI) runCall(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"args":              flagValue,
		"client-request-id": flagValue,
		"allow-write":       flagBool,
		"timeout":           flagValue,
		"json":              flagBool,
		"help":              flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh call: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) != 2 {
		return c.fail(ExitUsage, "aicli-mesh call: 需要 <目标> <op>（op 见 aicli-mesh help）")
	}
	timeout, err := parsed.durationValue("timeout", CallDefaultTimeout)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh call: --timeout: %v", err)
	}
	rawArgs := strings.TrimSpace(parsed.str("args", ""))
	if rawArgs != "" && !json.Valid([]byte(rawArgs)) {
		return c.fail(ExitUsage, "aicli-mesh call: --args 必须是合法 JSON（收到 %q）", rawArgs)
	}
	request := CallRequest{
		Target:          parsed.pos[0],
		Op:              parsed.pos[1],
		Args:            json.RawMessage(rawArgs),
		ClientRequestID: strings.TrimSpace(parsed.str("client-request-id", "")),
		Timeout:         timeout,
		AllowWrite:      parsed.boolean("allow-write"),
	}
	return c.runCallRequest(request, parsed.boolean("json"), "")
}

// runSend 是 invoke op 的固定写法：注入 prompt 并等待该 turn 结束（§5.6）。
// 写操作无隐式放行：必须显式 --allow-write。
func (c *CLI) runSend(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"allow-write":       flagBool,
		"timeout":           flagValue,
		"client-request-id": flagValue,
		"json":              flagBool,
		"help":              flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh send: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) < 2 {
		return c.fail(ExitUsage, "aicli-mesh send: 需要 <目标> <prompt>")
	}
	timeout, err := parsed.durationValue("timeout", CallDefaultTimeout)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh send: --timeout: %v", err)
	}
	prompt := strings.TrimSpace(strings.Join(parsed.pos[1:], " "))
	if prompt == "" {
		return c.fail(ExitUsage, "aicli-mesh send: prompt 不能为空")
	}
	if !parsed.boolean("allow-write") {
		// invoke 是写操作：无隐式放行（§5.6 要点 8）。
		return c.fail(ExitRefused, "aicli-mesh send: invoke 是写操作，需要 --allow-write 显式允许")
	}
	body, err := json.Marshal(map[string]any{"prompt": prompt})
	if err != nil {
		return c.fail(ExitFailure, "aicli-mesh send: 编码请求失败: %v", err)
	}
	request := CallRequest{
		Target:          parsed.pos[0],
		Op:              "invoke",
		Args:            body,
		ClientRequestID: strings.TrimSpace(parsed.str("client-request-id", "")),
		Timeout:         timeout,
		AllowWrite:      true,
	}
	return c.runCallRequest(request, parsed.boolean("json"), "assistant.content")
}

// runScreen 是 screen op 的固定写法：读取目标的屏幕快照（只读，无需
// --allow-write）。默认 view=tui + format=json，人读输出只打印 text 段。
func (c *CLI) runScreen(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"view":    flagValue,
		"tail":    flagValue,
		"format":  flagValue,
		"timeout": flagValue,
		"json":    flagBool,
		"help":    flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh screen: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) != 1 {
		return c.fail(ExitUsage, "aicli-mesh screen: 需要 <目标>（节点 ID / 会话 ID）")
	}
	timeout, err := parsed.durationValue("timeout", CallDefaultTimeout)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh screen: --timeout: %v", err)
	}
	payload := map[string]any{
		"view":   parsed.str("view", "tui"),
		"format": parsed.str("format", "json"),
	}
	if tail := strings.TrimSpace(parsed.str("tail", "")); tail != "" {
		if _, convErr := strconv.Atoi(tail); convErr != nil {
			return c.fail(ExitUsage, "aicli-mesh screen: --tail 需要整数（收到 %q）", tail)
		}
		payload["tail"] = tail
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return c.fail(ExitFailure, "aicli-mesh screen: 编码请求失败: %v", err)
	}
	request := CallRequest{
		Target:  parsed.pos[0],
		Op:      "screen",
		Args:    body,
		Timeout: timeout,
	}
	return c.runCallRequest(request, parsed.boolean("json"), "text")
}

// runCallRequest 执行一次调用并统一处理输出与退出码。
//
// humanField 指定人读输出优先打印 result 里的哪个字段（点分路径，如
// "assistant.content"）：命中就只打印它（send/screen 的主产物），未命中则
// 回退到 pretty JSON —— 输出永远不丢信息。
func (c *CLI) runCallRequest(request CallRequest, asJSON bool, humanField string) int {
	result := Call(context.Background(), c.paths(), "", request)
	exitCode := callExitCode(result)
	if asJSON {
		if err := c.printJSON(callJSONResult{SchemaVersion: SchemaVersion, CallResult: result}); err != nil {
			return c.fail(ExitFailure, "输出 JSON 失败: %v", err)
		}
		return exitCode
	}
	if !result.OK() {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = "目标返回 " + result.Status
		}
		fmt.Fprintf(c.errOut(), "调用失败: %s", result.Status)
		if result.Code != "" {
			fmt.Fprintf(c.errOut(), "（%s）", result.Code)
		}
		fmt.Fprintf(c.errOut(), ": %s\n", message)
		return exitCode
	}
	if humanField != "" {
		if text := callResultField(result.Result, strings.Split(humanField, ".")...); text != "" {
			fmt.Fprintln(c.out(), text)
			return exitCode
		}
	}
	if len(result.Result) > 0 {
		if pretty, err := prettyJSON(result.Result); err == nil {
			fmt.Fprintln(c.out(), pretty)
			return exitCode
		}
	}
	fmt.Fprintf(c.out(), "%s %s（%dms）\n", result.NodeID, result.Op, result.ElapsedMs)
	return exitCode
}

// callExitCode 把调用状态映射到 §7.3 的稳定退出码。
func callExitCode(result *CallResult) int {
	if result == nil {
		return ExitFailure
	}
	switch result.Status {
	case CallStatusOK:
		return ExitOK
	case CallStatusBusy:
		return ExitConflict
	case CallStatusNotFound:
		return ExitNotFound
	case CallStatusUnreachable, CallStatusTimeout:
		return ExitUnreachable
	case CallStatusRefused:
		return ExitRefused
	default:
		return ExitFailure
	}
}

// callResultField 按点分路径从 result JSON 里取字符串字段（数字也转字符串）。
// 字段不存在或类型不符时返回空串。
func callResultField(raw json.RawMessage, path ...string) string {
	if len(raw) == 0 || len(path) == 0 {
		return ""
	}
	var current any
	if err := json.Unmarshal(raw, &current); err != nil {
		// result 可能是 JSON 字符串（screen 的 text/plain 形态）：单段路径直接取它。
		var text string
		if stringErr := json.Unmarshal(raw, &text); stringErr == nil && len(path) == 1 {
			return strings.TrimRight(text, "\n")
		}
		return ""
	}
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = object[key]
		if !ok {
			return ""
		}
	}
	switch value := current.(type) {
	case string:
		return strings.TrimRight(value, "\n")
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

// prettyJSON 以两空格缩进重排一段 JSON（失败时由调用方回退）。
func prettyJSON(raw json.RawMessage) (string, error) {
	var buffer bytes.Buffer
	if err := json.Indent(&buffer, raw, "", "  "); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

// ---------------------------------------------------------------------------
// open —— §5.7 的本地写法（确保会话有活节点，并给出 §7.3 的窗口 URL）
// ---------------------------------------------------------------------------

// openResult 是 `open --json` 的输出：与 HTTP 端点同一形状（§5.7 / §5.9），
// 外层加 schema_version 以与其它子命令一致。
type openResult struct {
	SchemaVersion int `json:"schema_version"`
	*SpawnResult
}

// runOpen 确保某个会话在它自己的工作区里有活节点，并打印 §7.3 的窗口 URL。
//
// 与浏览器路径（POST /web/api/mesh/spawn）的差别：CLI 就是操作者本人，敲下这
// 条命令本身就是授权，所以没有 --mesh-allow-spawn 那样的进程开关；但 CLI
// **不是节点**（从不写档案），单飞租约因此用一个合成 owner `cli-<pid>`——
// 租约文件是可回收的审计记录，不冒充节点身份。
func (c *CLI) runOpen(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"port":     flagValue,
		"wait":     flagValue,
		"no-wait":  flagBool,
		"takeover": flagBool,
		"bin":      flagValue,
		"json":     flagBool,
		"help":     flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh open: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) != 1 {
		return c.fail(ExitUsage, "aicli-mesh open: 需要 <会话>（会话 ID / 节点 ID / pid:<PID>）")
	}
	paths := c.paths()
	if !paths.Enabled() {
		return c.fail(ExitFailure, "aicli-mesh open: 网格根目录不可用（设置 AICLI_MESH_DIR 或 AICLI_HOME）")
	}
	wait, err := parsed.durationValue("wait", time.Duration(DefaultSpawnWaitMS)*time.Millisecond)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh open: --wait: %v", err)
	}
	port := 0
	if raw := strings.TrimSpace(parsed.str("port", "")); raw != "" {
		value, convErr := strconv.Atoi(raw)
		if convErr != nil || value < 1 || value > 65535 {
			return c.fail(ExitUsage, "aicli-mesh open: --port 需要 1-65535 的整数（收到 %q）", raw)
		}
		port = value
	}
	bin := strings.TrimSpace(parsed.str("bin", ""))
	if bin != "" {
		// --bin 是「就用这一个二进制」：指错必须当场失败（与 AICLI_BIN 同一条
		// 规则），绝不悄悄换成旁边的 aicli.exe。
		if info, statErr := os.Stat(bin); statErr != nil {
			return c.fail(ExitUsage, "aicli-mesh open: --bin %s 不可用：%v", bin, statErr)
		} else if info.IsDir() {
			return c.fail(ExitUsage, "aicli-mesh open: --bin %s 是目录，不是可执行文件", bin)
		}
	}
	sessionID, source := openSessionTarget(paths, c.now(), parsed.pos[0])
	if sessionID == "" {
		return c.fail(ExitNotFound, "aicli-mesh open: 无法从 %q 解析出会话（节点没有会话，或目标为空）", parsed.pos[0])
	}
	if source == "assumed" {
		fmt.Fprintf(c.errOut(), "提示：%s 既不是节点也不是已有绑定，按会话 ID 原样拉起。\n", sessionID)
	}

	result := c.spawn()(
		SpawnRequest{
			SessionID: sessionID,
			Port:      port,
			WaitMS:    int(wait / time.Millisecond),
			Origin:    "cli",
			Takeover:  parsed.boolean("takeover"),
		},
		SpawnOptions{
			Paths:         paths,
			Now:           c.now,
			SelfNodeID:    fmt.Sprintf("cli-%d", os.Getpid()),
			PID:           os.Getpid(),
			Wait:          wait,
			Executable:    bin,
			FireAndForget: parsed.boolean("no-wait"),
		})
	if parsed.boolean("json") {
		if err := c.printJSON(openResult{SchemaVersion: SchemaVersion, SpawnResult: &result}); err != nil {
			return c.fail(ExitFailure, "aicli-mesh open: 输出 JSON 失败: %v", err)
		}
	} else {
		c.printSpawnHuman(result)
		if parsed.boolean("takeover") && result.Status == SpawnStatusStarted {
			fmt.Fprintln(c.out(), "接管：新节点将在会话激活时回收租约；旧节点继续运行并标记 orphaned（不会被杀）。")
		}
	}
	return openExitCode(result.Status)
}

// openSessionTarget 把 <会话> 参数解析成会话 ID，三种来源按可信度排序：
//  1. 目标是节点（活档案或残留档案）→ 用它的 session.id；
//  2. 目标命中会话绑定（S3：进程不在也能读到上次工作区）→ 用它；
//  3. 都没有 → 原样当会话 ID（会话从未在本机落过绑定也能拉起，
//     子进程会自己解析工作区，就像 `aicli resume` 一直做的那样）。
//
// 含路径分隔符的目标一律拒绝：那不是会话 ID，也没有必要把它交给子进程。
func openSessionTarget(paths Paths, now time.Time, target string) (sessionID string, source string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", ""
	}
	view := BuildView(paths, ViewOptions{Now: now})
	if node, err := resolveTarget(view, target); err == nil {
		if sid := sessionIDOf(node); sid != "" {
			return sid, "node"
		}
		return "", ""
	}
	if binding, ok := LoadBinding(paths, target); ok && strings.TrimSpace(binding.SessionID) != "" {
		return strings.TrimSpace(binding.SessionID), "binding"
	}
	if strings.ContainsAny(target, `/\`) {
		return "", ""
	}
	return target, "assumed"
}

// printSpawnHuman 是拉起结果的人读输出：成功打印窗口 URL，失败打印原因与
// 日志尾部（已脱敏，M7）。
func (c *CLI) printSpawnHuman(result SpawnResult) {
	switch result.Status {
	case SpawnStatusReused, SpawnStatusStarted:
		verb := "复用"
		if result.Status == SpawnStatusStarted {
			verb = "已拉起"
		}
		fmt.Fprintf(c.out(), "%s节点 %s（会话 %s，pid %d，端口 %d）\n",
			verb, result.NodeID, result.SessionID, result.PID, result.Port)
		if strings.TrimSpace(result.URL) != "" {
			fmt.Fprintln(c.out(), result.URL)
		}
		if result.Reason != "" {
			fmt.Fprintln(c.errOut(), "提示："+result.Reason)
		}
	default:
		fmt.Fprintf(c.errOut(), "拉起未成功（%s）：%s\n", result.Status, result.Reason)
		for _, line := range result.LogTail {
			fmt.Fprintln(c.errOut(), "  "+line)
		}
	}
}

// openExitCode 把 §5.7 的四态映射到 §7.3 的退出码：复用/拉起成功 = 0，
// 超时未见节点 = 3（目标不可达），启动失败 = 5（操作失败）。
func openExitCode(status string) int {
	switch status {
	case SpawnStatusReused, SpawnStatusStarted:
		return ExitOK
	case SpawnStatusNotRunning:
		return ExitUnreachable
	default:
		return ExitFailure
	}
}

// ---------------------------------------------------------------------------
// new —— 新建会话（拉起 `aicli chat`，会话 ID 由子进程生成）
// ---------------------------------------------------------------------------

// newResult 是 `new --json` 的输出：与 open 同一形状（§5.7 的 SpawnResult），
// 外加 workspace——会话 ID 是子进程生成的，工作区是调用方给的，脚本两个都要。
type newResult struct {
	SchemaVersion int    `json:"schema_version"`
	Workspace     string `json:"workspace,omitempty"`
	*SpawnResult
}

// runNew 在一个工作区里新建会话：拉起 `aicli chat`（不带会话 ID）并等它的节点
// 档案出现，然后打印新会话 ID 与窗口 URL。
//
// 与 open 的差别是刻意的：open 以会话为键（复用活节点、按会话单飞），new 此时
// 还没有会话 ID，所以只做「启动 + 按 pid 等档案」。CLI 依旧不是节点：不写档案、
// 不写绑定、不占租约——档案是子进程自己写的（与 open 同一条实现，见 mesh.Spawn
// 的 spawnNewSession）。
func (c *CLI) runNew(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"workspace": flagValue,
		"port":      flagValue,
		"wait":      flagValue,
		"no-wait":   flagBool,
		"bin":       flagValue,
		"json":      flagBool,
		"help":      flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh new: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) != 0 {
		return c.fail(ExitUsage, "aicli-mesh new: 不接受位置参数（工作区用 --workspace 指定）")
	}
	paths := c.paths()
	if !paths.Enabled() {
		return c.fail(ExitFailure, "aicli-mesh new: 网格根目录不可用（设置 AICLI_MESH_DIR 或 AICLI_HOME）")
	}
	wait, err := parsed.durationValue("wait", time.Duration(DefaultSpawnWaitMS)*time.Millisecond)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh new: --wait: %v", err)
	}
	port := 0
	if raw := strings.TrimSpace(parsed.str("port", "")); raw != "" {
		value, convErr := strconv.Atoi(raw)
		if convErr != nil || value < 1 || value > 65535 {
			return c.fail(ExitUsage, "aicli-mesh new: --port 需要 1-65535 的整数（收到 %q）", raw)
		}
		port = value
	}
	bin := strings.TrimSpace(parsed.str("bin", ""))
	if bin != "" {
		// 与 open 同一条规则：--bin 是「就用这一个二进制」，指错当场失败。
		if info, statErr := os.Stat(bin); statErr != nil {
			return c.fail(ExitUsage, "aicli-mesh new: --bin %s 不可用：%v", bin, statErr)
		} else if info.IsDir() {
			return c.fail(ExitUsage, "aicli-mesh new: --bin %s 是目录，不是可执行文件", bin)
		}
	}
	workspace, code, msg := newWorkspaceTarget(parsed.str("workspace", ""))
	if code != ExitOK {
		return c.fail(code, "aicli-mesh new: %s", msg)
	}

	result := c.spawn()(
		SpawnRequest{
			NewSession: true,
			Port:       port,
			WaitMS:     int(wait / time.Millisecond),
			Origin:     "cli",
		},
		SpawnOptions{
			Paths:      paths,
			Now:        c.now,
			SelfNodeID: fmt.Sprintf("cli-%d", os.Getpid()),
			PID:        os.Getpid(),
			Wait:       wait,
			Executable: bin,
			// 新会话没有绑定可查：工作区只能由调用方给出（--workspace 或当前目录）。
			WorkspaceFor:  func(string) (string, bool) { return workspace, true },
			FireAndForget: parsed.boolean("no-wait"),
		})
	if parsed.boolean("json") {
		if err := c.printJSON(newResult{SchemaVersion: SchemaVersion, Workspace: workspace, SpawnResult: &result}); err != nil {
			return c.fail(ExitFailure, "aicli-mesh new: 输出 JSON 失败: %v", err)
		}
	} else {
		c.printNewHuman(result, workspace)
	}
	return openExitCode(result.Status)
}

// newWorkspaceTarget 解析新会话的工作区：--workspace 优先，否则当前目录；返回
// 绝对路径（档案里记的是子进程的 cwd，相对路径会随调用方目录漂移）。
func newWorkspaceTarget(raw string) (dir string, code int, msg string) {
	dir = strings.TrimSpace(raw)
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", ExitFailure, fmt.Sprintf("无法确定当前目录：%v（用 --workspace 指定）", err)
		}
		dir = cwd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", ExitUsage, fmt.Sprintf("--workspace %s 无法解析：%v", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", ExitUsage, fmt.Sprintf("--workspace %s 不可用：%v", abs, err)
	}
	if !info.IsDir() {
		return "", ExitUsage, fmt.Sprintf("--workspace %s 不是目录", abs)
	}
	return abs, ExitOK, ""
}

// printNewHuman 是新建结果的人读输出。--no-wait 时档案还没出现、会话 ID 未知：
// 如实说「已启动、ID 待查」，而不是印一个空会话。
func (c *CLI) printNewHuman(result SpawnResult, workspace string) {
	if result.Status != SpawnStatusStarted {
		c.printSpawnHuman(result)
		return
	}
	if strings.TrimSpace(result.SessionID) == "" {
		fmt.Fprintf(c.out(), "已启动新会话的节点进程（pid %d，端口 %d）\n", result.PID, result.Port)
	} else {
		fmt.Fprintf(c.out(), "新会话 %s 已就绪（节点 %s，pid %d，端口 %d）\n",
			result.SessionID, result.NodeID, result.PID, result.Port)
	}
	if workspace != "" {
		fmt.Fprintf(c.out(), "工作区：%s\n", workspace)
	}
	if strings.TrimSpace(result.URL) != "" {
		fmt.Fprintln(c.out(), result.URL)
	}
	if result.Reason != "" {
		fmt.Fprintln(c.errOut(), "提示："+result.Reason)
	}
}

// ---------------------------------------------------------------------------
// stop —— §5.7 的本地写法（优雅退出或强制终止目标节点）
// ---------------------------------------------------------------------------

// stopResult 是 `stop --json` 的输出：与 HTTP 端点同一形状（§5.7），外层加
// schema_version 以与其它子命令一致。
type stopResult struct {
	SchemaVersion int `json:"schema_version"`
	*StopResult
}

// runStop 停止目标节点（§7.2 / §5.7）。默认优雅：把 /exit 投给目标的
// /web/api/input，等它自己收尾；--force 才终止进程。
//
// 治理开关在**目标**进程（--mesh-allow-stop），且两层都拦：目标自己的 HTTP
// 处理器在进程内拦（403），本地编排（CLI / Web）在投递前按档案的
// CapabilityStop 拦（refused）——这样「谁能停我」由被停者决定，而不是由一台
// 可能被入侵的调用方机器决定（§9.2：CLI / Web 都绕不过开关）。
// CLI 不是节点：callerID 为空，自停检查天然不适用（CLI 没有可被停的档案）。
func (c *CLI) runStop(args []string) int {
	parsed, err := parseArgs(args, flagSpec{
		"force": flagBool,
		"wait":  flagValue,
		"json":  flagBool,
		"help":  flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh stop: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) != 1 {
		return c.fail(ExitUsage, "aicli-mesh stop: 需要 <目标>（节点 ID / 会话 ID / pid:<PID>）")
	}
	paths := c.paths()
	if !paths.Enabled() {
		return c.fail(ExitFailure, "aicli-mesh stop: 网格根目录不可用（设置 AICLI_MESH_DIR 或 AICLI_HOME）")
	}
	wait, err := parsed.durationValue("wait", StopDefaultWait)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh stop: --wait: %v", err)
	}
	mode := StopModeGraceful
	if parsed.boolean("force") {
		mode = StopModeForce
	}
	result := Stop(context.Background(), paths, "", StopRequest{
		Target: parsed.pos[0],
		Mode:   mode,
		Wait:   wait,
	})
	if parsed.boolean("json") {
		if err := c.printJSON(stopResult{SchemaVersion: SchemaVersion, StopResult: result}); err != nil {
			return c.fail(ExitFailure, "aicli-mesh stop: 输出 JSON 失败: %v", err)
		}
	} else {
		c.printStopHuman(result)
	}
	return stopExitCode(result.Status)
}

// printStopHuman 是停止结果的人读输出：成功一行，失败给原因与下一步。
func (c *CLI) printStopHuman(result *StopResult) {
	if result == nil {
		return
	}
	switch result.Status {
	case StopStatusStopped:
		verb := "已终止"
		if result.Graceful {
			verb = "已优雅退出"
		}
		fmt.Fprintf(c.out(), "%s节点 %s（pid %d，模式 %s）\n", verb, result.NodeID, result.PID, result.Mode)
		if result.Code == StopCodeAlreadyStopped {
			fmt.Fprintln(c.errOut(), "提示："+result.Message)
		}
	default:
		fmt.Fprintf(c.errOut(), "停止未成功（%s）：%s\n", result.Status, result.Message)
		if result.Code != "" {
			fmt.Fprintf(c.errOut(), "原因码：%s\n", result.Code)
		}
		switch result.Code {
		case CallCodeNoEndpoint:
			fmt.Fprintln(c.errOut(), "提示：该节点没有回环控制面，用 --force 直接终止进程。")
		case StopCodeTimeout:
			fmt.Fprintln(c.errOut(), "提示：加大 --wait，或用 --force 直接终止进程。")
		case StopCodeNotAllowed:
			fmt.Fprintln(c.errOut(), "提示：目标进程未开启停止开关（--mesh-allow-stop）。")
		}
	}
}

// stopExitCode 把 §5.7 的停止状态映射到 §7.3 的退出码：stopped = 0，
// 目标不存在 = 2，等待超时 = 3（不可达/没等到），策略拒绝 = 6，其余 = 5。
func stopExitCode(status string) int {
	switch status {
	case StopStatusStopped:
		return ExitOK
	case StopStatusNotFound:
		return ExitNotFound
	case StopStatusTimeout:
		return ExitUnreachable
	case StopStatusRefused:
		return ExitRefused
	default:
		return ExitFailure
	}
}

// ---------------------------------------------------------------------------
// watch（journal 事件流）
// ---------------------------------------------------------------------------

// watchResult is the `watch --once --json` envelope (§7.3 stable schema).
// Follow mode cannot use an envelope (the stream never ends), so it switches to
// one JournalEntry per line — the only documented exception to the envelope.
type watchResult struct {
	SchemaVersion int            `json:"schema_version"`
	Events        []JournalEntry `json:"events"`
	Counts        watchCounts    `json:"counts"`
}

type watchCounts struct {
	Events int `json:"events"`
	Nodes  int `json:"nodes"`
}

// runWatch implements `aicli-mesh watch`: replay the journal window and, unless
// --once, keep tailing. It never talks to a node, so it works when every
// process has already exited (architecture §6.6 / §7.2).
func (c *CLI) runWatch(args []string) int {
	// --no-color 目前是兼容开关：watch 的人读输出本来就不带颜色（与 ls/gc 的
	// 纯文本表格同一口径），保留它是为了脚本与文档里的命令行稳定。
	parsed, err := parseArgs(args, flagSpec{
		"since":    flagValue,
		"node":     flagValue,
		"session":  flagValue,
		"once":     flagBool,
		"limit":    flagValue,
		"interval": flagValue,
		"json":     flagBool,
		"no-color": flagBool,
		"help":     flagBool,
	})
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh watch: %v", err)
	}
	if parsed.boolean("help") {
		c.printUsage(c.out())
		return ExitOK
	}
	if len(parsed.pos) > 0 {
		return c.fail(ExitUsage, "aicli-mesh watch: 不接受位置参数（收到 %q）；过滤用 --node / --session", parsed.pos[0])
	}
	paths := c.paths()
	if !paths.Enabled() {
		return c.fail(ExitFailure, "aicli-mesh watch: 网格根目录不可用（设置 AICLI_MESH_DIR 或 AICLI_HOME）")
	}
	since, err := parsed.durationValue("since", WatchDefaultSince)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh watch: --since: %v", err)
	}
	interval, err := parsed.durationValue("interval", WatchDefaultInterval)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh watch: --interval: %v", err)
	}
	if interval < 50*time.Millisecond {
		return c.fail(ExitUsage, "aicli-mesh watch: --interval 最小 50ms（避免忙等）")
	}
	limit, err := parsed.intValue("limit", 0)
	if err != nil {
		return c.fail(ExitUsage, "aicli-mesh watch: --limit: %v", err)
	}
	if limit < 0 {
		return c.fail(ExitUsage, "aicli-mesh watch: --limit 不能为负")
	}
	opts := WatchOptions{
		Since:     since,
		NodeID:    strings.TrimSpace(parsed.str("node", "")),
		SessionID: strings.TrimSpace(parsed.str("session", "")),
		Follow:    !parsed.boolean("once"),
		Interval:  interval,
		Limit:     limit,
		Now:       c.now,
	}

	events, err := CollectJournalEvents(paths, opts)
	if err != nil {
		return c.fail(ExitFailure, "aicli-mesh watch: 读取 journal 失败: %v", err)
	}
	if filtered := opts.NodeID != "" || opts.SessionID != ""; filtered && len(events) == 0 {
		// 区分「目标根本不存在」（退出码 2）与「目标存在、但窗口内没有事件」
		// （正常空结果，退出码 0）：长跑进程上后者很常见，不该当失败。
		all, err := CollectJournalEvents(paths, WatchOptions{NodeID: opts.NodeID, SessionID: opts.SessionID, Now: c.now})
		if err != nil {
			return c.fail(ExitFailure, "aicli-mesh watch: 读取 journal 失败: %v", err)
		}
		if len(all) == 0 {
			return c.fail(ExitNotFound, "aicli-mesh watch: 没有匹配的事件（--node %q / --session %q）", opts.NodeID, opts.SessionID)
		}
	}

	jsonMode := parsed.boolean("json")
	if !opts.Follow {
		if jsonMode {
			payload := watchResult{
				SchemaVersion: SchemaVersion,
				Events:        watchEntries(events),
				Counts:        watchCounts{Events: len(events), Nodes: watchNodeCount(events)},
			}
			if err := c.printJSON(payload); err != nil {
				return c.fail(ExitFailure, "aicli-mesh watch: 输出 JSON 失败: %v", err)
			}
			return ExitOK
		}
		for _, event := range events {
			fmt.Fprintln(c.out(), formatWatchEvent(event))
		}
		if len(events) == 0 {
			fmt.Fprintln(c.errOut(), "（窗口内没有事件；--since 0 可回放全部）")
		}
		return ExitOK
	}

	// 实时模式：Ctrl-C 是正常收尾（退出码 0），不是失败。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	emit := func(event WatchEvent) error {
		if jsonMode {
			data, err := json.Marshal(event.Entry)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(c.out(), "%s\n", data)
			return err
		}
		_, err := fmt.Fprintln(c.out(), formatWatchEvent(event))
		return err
	}
	switch err := WatchJournal(ctx, paths, opts, emit); {
	case errors.Is(err, context.Canceled):
		return ExitOK
	case err != nil:
		return c.fail(ExitFailure, "aicli-mesh watch: %v", err)
	default:
		return ExitOK
	}
}

// formatWatchEvent renders one event for humans. The node id is always shown:
// watch merges every node on the machine, and `show` already covers the
// single-node view.
func formatWatchEvent(event WatchEvent) string {
	return fmt.Sprintf("[%s] %s", event.Entry.NodeID, FormatJournalEntry(event.Entry))
}

func watchEntries(events []WatchEvent) []JournalEntry {
	out := make([]JournalEntry, 0, len(events))
	for _, event := range events {
		out = append(out, event.Entry)
	}
	return out
}

func watchNodeCount(events []WatchEvent) int {
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		seen[event.Entry.NodeID] = struct{}{}
	}
	return len(seen)
}
