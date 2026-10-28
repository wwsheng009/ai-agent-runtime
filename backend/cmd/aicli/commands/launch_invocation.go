package commands

// launch_invocation.go 记录「用户是怎么把这次 aicli 启动起来的」。
//
// 为什么必须在启动期抓取：会话列表要能把一个已有会话在**另一个终端**里原样拉起
// （`aicli sessions` 交互界面、`aicli resume <id> --detach`、gateway/mesh 的窗口
// 控制），靠的不是重新猜一遍配置，而是把用户本次命令行上的旗标如实持久化
// （--web / --yolo / --debug / -m …）。这些 token 只存在于进程 argv 里，cobra
// 解析完成后原始序列就丢了，所以快照必须发生在 main 的最前面。
//
// 两份口径，故意不等价：
//   - launch_argv：用户原始调用（含子命令与位置参数），敏感值脱敏后逐字保留，
//     只用于展示与审计——它不保证可重放（--session、--prompt 重放会改写会话）。
//   - launch_args：只含「可重放」的旗标 token，是重建启动命令的唯一来源。
//
// 旗标分类用 cobra 自己的 flag 元数据（NoOptDefVal 区分布尔旗标与取值旗标），
// 因此 `--web --yolo` 不会把 --yolo 误当成 --web 的值；未注册的 token 一律不进
// 重放集（宁可少带参数，也不能把下一个旗标吞成值）。

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// launchHistoryMaxEntries 是 launch_history 的条数上限（最新在前）。会话每被一个
// 新进程接手就写一条，不设上限会让大会话的 metadata 单调膨胀。
const launchHistoryMaxEntries = 8

// launchRedactedValue 是敏感旗标值的替身。宁可丢失原值，也不能把令牌写进会话库
// （mesh spawn 的日志脱敏是同一条理由）。
const launchRedactedValue = "***"

// launchSecretFlagWords 是「尾词判定」集合：旗标名按 -/_ 分词后，最后一个词命中
// 这里任一项即视为携带密钥（--web-token、--api-key、--client-secret）。
//
// 为什么不是子串匹配（第一版就是子串，被单测抓到）：`--budget-tokens` 里也含
// "token"，子串判定会把单轮 token 预算当成密钥——它既被从 launch_args 里剔除（重放
// 时丢掉预算设置），又在 launch_argv 里被替换成 ***（审计看不到真相）。「按尾词
// 判定」正好区分开「一个 token 值」与「多少个 token」这两类语义。
var launchSecretFlagWords = map[string]bool{
	"token": true, "secret": true, "password": true, "passwd": true,
	"key": true, "apikey": true, "credential": true, "credentials": true,
}

// launchSecretFlagHints 是整名子串兜底（复合写法里密钥语义不在尾词上的少数形态）。
// 只列明确携带凭据的写法，绝不放裸 "token"/"key"，否则会重演上面的误判。
var launchSecretFlagHints = []string{
	"api-key", "api_key", "apikey", "access-key", "accesskey", "secret-key",
	"authorization", "bearer",
}

// launchNonRelaunchFlags 是「记录但绝不重放」的旗标（按长名匹配）：
//   - 会话目标（session/resume/list-sessions）：重放时会与新会话 ID 冲突；
//   - 一次性输入（prompt/message/script/image）：重放等于把同一句话再发一遍；
//   - 输出形态（json/output/envelope/no-interactive/headless）：新终端要的是
//     交互 TUI，带着它们会直接退化成脚本模式；
//   - 独占文件（logfile/transcript-file/render-output-file）：两个进程写同一个
//     文件会互相截断；
//   - console-host：重放会再自启一次新 Console，形成双层窗口；
//   - web-token：敏感值（已由 hints 兜住，这里显式列出以防改名后漏网）。
var launchNonRelaunchFlags = map[string]bool{
	"session":            true,
	"resume":             true,
	"list-sessions":      true,
	"prompt":             true,
	"message":            true,
	"script":             true,
	"image":              true,
	"json":               true,
	"output":             true,
	"envelope":           true,
	"no-interactive":     true,
	"headless":           true,
	"logfile":            true,
	"transcript-file":    true,
	"render-output-file": true,
	"console-host":       true,
	"web-token":          true,
}

