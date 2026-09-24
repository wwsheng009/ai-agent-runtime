package main

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
)

// meshRestrictWorkspaceFlag 是跨工作区收敛开关的 flag 名（架构 §9.3 / §10，
// P2 治理项）。S8 先落判定逻辑与原因码，S10 的 M10 场景需要它验证「默认
// 放行」的反面，故开关本体随 S8 接线。
const meshRestrictWorkspaceFlag = "mesh-restrict-workspace"

// meshAllowSpawnFlag 是网格拉起开关（架构 §5.7 要点 7，P1）：默认**开启**。
// 浏览器点「在新窗口打开」本就该直接生效；关掉它是给「只读网格」的部署
// （CI / 共享机器）留一个总闸，而不是常态配置——默认关会让功能名存实亡。
const meshAllowSpawnFlag = "mesh-allow-spawn"

// meshAllowStopFlag 是网格停止开关（架构 §5.7 / §9.2，P2）：默认**关闭**。
// 与拉起相反：停止会中断别人的会话（force 还不给收尾机会），默认开等于把
// 「谁都能杀谁」变成常态；要停就得在被停的进程上显式打开。
const meshAllowStopFlag = "mesh-allow-stop"

// meshAllowNonLoopbackFlag 是跨机逃生门（架构 §9.4 / §10，P2）：默认**关闭**。
// 非回环下网格写路径（call / spawn / stop）一律拒绝；打开它只放宽这一层——
// 令牌、逐次 allow_write 与 --mesh-allow-stop 的要求都照旧（§9.4：非回环必须
// 带令牌，且写操作要额外开关）。
const meshAllowNonLoopbackFlag = "mesh-allow-nonloopback"

// meshJournalFlag 是审计开关（架构 §9.5 / §10，P2）：默认**开启**（审计价值高、
// 成本低）。关闭后不写 journal（seq 仍分配，扇入不受影响），watch 与
// gc --keep-days 相应退化，其余功能不受影响。
const meshJournalFlag = "mesh-journal"

// registerMeshGovernanceFlags 注册网格治理开关。默认值必须与架构 §10 的
// 表格一致：`--mesh-restrict-workspace` 默认**关闭**——工作区是筛选维度，
// 不是权限边界（§9.3），默认收敛会让跨项目协作变成「先改配置才能用」；
// `--mesh-allow-spawn` 默认**开启**（§5.7）；`--mesh-allow-stop` 默认
// **关闭**（§5.7 / §9.2：停止是治理动作，不是日常操作）；
// `--mesh-allow-nonloopback` 默认**关闭**（§9.4：非回环一律拒绝写路径）；
// `--mesh-journal` 默认**开启**（§9.5：审计默认开）。
func registerMeshGovernanceFlags(fs *pflag.FlagSet) {
	if fs == nil {
		return
	}
	fs.Bool(meshRestrictWorkspaceFlag, false,
		"收敛：拒绝跨工作区的写调用（refused + mesh_cross_workspace_denied；只读调用不受影响；默认关闭）")
	fs.Bool(meshAllowSpawnFlag, true,
		"允许 /web/api/mesh/spawn 在会话工作区复用或拉起节点（默认开启；关闭后该端点一律 refused + mesh_spawn_not_allowed）")
	fs.Bool(meshAllowStopFlag, false,
		"允许停止节点（默认关闭；graceful 投 /exit、force 终止进程；关闭后 /web/api/mesh/stop 该端点一律 refused + mesh_stop_not_allowed，档案也不声明 stop 能力——aicli-mesh stop 的本地编排同样拒绝）")
	fs.Bool(meshAllowNonLoopbackFlag, false,
		"允许非回环客户端进入网格写路径 /web/api/mesh/call|spawn|stop（默认关闭；开启后仍需令牌，写 op 仍需 allow_write，停止仍需 --mesh-allow-stop）")
	fs.Bool(meshJournalFlag, true,
		"写网格审计日志 mesh/journal/<node>.ndjson（默认开启；--mesh-journal=false 后失去审计与对账，watch 与 gc --keep-days 相应退化，其余功能不受影响）")
}

// applyMeshGovernanceFlags 把治理开关落到进程级状态（commands 包）。
// 只有显式传入才改变行为：默认（false）下跨工作区写调用照常放行，与
// 「工作区不是权限边界」（§9.3）一致；开启后只拦截写操作，只读跨工作区
// 仍与同工作区无差别。
func applyMeshGovernanceFlags(fs *pflag.FlagSet) {
	if fs == nil {
		return
	}
	restrict, err := fs.GetBool(meshRestrictWorkspaceFlag)
	if err != nil {
		return
	}
	commands.SetChatWebMeshRestrictWorkspace(restrict)
	if restrict {
		fmt.Fprintln(os.Stderr, "Info: mesh workspace restriction on: cross-workspace write calls will be refused (mesh_cross_workspace_denied)")
	}
	// 拉起开关（§5.7 要点 7）：默认 true，显式 --mesh-allow-spawn=false 才关闭。
	// 关掉时端点仍注册（回 refused 而不是 404），让调用方能读到原因码。
	if allowSpawn, spawnErr := fs.GetBool(meshAllowSpawnFlag); spawnErr == nil {
		commands.SetChatWebMeshAllowSpawn(allowSpawn)
		if !allowSpawn {
			fmt.Fprintln(os.Stderr, "Info: mesh spawn disabled (--mesh-allow-spawn=false): /web/api/mesh/spawn will refuse (mesh_spawn_not_allowed)")
		}
	}
	// 停止开关（§5.7 / §9.2）：默认 false，显式 --mesh-allow-stop=true 才开启。
	// 开启是一次有意的风险接受，因此回一行 Info，让日志里留下痕迹。
	if allowStop, stopErr := fs.GetBool(meshAllowStopFlag); stopErr == nil {
		commands.SetChatWebMeshAllowStop(allowStop)
		if allowStop {
			fmt.Fprintln(os.Stderr, "Info: mesh stop enabled (--mesh-allow-stop=true): /web/api/mesh/stop can stop nodes (graceful=/exit, force=terminate)")
		}
	}
	// 跨机开关（§9.4）：默认 false，显式 --mesh-allow-nonloopback=true 才放宽。
	// 开启是一次有意的风险接受，回一行 Info 留痕（与 --mesh-allow-stop 同口径）。
	if allowNonLoopback, nonLoopbackErr := fs.GetBool(meshAllowNonLoopbackFlag); nonLoopbackErr == nil {
		commands.SetChatWebMeshAllowNonLoopback(allowNonLoopback)
		if allowNonLoopback {
			fmt.Fprintln(os.Stderr, "Info: mesh non-loopback write path enabled (--mesh-allow-nonloopback=true): /web/api/mesh/call|spawn|stop accept non-loopback clients (token still required; write ops still need allow_write)")
		}
	}
	// 审计开关（§9.5）：默认 true，显式 --mesh-journal=false 才关闭。关闭后
	// journal 只分配 seq 不落盘（扇入与 SSE 的 seq 同源，不受影响），watch 退化。
	if journal, journalErr := fs.GetBool(meshJournalFlag); journalErr == nil {
		commands.SetChatWebMeshJournalEnabled(journal)
		if !journal {
			fmt.Fprintln(os.Stderr, "Info: mesh journal disabled (--mesh-journal=false): no audit trail; watch and gc --keep-days degrade")
		}
	}
}
