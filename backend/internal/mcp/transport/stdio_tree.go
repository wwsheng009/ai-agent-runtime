//go:build !win7compat

package transport

import (
	"context"
	"os"
	"os/exec"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// stdioTreeGuardTransport 给 SDK 的 CommandTransport 补上「父死子亡」兜底。
//
// 背景（计划 §6.2 验收标准 10 / §2.5 Z10）：
//   - exec.CommandContext 只会在 ctx 取消时杀**直接子进程**，父进程被强杀
//     （客户端 Drop → child.kill()，不走 session/close）时 ctx 永远不会取消，
//     stdio MCP server（以及 npx → node 这类孙进程）会成为孤儿。
//   - Windows 用 Job Object + JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE：只要本进程
//     持有 job handle，进程被强杀时操作系统会连带收掉整棵树；Unix 退化为
//     进程组（ctx 取消路径按组终止）。
//
// 复用 internal/executor 的 ProcessGuard，避免再造一套 job object 实现。
type stdioTreeGuardTransport struct {
	inner   mcp.Transport
	cmd     *exec.Cmd
	guard   *executor.ProcessGuard
	target  string
	emitter *lifecycleEmitter
}

func newStdioTreeGuardTransport(
	inner mcp.Transport,
	cmd *exec.Cmd,
	guard *executor.ProcessGuard,
	target string,
	emitter *lifecycleEmitter,
) mcp.Transport {
	if inner == nil || cmd == nil || guard == nil {
		return inner
	}
	return &stdioTreeGuardTransport{
		inner:   inner,
		cmd:     cmd,
		guard:   guard,
		target:  target,
		emitter: emitter,
	}
}

func (t *stdioTreeGuardTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		// 进程没起来：job 里没有成员，直接释放句柄即可。
		t.guard.Close()
		return nil, err
	}

	if proc := t.cmd.Process; proc != nil {
		if attachErr := t.guard.Attach(proc); attachErr != nil {
			// 非致命：拿不到 job（例如宿主已在不允许嵌套的 job 内）时退化为
			// 「ctx 取消 / Close 时按进程组或 taskkill 收树」，并把降级原因
			// 记进生命周期事件，便于诊断（不静默）。
			t.emitter.emitLifecycleEvent(TraceIDFromContext(ctx), "mcp.stdio.tree_guard_degraded", "stdio", "", map[string]interface{}{
				"target": t.target,
				"pid":    proc.Pid,
				"error":  attachErr.Error(),
			})
		} else {
			t.emitter.emitLifecycleEvent(TraceIDFromContext(ctx), "mcp.stdio.tree_guard_attached", "stdio", "", map[string]interface{}{
				"target": t.target,
				"pid":    proc.Pid,
			})
		}
	}

	return &stdioTreeGuardConnection{
		Connection: conn,
		guard:      t.guard,
		target:     t.target,
		emitter:    t.emitter,
	}, nil
}

// stdioTreeGuardConnection 在连接关闭时把整棵 stdio 进程树收干净。
type stdioTreeGuardConnection struct {
	mcp.Connection
	guard     *executor.ProcessGuard
	target    string
	emitter   *lifecycleEmitter
	closeOnce sync.Once
}

func (c *stdioTreeGuardConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// 先走 SDK 的正常关闭（关 stdin → 等退出 → 必要时杀直接子进程）。
		err = c.Connection.Close()
		// 再兜底终止整棵树：npx / uvx 这类启动器会留下孙进程。
		report := c.guard.Terminate()
		c.guard.Close()

		payload := map[string]interface{}{}
		if c.target != "" {
			payload["target"] = c.target
		}
		if report.Mode != "" {
			payload["mode"] = report.Mode
		}
		if len(report.Killed) > 0 {
			payload["killed_pids"] = report.Killed
		}
		if report.Err != "" {
			payload["error"] = report.Err
		}
		c.emitter.emitLifecycleEvent("", "mcp.stdio.tree_terminated", "stdio", "", payload)
	})
	return err
}

// treeGuardSnapshot 返回当前 stdio 进程树的根 PID 与存活后代 PID，供诊断与
// 端到端校验使用（无 job 或未启动时返回 0/nil）。
func (t *StdioTransport) treeGuardSnapshot() (int, []int) {
	if t == nil {
		return 0, nil
	}
	t.mu.Lock()
	guard := t.guard
	t.mu.Unlock()
	if guard == nil {
		return 0, nil
	}
	return guard.PID(), guard.Leftovers()
}

// treeGuardReport 返回守卫的终止报告（含 Job Object 绑定失败等降级信息）。
func (t *StdioTransport) treeGuardReport() executor.TerminationReport {
	if t == nil {
		return executor.TerminationReport{}
	}
	t.mu.Lock()
	guard := t.guard
	t.mu.Unlock()
	if guard == nil {
		return executor.TerminationReport{}
	}
	return guard.Report()
}

// stdioProcessPID 返回最近一次装配出的 stdio 命令的根进程 PID（未启动时 0）。
// 它不依赖 Job Object 绑定是否成功，因此诊断/测试可以在降级场景下照样拿到
// 真实 PID 做「是否残留」判定。
func (t *StdioTransport) stdioProcessPID() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// newStdioCommandGuard 构造 stdio 命令并绑定进程树守卫。
//
// 返回的 cleanup 用于「进程未启动」的失败路径（释放 job handle，避免句柄泄漏）。
func newStdioCommandGuard(ctx context.Context, rc stdioCommand) (*exec.Cmd, *executor.ProcessGuard, error) {
	cmd := exec.CommandContext(ctx, rc.Path, rc.Args...)
	// Windows 垫片：整条命令行由 resolveStdioCommand 拼好，必须原样下发；
	// 若交给 os/exec 拼装，参数含空格时会被 cmd.exe 截断（计划 §5.1）。
	if rc.RawCmdLineExplicit() {
		applyRawCmdLine(cmd, rc.RawCmdLine)
	}
	guard := executor.NewProcessGuard()
	if err := guard.Bind(cmd); err != nil {
		guard.Close()
		return cmd, nil, err
	}
	// ctx 取消（客户端 Close → cancel）时按整棵树终止，而不是只杀直接子进程。
	cmd.Cancel = func() error {
		if report := guard.Terminate(); report.TreeKill {
			return nil
		}
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Kill()
	}
	return cmd, guard, nil
}