// launchInvocationRecord 是一次启动的规范化结果（随会话 metadata 持久化）。
type launchInvocationRecord struct {
	// Instance 是进程实例标识（pid + 启动纳秒），用于 launch_history 幂等覆盖。
	Instance string
	// Entry 是启动子命令（chat|resume|acp|exec…）；空表示默认入口 chat。
	Entry string
	// RawArgv 是脱敏后的完整调用参数（含子命令与位置参数），仅展示用。
	RawArgv []string
	// Flags 是可重放旗标 token，重建启动命令只用这一份。
	Flags []string
	// Binary 是 aicli 可执行文件路径（新终端必须用同一个二进制，避免跨版本）。
	Binary string
	// Cwd 是启动时的工作目录：会话工作区锚点由此而来，跨目录重放会漂移。
	Cwd string
	// RecordedAt 是本次启动的记录时刻。
	RecordedAt time.Time

	// flagMeta 是构造本次记录时从 cobra 拿到的旗标形态知识（布尔名表 + 短名→长名）。
	// 合并继承旗标时要靠它判断一个 token 是否吃值，以及 -m 与 --model 是同一个旗标；
	// 手工构造的记录为 nil，合并退化为「下一个 token 不以 - 开头就当值」的启发式。
	flagMeta *launchFlagMeta
}

// launchFlagMeta 是旗标分类的可序列化快照（不持有 cobra 对象，避免记录被命令树生命周期绑住）。
type launchFlagMeta struct {
	boolNames   map[string]bool
	shortToLong map[string]string
}

func newLaunchFlagMeta(index launchFlagIndex) *launchFlagMeta {
	meta := &launchFlagMeta{boolNames: map[string]bool{}, shortToLong: map[string]string{}}
	for name, kind := range index.long {
		if kind == launchFlagBool {
			meta.boolNames[name] = true
		}
	}
	for short, spec := range index.short {
		meta.shortToLong[short] = spec.Name
	}
	return meta
}

// launchInvocationCapture 是本进程的启动快照。只在 main 顶部写一次，之后全是读；
// 读写锁只为防「测试注入与并发读」交错（生产路径单写）。
var (
	launchInvocationMu   sync.RWMutex
	launchInvocationArgv []string
	// launchProcessStartedAt 与 pid 一起构成进程实例标识：同一进程内反复 sync
	// 只覆盖同一条启动记录，不会把 history 撑爆。
	launchProcessStartedAt = time.Now()
)

// SetLaunchInvocation 由 main 在最早期调用（console-host 自重启之后、配置解析
// 之前），把用户 argv 原样快照进本进程。
func SetLaunchInvocation(argv []string) {
	copied := make([]string, 0, len(argv))
	for _, token := range argv {
		copied = append(copied, token)
	}
	launchInvocationMu.Lock()
	launchInvocationArgv = copied
	launchInvocationMu.Unlock()
}

// LaunchInvocationArgv 返回快照的原始 argv 副本。
func LaunchInvocationArgv() []string {
	launchInvocationMu.RLock()
	defer launchInvocationMu.RUnlock()
	if len(launchInvocationArgv) == 0 {
		return nil
	}
	return append([]string(nil), launchInvocationArgv...)
}

func launchProcessInstanceID() string {
	return fmt.Sprintf("%d@%d", os.Getpid(), launchProcessStartedAt.UnixNano())
}

// currentLaunchBinaryAndCwd 解析「重放该用哪个 aicli、在哪个目录启动」：
// os.Executable 优先（本次调用的真实二进制），退化到 argv[0]，再退化到 PATH 名。
// 变量化是为了让测试不依赖测试二进制的真实路径与工作目录。
var currentLaunchBinaryAndCwd = func() (string, string) {
	binary := ""
	if executable, err := os.Executable(); err == nil {
		binary = strings.TrimSpace(executable)
	}
	if binary == "" && len(os.Args) > 0 {
		binary = strings.TrimSpace(os.Args[0])
	}
	if binary == "" {
		binary = "aicli"
	}
	cwd := ""
	if dir, err := os.Getwd(); err == nil {
		cwd = dir
	}
	return binary, cwd
}

// newLaunchInvocationRecord 从进程快照与已解析的 cobra 命令构造启动记录。
// 没有 argv 快照（被嵌入调用、或测试直接进 HandleChat）时返回 nil，调用方据此
// 保持既有行为：不写任何 launch 键，也绝不清空历史。
func newLaunchInvocationRecord(cmd *cobra.Command) *launchInvocationRecord {
	argv := LaunchInvocationArgv()
	if len(argv) == 0 {
		return nil
	}
	flagIndex := newLaunchFlagIndex(cmd)
	split := splitLaunchArgv(argv, flagIndex)
	binary, cwd := currentLaunchBinaryAndCwd()
	return &launchInvocationRecord{
		Instance:   launchProcessInstanceID(),
		Entry:      launchEntryOrDefault(split.Entry),
		RawArgv:    split.RedactedArgv,
		Flags:      split.Flags,
		Binary:     binary,
		Cwd:        cwd,
		RecordedAt: time.Now(),
		flagMeta:   newLaunchFlagMeta(flagIndex),
	}
}

