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

// registerMeshGovernanceFlags 注册网格治理开关。默认值必须与架构 §10 的
// 表格一致：`--mesh-restrict-workspace` 默认**关闭**——工作区是筛选维度，
// 不是权限边界（§9.3），默认收敛会让跨项目协作变成「先改配置才能用」。
func registerMeshGovernanceFlags(fs *pflag.FlagSet) {
	if fs == nil {
		return
	}
	fs.Bool(meshRestrictWorkspaceFlag, false,
		"收敛：拒绝跨工作区的写调用（refused + mesh_cross_workspace_denied；只读调用不受影响；默认关闭）")
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
}