// launchEntryOrDefault 把空入口归一到默认子命令：`aicli --yolo` 会被
// prependDefaultChatCommand 改写成 `aicli chat --yolo`，而 argv 快照里没有
// "chat" 这个词，展示时需要补回来。
func launchEntryOrDefault(entry string) string {
	if trimmed := strings.TrimSpace(entry); trimmed != "" {
		return trimmed
	}
	return "chat"
}

// launchFlagKind 表示一个旗标是否携带取值。
type launchFlagKind int

const (
	// launchFlagUnknown：未注册的 token，一律不进重放集。
	launchFlagUnknown launchFlagKind = iota
	// launchFlagBool：布尔/可选值旗标（--web、--yolo、--mesh=false）。
	launchFlagBool
	// launchFlagValue：必须吃掉后一个 token 的旗标（-m gpt-4.1）。
	launchFlagValue
)

// launchFlagSpec 是一个已注册旗标的归一信息（长名 + 分类）。
type launchFlagSpec struct {
	Name string
	Kind launchFlagKind
}

// launchFlagIndex 是 argv 分类需要的 flag 元数据索引（长名表 + 短名表）。
type launchFlagIndex struct {
	long  map[string]launchFlagKind
	short map[string]launchFlagSpec
}

func (index launchFlagIndex) classifyLong(name string) (launchFlagKind, bool) {
	kind, ok := index.long[name]
	return kind, ok
}

func (index launchFlagIndex) classifyShort(char string) (launchFlagSpec, bool) {
	spec, ok := index.short[char]
	return spec, ok
}

// newLaunchFlagIndex 从 cmd（含继承的持久旗标）与 root 持久旗标建索引。
// pflag 用 NoOptDefVal 标记布尔旗标：有默认值即「可以不带值」，也就是布尔形态。
func newLaunchFlagIndex(cmd *cobra.Command) launchFlagIndex {
	index := launchFlagIndex{long: map[string]launchFlagKind{}, short: map[string]launchFlagSpec{}}
	add := func(set *pflag.FlagSet) {
		if set == nil {
			return
		}
		set.VisitAll(func(flag *pflag.Flag) {
			if flag == nil {
				return
			}
			name := strings.TrimSpace(flag.Name)
			if name == "" {
				return
			}
			kind := launchFlagValue
			if strings.TrimSpace(flag.NoOptDefVal) != "" {
				kind = launchFlagBool
			}
			if _, exists := index.long[name]; !exists {
				index.long[name] = kind
			}
			if short := strings.TrimSpace(flag.Shorthand); short != "" {
				if _, exists := index.short[short]; !exists {
					index.short[short] = launchFlagSpec{Name: name, Kind: kind}
				}
			}
		})
	}
	if cmd != nil {
		// Flags() 在 Run 阶段已并入继承来的持久旗标；Root().PersistentFlags()
		// 再兜一层，保证只注册了少量本地旗标的命令也能认出全局旗标。
		add(cmd.Flags())
		if root := cmd.Root(); root != nil {
			add(root.PersistentFlags())
		}
	}
	return index
}

// launchArgvSplit 是 argv 分类结果。
type launchArgvSplit struct {
	// Entry 是第一个位置参数（子命令名），空表示默认入口。
	Entry string
	// Positional 是全部位置参数（含子命令）。
	Positional []string
	// Flags 是可重放的旗标 token 序列（保持用户写法）。
	Flags []string
	// RedactedArgv 是脱敏后的完整 token 序列，逐字对齐用户输入。
	RedactedArgv []string
}

// splitLaunchArgv 把原始 argv 切成「脱敏全量」与「可重放旗标」两份。
//
// 规则（一律取保守方向）：
//   - `--` 之后的 token 一律按位置参数处理，不再解释为旗标；
//   - 已注册的布尔旗标不吃下一个 token，已注册的取值旗标恰好吃一个；
//   - 未注册的 `--foo` / `-z` 只进 RedactedArgv，不进 Flags：猜错取值性会把
//     下一个旗标吞成值，重放出来的就是一条错误的命令行；
//   - 敏感旗标（launchSecretFlagHints）的值替换成 ***，且不进 Flags；
//   - 命中 launchNonRelaunchFlags 的旗标只记录不重放。
func splitLaunchArgv(argv []string, index launchFlagIndex) launchArgvSplit {
	split := launchArgvSplit{RedactedArgv: make([]string, 0, len(argv))}
	positionalOnly := false
	for i := 0; i < len(argv); i++ {
		token := argv[i]
		switch {
		case positionalOnly:
			split.appendPositional(token)
		case token == "--":
			positionalOnly = true
			split.RedactedArgv = append(split.RedactedArgv, token)
		case isLaunchFlagToken(token):
			i = applyLaunchFlagToken(argv, i, index, &split)
		default:
			split.appendPositional(token)
		}
	}
	return split
}

func (split *launchArgvSplit) appendPositional(token string) {
	split.RedactedArgv = append(split.RedactedArgv, token)
	split.Positional = append(split.Positional, token)
	if split.Entry == "" {
		split.Entry = token
	}
}

func isLaunchFlagToken(token string) bool {
	return strings.HasPrefix(token, "-") && token != "-"
}

// applyLaunchFlagToken 分派长名与短名两种写法，返回下一个待读下标。
func applyLaunchFlagToken(argv []string, i int, index launchFlagIndex, split *launchArgvSplit) int {
	if strings.HasPrefix(argv[i], "--") {
		return applyLaunchLongFlag(argv, i, index, split)
	}
	return applyLaunchShortFlag(argv, i, index, split)
}

// applyLaunchLongFlag 处理 --name / --name=value。
func applyLaunchLongFlag(argv []string, i int, index launchFlagIndex, split *launchArgvSplit) int {
	token := argv[i]
	name := strings.TrimPrefix(token, "--")
	inlineValue := ""
	hasInline := false
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		inlineValue, hasInline = name[eq+1:], true
		name = name[:eq]
	}
	kind, known := index.classifyLong(name)
	secret := launchFlagIsSecret(name)

	if hasInline {
		masked := inlineValue
		if secret {
			masked = launchRedactedValue
		}
		split.RedactedArgv = append(split.RedactedArgv, "--"+name+"="+masked)
	} else {
		split.RedactedArgv = append(split.RedactedArgv, token)
	}

	if !known {
		// 未知旗标：保留展示，但不参与重放（无法判断它是否吃值）。
		return i
	}
	relaunchable := !secret && !launchFlagIsNonRelaunchable(name)
	if kind == launchFlagBool {
		if relaunchable {
			split.Flags = append(split.Flags, token)
		}
		return i
	}
	if hasInline {
		if relaunchable {
			split.Flags = append(split.Flags, token)
		}
		return i
	}
	if i+1 >= len(argv) {
		// 悬空取值旗标：没有值可带，重放出去会被 pflag 拒绝，只记录。
		return i
	}
	value := argv[i+1]
	if secret {
		split.RedactedArgv = append(split.RedactedArgv, launchRedactedValue)
	} else {
		split.RedactedArgv = append(split.RedactedArgv, value)
	}
	if relaunchable {
		split.Flags = append(split.Flags, token, value)
	}
	return i + 1
}

// applyLaunchShortFlag 处理 -m value、-s（布尔）与组合簇 -sm / -t100。
//
// 组合簇里只要出现「未注册 / 敏感 / 不重放」任一情形，整簇就不进重放集：半簇重放
// 会改变语义（例如 -sm 只带 -s），代价远大于少带一个参数。
func applyLaunchShortFlag(argv []string, i int, index launchFlagIndex, split *launchArgvSplit) int {
	token := argv[i]
	body := strings.TrimPrefix(token, "-")
	chars := []rune(body)

	known := true
	secret := false
	blocked := false
	hasValue := false
	inline := false
	inlinePrefix := ""
	valueFromNext := false

	for position, char := range chars {
		spec, ok := index.classifyShort(string(char))
		if !ok {
			known = false
			break
		}
		if launchFlagIsSecret(spec.Name) {
			secret = true
		}
		if launchFlagIsNonRelaunchable(spec.Name) {
			blocked = true
		}
		if spec.Kind == launchFlagBool {
			continue
		}
		hasValue = true
		if position < len(chars)-1 {
			inline = true
			inlinePrefix = string(chars[:position+1])
		} else {
			valueFromNext = true
		}
		break
	}

	relaunchable := known && !secret && !blocked

	switch {
	case !hasValue:
		split.RedactedArgv = append(split.RedactedArgv, token)
		if relaunchable {
			split.Flags = append(split.Flags, token)
		}
		return i
	case inline:
		if secret {
			split.RedactedArgv = append(split.RedactedArgv, "-"+inlinePrefix+launchRedactedValue)
		} else {
			split.RedactedArgv = append(split.RedactedArgv, token)
		}
		if relaunchable {
			split.Flags = append(split.Flags, token)
		}
		return i
	case valueFromNext:
		if i+1 >= len(argv) {
			split.RedactedArgv = append(split.RedactedArgv, token)
			return i
		}
		value := argv[i+1]
		split.RedactedArgv = append(split.RedactedArgv, token)
		if secret {
			split.RedactedArgv = append(split.RedactedArgv, launchRedactedValue)
		} else {
			split.RedactedArgv = append(split.RedactedArgv, value)
		}
		if relaunchable {
			split.Flags = append(split.Flags, token, value)
		}
		return i + 1
	default:
		split.RedactedArgv = append(split.RedactedArgv, token)
		return i
	}
}

func launchFlagIsSecret(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, hint := range launchSecretFlagHints {
		if strings.Contains(name, hint) {
			return true
		}
	}
	// 尾词判定：取按 -/_ 分词后的最后一个词。
	fields := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	if len(fields) == 0 {
		return false
	}
	return launchSecretFlagWords[fields[len(fields)-1]]
}

func launchFlagIsNonRelaunchable(name string) bool {
	return launchNonRelaunchFlags[strings.ToLower(strings.TrimSpace(name))]
}

// applyLaunchInvocationToContext 把启动记录写进会话 metadata.context。
//
// 同一进程内多次 sync 只覆盖当前记录，并按 Instance 去重 history（否则每个 turn
// 都会往历史里塞一条同样的启动）。返回是否发生了写入。
func applyLaunchInvocationToContext(ctx map[string]interface{}, record *launchInvocationRecord, now time.Time) bool {
	if ctx == nil || record == nil {
		return false
	}
	if record.RecordedAt.IsZero() {
		if now.IsZero() {
			now = time.Now()
		}
		record.RecordedAt = now
	}
	sessionmeta.Set(ctx, sessionmeta.LaunchArgs, mergeLaunchFlags(
		launchStringListKey(ctx, sessionmeta.LaunchArgs), record.Flags, record.flagMeta,
	), sessionmeta.LegacyAICLILaunchArgs)
	sessionmeta.Set(ctx, sessionmeta.LaunchArgv, append([]string(nil), record.RawArgv...), sessionmeta.LegacyAICLILaunchArgv)
	sessionmeta.Set(ctx, sessionmeta.LaunchEntry, launchEntryOrDefault(record.Entry), sessionmeta.LegacyAICLILaunchEntry)
	sessionmeta.Set(ctx, sessionmeta.LaunchInstance, record.Instance, sessionmeta.LegacyAICLILaunchInstance)
	sessionmeta.Set(ctx, sessionmeta.LaunchRecordedAt, record.RecordedAt.UTC().Format(time.RFC3339Nano), sessionmeta.LegacyAICLILaunchRecordedAt)
	if text := strings.TrimSpace(record.Binary); text != "" {
		sessionmeta.Set(ctx, sessionmeta.LaunchBinary, text, sessionmeta.LegacyAICLILaunchBinary)
	}
	if text := strings.TrimSpace(record.Cwd); text != "" {
		sessionmeta.Set(ctx, sessionmeta.LaunchCwd, text, sessionmeta.LegacyAICLILaunchCwd)
	}
	sessionmeta.Set(ctx, sessionmeta.LaunchHistory, pushLaunchHistoryEntry(ctx, record, launchHistoryMaxEntries), sessionmeta.LegacyAICLILaunchHistory)
	return true
}

// pushLaunchHistoryEntry 追加一条启动历史（最新在前，同实例覆盖）。
func pushLaunchHistoryEntry(ctx map[string]interface{}, record *launchInvocationRecord, limit int) []map[string]interface{} {
	if record == nil {
		return nil
	}
	if limit <= 0 {
		limit = launchHistoryMaxEntries
	}
	entry := map[string]interface{}{
		"instance": strings.TrimSpace(record.Instance),
		"at":       record.RecordedAt.UTC().Format(time.RFC3339Nano),
		"entry":    launchEntryOrDefault(record.Entry),
		"cwd":      strings.TrimSpace(record.Cwd),
		"flags":    append([]string(nil), record.Flags...),
		"argv":     append([]string(nil), record.RawArgv...),
	}
	existing := launchHistoryEntriesFromContext(ctx)
	if len(existing) > 0 && sameLaunchInvocationInstance(existing[0], record.Instance) {
		existing = existing[1:] // 同实例重写：丢弃旧头，避免重复累积。
	}
	entries := make([]map[string]interface{}, 0, limit)
	entries = append(entries, entry)
	for _, item := range existing {
		if len(entries) >= limit || item == nil {
			continue
		}
		entries = append(entries, item)
	}
	return entries
}

func sameLaunchInvocationInstance(entry map[string]interface{}, instance string) bool {
	if entry == nil {
		return false
	}
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return false
	}
	text, _ := entry["instance"].(string)
	return strings.TrimSpace(text) == instance
}

// launchHistoryEntriesFromContext 读出启动历史（JSON 往返后是 []interface{}）。
func launchHistoryEntriesFromContext(ctx map[string]interface{}) []map[string]interface{} {
	value, ok := sessionmeta.Value(ctx, sessionmeta.LaunchHistory)
	if !ok || value == nil {
		return nil
	}
	var result []map[string]interface{}
	switch typed := value.(type) {
	case []map[string]interface{}:
		for _, item := range typed {
			if item != nil {
				result = append(result, item)
			}
		}
	case []interface{}:
		for _, item := range typed {
			record, ok := item.(map[string]interface{})
			if !ok || record == nil {
				continue
			}
			result = append(result, record)
		}
	}
	return result
}

// launchInvocationFromContext 读回一份启动记录（无记录返回 nil）。
func launchInvocationFromContext(ctx map[string]interface{}) *launchInvocationRecord {
	if len(ctx) == 0 {
		return nil
	}
	record := &launchInvocationRecord{
		Flags:    launchStringListKey(ctx, sessionmeta.LaunchArgs),
		RawArgv:  launchStringListKey(ctx, sessionmeta.LaunchArgv),
		Entry:    strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.LaunchEntry)),
		Instance: strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.LaunchInstance)),
		Binary:   strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.LaunchBinary)),
		Cwd:      strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.LaunchCwd)),
	}
	if text := strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.LaunchRecordedAt)); text != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			record.RecordedAt = parsed
		}
	}
	if len(record.Flags) == 0 && len(record.RawArgv) == 0 && record.Entry == "" && record.Cwd == "" && record.Binary == "" {
		return nil
	}
	return record
}

// launchFlagEntry 是「一个旗标 + 它的值」的最小结构：合并必须以条目为单位，
// 否则 `--model gpt-4.1` 会被拆成两个独立 token 参与去重，把值当成孤立旗标。
type launchFlagEntry struct {
	// Key 是归一后的旗标名（短名按 flagMeta 换成长名；组合短名簇整簇为一个 Key）。
	Key    string
	Tokens []string
}

// parseLaunchFlagEntries 把旗标 token 序列切成条目。
//
// 有 flagMeta 时按 cobra 的元数据判断是否吃值（`--yolo` 不吃、`-m x` 吃一个）；
// 没有元数据（手工构造的记录）才退化为「下一个 token 不以 - 开头就当值」的启发式。
// 无法识别的 token 原样成条（Key 用自身），保证合并永远不会把未知旗标丢掉。
func parseLaunchFlagEntries(tokens []string, meta *launchFlagMeta) []launchFlagEntry {
	entries := make([]launchFlagEntry, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		token := strings.TrimSpace(tokens[i])
		if token == "" {
			continue
		}
		if !strings.HasPrefix(token, "-") {
			// 落单的值 token（上游解析异常时）：挂到上一条目上，别让它单独成条。
			if len(entries) > 0 {
				entries[len(entries)-1].Tokens = append(entries[len(entries)-1].Tokens, token)
				continue
			}
			entries = append(entries, launchFlagEntry{Key: token, Tokens: []string{token}})
			continue
		}
		entry := launchFlagEntry{Key: token, Tokens: []string{token}}
		name := strings.TrimPrefix(strings.TrimPrefix(token, "-"), "-")
		hasInline := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, hasInline = name[:eq], true
		}
		switch {
		case strings.HasPrefix(token, "--"):
			entry.Key = name
			// 长名旗标：布尔或内联值都自带完整语义，否则吃下一个 token。
			if hasInline || (meta != nil && meta.boolNames[name]) {
				entries = append(entries, entry)
				continue
			}
			if meta == nil && i+1 < len(tokens) && strings.HasPrefix(strings.TrimSpace(tokens[i+1]), "-") {
				entries = append(entries, entry)
				continue
			}
		case len(name) == 1:
			if meta != nil {
				if long, ok := meta.shortToLong[name]; ok {
					entry.Key = long
					name = long
				}
			}
			if meta != nil && meta.boolNames[name] {
				entries = append(entries, entry)
				continue
			}
			if meta == nil && i+1 < len(tokens) && strings.HasPrefix(strings.TrimSpace(tokens[i+1]), "-") {
				entries = append(entries, entry)
				continue
			}
		default:
			// 组合短名簇（-sm）：整簇一个 Key；只有启发式能判断它是否还带外置值。
			if meta != nil {
				entries = append(entries, entry)
				continue
			}
		}
		if i+1 < len(tokens) {
			if value := strings.TrimSpace(tokens[i+1]); value != "" {
				entry.Tokens = append(entry.Tokens, value)
				i++
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// mergeLaunchFlags 把本次启动传入的旗标叠在会话已记录的那份上。
//
// 规则（都是为了让「重启会话」不丢参数，同时让「这次明确说了什么」说了算）：
//   - 同名旗标：新的**替换**旧的，并留在旧的位置上（列表顺序稳定，反复 sync 不漂移）；
//   - 本次没提的旗标：继承；
//   - 本次新增的旗标：追加在尾部 —— 万一短名与长名没被归一到同一个 Key（例如
//     旧记录存了 `--model x` 而本次传 `-m y`），pflag 的「后者覆盖前者」也仍然让
//     本次的值生效，不会因为去重失败而给出错的结果。
//
// 幂等性由「同名就地替换」保证：同一记录反复写入既不会增长也不会换序。
func mergeLaunchFlags(inherited, current []string, meta *launchFlagMeta) []string {
	currentEntries := parseLaunchFlagEntries(current, meta)
	inheritedEntries := parseLaunchFlagEntries(inherited, meta)
	if len(currentEntries) == 0 {
		return tokensOfLaunchEntries(inheritedEntries)
	}
	used := make(map[string]bool, len(currentEntries))
	merged := make([]launchFlagEntry, 0, len(inheritedEntries)+len(currentEntries))
	for _, entry := range inheritedEntries {
		if replacement, ok := findLaunchEntry(currentEntries, entry.Key); ok {
			if !used[entry.Key] {
				used[entry.Key] = true
				merged = append(merged, replacement)
			}
			continue
		}
		merged = append(merged, entry)
	}
	for _, entry := range currentEntries {
		if used[entry.Key] {
			continue
		}
		used[entry.Key] = true
		merged = append(merged, entry)
	}
	return tokensOfLaunchEntries(merged)
}

func findLaunchEntry(entries []launchFlagEntry, key string) (launchFlagEntry, bool) {
	for _, entry := range entries {
		if entry.Key == key {
			return entry, true
		}
	}
	return launchFlagEntry{}, false
}

func tokensOfLaunchEntries(entries []launchFlagEntry) []string {
	tokens := make([]string, 0, len(entries)*2)
	for _, entry := range entries {
		tokens = append(tokens, entry.Tokens...)
	}
	return tokens
}

// launchStringList 把一个元数据值读成规范化字符串切片。
//
// 为什么不借道「按 key 取」的通用读取器：这里既要读 context 顶层键（launch_args），
// 也要读启动历史里的**嵌套**值（entry["flags"]）。后者已经是值本身，按 key 取就得
// 先给它套一个 `{key: value}` 的假 map——那只是为了喂接口而伪造出来的结构。
//
// 存储层是 JSON 往返的：写入是 []string，读回来是 []interface{}，两种形态必须得到
// 同一结果；非字符串元素按 fmt 归一、空值跳过，空结果统一返回 nil（调用方以
// len==0 判「未记录」，不必再区分两种空）。
func launchStringList(value interface{}) []string {
	var raw []interface{}
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				result = append(result, text)
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case []interface{}:
		raw = typed
	case string:
		raw = []interface{}{typed}
	default:
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if item == nil {
			continue
		}
		text, ok := item.(string)
		if !ok {
			text = fmt.Sprintf("%v", item)
		}
		if text = strings.TrimSpace(text); text == "" || text == "<nil>" {
			continue
		}
		result = append(result, text)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// launchStringListKey 读出某个 context 键的字符串切片形态。
func launchStringListKey(ctx map[string]interface{}, key string) []string {
	value, ok := sessionmeta.Value(ctx, key)
	if !ok {
		return nil
	}
	return launchStringList(value)
}

// effectiveSessionLaunchFlags 返回「现在重放这个会话会用的旗标」：会话已记录的那份
// 叠加本次进程真正传入的那份（同名覆盖）。
//
// 为什么在读取时再合一次，而不是直接读 launch_args：写入发生在 sync 里，而退出提示
// 与收尾 sync 同处一条关闭链上，先后顺序不该改变给用户的那句命令。合并本身幂等，
// 因此无论刚没刚 sync 过，得到的都是同一份有效参数。
func effectiveSessionLaunchFlags(session *ChatSession) []string {
	if session == nil {
		return nil
	}
	stored := []string(nil)
	if session.RuntimeSession != nil {
		stored = launchStringListKey(session.RuntimeSession.Metadata.Context, sessionmeta.LaunchArgs)
	}
	if session.LaunchInvocation == nil {
		return stored
	}
	return mergeLaunchFlags(stored, session.LaunchInvocation.Flags, session.LaunchInvocation.flagMeta)
}

// launchInvocationFromRuntimeSession 从会话元数据读回启动记录（旧会话没有记录时
// 返回 nil，调用方据此退化成「不带旗标重放」而不是报错）。
func launchInvocationFromRuntimeSession(session *runtimechat.Session) *launchInvocationRecord {
	if session == nil {
		return nil
	}
	return launchInvocationFromContext(session.Metadata.Context)
}

// launchCommandForSession 返回该会话可复制的启动命令：优先用持久化好的
// launch_command（当年那条命令的原样），缺失时按 launch_args 现算一条。
func launchCommandForSession(session *runtimechat.Session, record *launchInvocationRecord) string {
	if session != nil && record != nil {
		if stored := strings.TrimSpace(sessionmeta.String(session.Metadata.Context, sessionmeta.LaunchCommand)); stored != "" {
			return stored
		}
	}
	binary := ""
	if record != nil {
		binary = record.Binary
	}
	flags := []string(nil)
	if record != nil {
		flags = record.Flags
	}
	return formatLaunchCommand(binary, strings.TrimSpace(sessionIDOfRuntimeSession(session)), flags)
}

// sessionIDOfRuntimeSession 取会话 ID（nil 安全）。
func sessionIDOfRuntimeSession(session *runtimechat.Session) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(session.ID)
}

// sessionRelaunchArgs 构造「在新终端里重新打开某个会话」的参数向量（不含程序名）。
//
// 入口恒为 resume：`aicli resume <id>` 是按 ID 打开已有会话的稳定契约（chat 需要
// --session、acp 是协议入口），而 launch_args 已剔除会话目标类旗标，不会与这里的
// ID 位置参数冲突。
func sessionRelaunchArgs(sessionID string, flags []string) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	args := make([]string, 0, len(flags)+2)
	args = append(args, "resume", sessionID)
	for _, flag := range flags {
		if flag = strings.TrimSpace(flag); flag != "" {
			args = append(args, flag)
		}
	}
	return args
}

// formatLaunchCommand 渲染一行可复制粘贴的启动命令（展示用；真正的进程启动走
// argv，由平台 launcher 负责转义，不经过这里）。
func formatLaunchCommand(binary, sessionID string, flags []string) string {
	parts := make([]string, 0, len(flags)+3)
	parts = append(parts, quoteLaunchToken(firstNonEmptyChatValue(strings.TrimSpace(binary), "aicli")))
	parts = append(parts, "resume")
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		parts = append(parts, quoteLaunchToken(sessionID))
	}
	for _, flag := range flags {
		if flag = strings.TrimSpace(flag); flag != "" {
			parts = append(parts, quoteLaunchToken(flag))
		}
	}
	return strings.Join(parts, " ")
}

// quoteLaunchToken 只在需要时加引号，保证复制出来的命令仍然可读。
func quoteLaunchToken(token string) string {
	if token == "" {
		return `""`
	}
	if !strings.ContainsAny(token, " \t\"'&|;<>") {
		return token
	}
	return `"` + strings.ReplaceAll(token, `"`, `\"`) + `"`
}
