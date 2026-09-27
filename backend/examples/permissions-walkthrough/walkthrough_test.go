package permissionswalkthrough

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// 本文件把 docs/aicli/permissions.md 的规则/模式/硬门变成一张可执行决策表：
// 用产品同款装载路径（LoadPermissionsFile → BuildPermissionsOverlay）读取本目录的
// .aicli/permissions.yaml，交给真实的 internal/policy.Engine 求值，逐条断言
// Decision.Type / Stage / Reason。运行（在 backend/ 模块根）：
//
//	go test ./examples/permissions-walkthrough/ -count=1 -v
//
// 断言 Stage 的原因：同为 allow，可能来自 rules，也可能来自只读快车道；
// Stage 才是「这条 yaml 规则真的命中了」的证据（阶段表见手册 §3.1）。

const examplePermissionsPath = ".aicli/permissions.yaml"

// recordingApprovalHandler 记录每次审批请求并返回固定应答：用来区分
// 「规则/模式放行」与「询问后批准」——两者最终 Type 都是 allow，Stage 不同。
type recordingApprovalHandler struct {
	calls    int
	requests []policy.ApprovalRequest
	response policy.ApprovalResponse
}

func (h *recordingApprovalHandler) RequestApproval(_ context.Context, req policy.ApprovalRequest) (policy.ApprovalResponse, error) {
	h.calls++
	h.requests = append(h.requests, req)
	return h.response, nil
}

// loadExampleOverlay 走 CLI 同款装载路径：LoadPermissionsFile 之后
// BuildPermissionsOverlay（示例没有 CLI --allow-tool/--deny-tool 硬名单）。
// 注意手册 §6.10：权限文件装载失败会整文件丢弃，所以这里必须报错而非跳过。
func loadExampleOverlay(t *testing.T) policy.PermissionsOverlay {
	t.Helper()
	file, err := policy.LoadPermissionsFile(examplePermissionsPath)
	require.NoError(t, err, "示例权限文件必须可装载")
	require.NotNil(t, file, "测试工作目录必须是示例工程根，才能读到 .aicli/permissions.yaml")
	require.Equal(t, 1, file.Version, "示例使用 version: 1")
	require.True(t, file.DisableBypass, "示例启用 disable_bypass（手册 §1.3）")
	overlay := policy.BuildPermissionsOverlay(file, nil, nil)
	require.NotEmpty(t, overlay.Rules, "示例文件必须产生可求值的规则")
	return overlay
}

// buildEngine 构建真实引擎，并把 overlay 的规则与 disable_bypass 按宿主的
// 接线方式落到引擎上（§1.3：宿主必须同时用 overlay.DisableBypass 设置引擎）。
// applyDisableBypass=false 只用于对照实验，证明 bypass 行为差异确实来自
// 权限文件的 disable_bypass 字段，而不是引擎的其它默认值。
func buildEngine(t *testing.T, handler policy.ApprovalHandler, applyDisableBypass bool) *policy.Engine {
	t.Helper()
	overlay := loadExampleOverlay(t)
	engine := &policy.Engine{
		Mode:       policy.ModeDefault,
		AskHandler: handler,
		Policy:     policy.ApplyPermissionsOverlayToPolicy(policy.NewToolExecutionPolicy(nil, false), overlay),
	}
	if applyDisableBypass {
		engine.DisableBypass = overlay.DisableBypass
	}
	policy.ApplyPermissionsOverlayToEngine(engine, overlay)
	return engine
}

// TestWalkthroughDecisionTable 是示例工程的自验证主体：每一行 = 一条示例规则
// （或一条模式语义）在真实引擎上的期望结果，注释指回手册章节；任何一条与
// 引擎实际行为不符都会让测试变红，README 的示例对照表与它同源。
func TestWalkthroughDecisionTable(t *testing.T) {
	root := t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), root)

	type decisionCase struct {
		name                 string
		mode                 policy.Mode
		tool                 string
		args                 map[string]interface{}
		wantType             policy.DecisionType
		wantStage            string
		wantReasonIn         string
		wantApprovalCalls    int
		wantApprovalReasonIn string
	}

	cases := []decisionCase{
		// ── allow 命中（rules 阶段直接放行，不触发审批）──────────────────
		{
			// Shell(git status:*) 是「前缀 + 参数」形态：命中 git status 本身与
			// 任意后续参数（§2.2）；allow 在 rules 阶段定案（§3.1 第 5 阶）。
			name:         "allow_shell_git_status",
			tool:         "shell",
			args:         map[string]interface{}{"command": "git status"},
			wantType:     policy.DecisionAllow,
			wantStage:    policy.StageRules,
			wantReasonIn: "rules:allow_git_readonly",
		},
		{
			// 复合命令按 &&/;/| 拆段，allow 要求每一段都命中【同一个 specifier】
			// （§2.2 / §6.5）：Shell(git diff:*) 同时覆盖两段。
			name:         "allow_shell_compound_all_segments",
			tool:         "shell",
			args:         map[string]interface{}{"command": "git diff --stat && git diff --cached"},
			wantType:     policy.DecisionAllow,
			wantStage:    policy.StageRules,
			wantReasonIn: "rules:allow_git_readonly",
		},
		{
			// 实现边界（与手册 §4.1 的表述不一致，见 README「已知差异」）：
			// allow 的「每段都命中」只在单个 specifier 内判定，跨 specifier
			// （Shell(git status:*) 覆盖第一段、Shell(git diff:*) 覆盖第二段）
			// 拼不出复合放行——Stage=readonly_auto 证明 rules 未命中；
			// 该命令整体只读，所以仍被 §3.1 第 7 阶只读命令表放行。
			name:         "compound_across_specifiers_not_covered_by_allow",
			tool:         "shell",
			args:         map[string]interface{}{"command": "git status --short && git diff --stat"},
			wantType:     policy.DecisionAllow,
			wantStage:    policy.StageReadonlyAuto,
			wantReasonIn: "readonly_auto:shell_readonly",
		},
		{
			// Edit(src/**) 的相对路径锚定工作区根，** 跨层级（§2.3）；
			// 命中后 rules 阶段放行（没有这条规则时 default 对写文件是询问，§1.1）。
			name:         "allow_edit_src",
			tool:         "edit",
			args:         map[string]interface{}{"file_path": "src/main.go"},
			wantType:     policy.DecisionAllow,
			wantStage:    policy.StageRules,
			wantReasonIn: "rules:allow_src_edits",
		},
		{
			// WebFetch(domain:...) 只匹配 URL host，精确主机 docs.example.com（§2.4）。
			// fetch 不命中规则时会掉进只读快车道（Stage=readonly_auto），
			// 因此断言 Stage=rules 才说明这条示例规则真的命中。
			name:         "allow_fetch_docs_domain",
			tool:         "fetch",
			args:         map[string]interface{}{"url": "https://docs.example.com/guide"},
			wantType:     policy.DecisionAllow,
			wantStage:    policy.StageRules,
			wantReasonIn: "rules:allow_docs_site",
		},
		{
			// 同一条 WebFetch 规则也覆盖 download（fetch 组，§2.4）；download 会写盘、
			// 不在只读 taxonomy 里，所以这里 allow 只能来自示例规则。
			name:         "allow_download_docs_domain",
			tool:         "download",
			args:         map[string]interface{}{"url": "https://docs.example.com/data.csv"},
			wantType:     policy.DecisionAllow,
			wantStage:    policy.StageRules,
			wantReasonIn: "rules:allow_docs_site",
		},

		// ── ask 命中：rules 阶段的 ask 进入审批（AskHandler 路径）────────
		{
			// Shell(git push:*) 命中后询问（§4.2）；宿主批准 → Type=allow、
			// Stage=ask、Reason=ask:approved（§3.3 审批阶段）。审批请求里的
			// reason 保留规则命中信息 rules:review_git_push。
			name:                 "ask_shell_git_push_via_handler",
			tool:                 "shell",
			args:                 map[string]interface{}{"command": "git push origin main"},
			wantType:             policy.DecisionAllow,
			wantStage:            policy.StageAsk,
			wantReasonIn:         "ask:approved",
			wantApprovalCalls:    1,
			wantApprovalReasonIn: "rules:review_git_push",
		},
		{
			// Read(.env) 相对形态命中任意深度的同名文件（§2.3）；ask 规则先于
			// 只读快车道定案（§4.4：密钥读取显式化，不依赖默认行为）。
			name:                 "ask_read_env_via_handler",
			tool:                 "view",
			args:                 map[string]interface{}{"file_path": ".env"},
			wantType:             policy.DecisionAllow,
			wantStage:            policy.StageAsk,
			wantReasonIn:         "ask:approved",
			wantApprovalCalls:    1,
			wantApprovalReasonIn: "rules:review_secret_reads",
		},
		{
			// Read(**/*.pem) 的 ** 跨目录：certs/server.pem 命中（§2.3 / §4.4）。
			name:                 "ask_read_pem_deep_path_via_handler",
			tool:                 "view",
			args:                 map[string]interface{}{"file_path": "certs/server.pem"},
			wantType:             policy.DecisionAllow,
			wantStage:            policy.StageAsk,
			wantReasonIn:         "ask:approved",
			wantApprovalCalls:    1,
			wantApprovalReasonIn: "rules:review_secret_reads",
		},

		// ── deny 命中：rules 阶段立即返回，不进入审批 ────────────────────
		{
			// Edit(dist/**) deny 在 rules 阶段立即拒绝（§3.1 第 5 阶 / §4.3）。
			name:         "deny_edit_dist",
			tool:         "edit",
			args:         map[string]interface{}{"file_path": "dist/app.js"},
			wantType:     policy.DecisionDeny,
			wantStage:    policy.StageRules,
			wantReasonIn: "rules:protect_generated",
		},
		{
			// Edit(.git/**) 保护 VCS 元数据（§4.4）；显式 deny 规则是
			// bypass 也越不过的收紧手段（§1.1 末行）。
			name:         "deny_edit_git_metadata",
			tool:         "edit",
			args:         map[string]interface{}{"file_path": ".git/config"},
			wantType:     policy.DecisionDeny,
			wantStage:    policy.StageRules,
			wantReasonIn: "rules:protect_generated",
		},

		// ── specifier 不匹配 → 回落模式默认（default = 询问）────────────
		{
			// Edit(src/**) 只覆盖 src/ 子树；lib/util.go 不命中 → default 对写文件
			// 询问（§1.1 写文件行 / §2.3 锚点语义）。
			name:                 "fallback_edit_outside_src_asks",
			tool:                 "edit",
			args:                 map[string]interface{}{"file_path": "lib/util.go"},
			wantType:             policy.DecisionAllow,
			wantStage:            policy.StageAsk,
			wantReasonIn:         "ask:approved",
			wantApprovalCalls:    1,
			wantApprovalReasonIn: "permission_mode_requires_approval",
		},
		{
			// 未被 allow 覆盖的 git 子命令仍按模式询问（§4.1）：checkout 不在
			// Shell(git status:*)/Shell(git diff:*) 里，default 对非只读 shell 询问。
			name:                 "fallback_shell_uncovered_subcommand_asks",
			tool:                 "shell",
			args:                 map[string]interface{}{"command": "git checkout main"},
			wantType:             policy.DecisionAllow,
			wantStage:            policy.StageAsk,
			wantReasonIn:         "ask:approved",
			wantApprovalCalls:    1,
			wantApprovalReasonIn: "permission_mode_requires_approval",
		},
		{
			// 精确主机规则不含其它域（apex 也不命中，§2.4）；download 会写盘、
			// 不在只读 taxonomy，因此回落 default 询问而不是只读快车道。
			name:                 "fallback_download_other_host_asks",
			tool:                 "download",
			args:                 map[string]interface{}{"url": "https://example.com/data.csv"},
			wantType:             policy.DecisionAllow,
			wantStage:            policy.StageAsk,
			wantReasonIn:         "ask:approved",
			wantApprovalCalls:    1,
			wantApprovalReasonIn: "permission_mode_requires_approval",
		},
		{
			// 已知差异：fetch 在引擎 taxonomy 里是 ReadOnly（internal/policy/taxonomy.go），
			// 规则未命中时会走 §3.1 第 7 阶只读快车道直接放行；手册 §1.1 表把「网络」
			// 写成 default 下询问，实测对 fetch 不成立（详见 README「已知差异」一节）。
			name:         "fallback_fetch_other_host_readonly_lane",
			tool:         "fetch",
			args:         map[string]interface{}{"url": "https://example.com/guide"},
			wantType:     policy.DecisionAllow,
			wantStage:    policy.StageReadonlyAuto,
			wantReasonIn: "taxonomy_readonly:fetch",
		},

		// ── disable_bypass（§1.3）：bypass 请求被降级为 default ────────
		{
			// 示例文件写了 disable_bypass: true；以 bypass 模式请求普通写入时，
			// 引擎先把它降级为 default（§1.3）：该问的照问，不会静默放行。
			name:                 "bypass_downgraded_to_default_asks",
			mode:                 policy.ModeBypassPermissions,
			tool:                 "write",
			args:                 map[string]interface{}{"file_path": "notes/todo.md"},
			wantType:             policy.DecisionAllow,
			wantStage:            policy.StageAsk,
			wantReasonIn:         "ask:approved",
			wantApprovalCalls:    1,
			wantApprovalReasonIn: "permission_mode_requires_approval",
		},
		{
			// dont_ask 的 fail-closed 语义不受 disable_bypass 影响（§1.3 / §6.6）：
			// 该问的变成拒绝，reason=mode:dont_ask_denies_unapproved（§1.1 / §4.5）。
			name:         "dont_ask_denies_unapproved_write",
			mode:         policy.ModeDontAsk,
			tool:         "write",
			args:         map[string]interface{}{"file_path": "notes/todo.md"},
			wantType:     policy.DecisionDeny,
			wantStage:    policy.StageMode,
			wantReasonIn: "dont_ask_denies_unapproved",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := &recordingApprovalHandler{response: policy.ApprovalResponse{Allowed: true}}
			engine := buildEngine(t, handler, true)
			mode := tc.mode
			if mode == "" {
				mode = policy.ModeDefault
			}
			decision, err := engine.Evaluate(ctx, policy.EvalRequest{
				ToolName: tc.tool,
				Args:     tc.args,
				Mode:     mode,
			})
			require.NoError(t, err)
			// 为什么是这个结果：见本用例上方注释与手册对应章节。
			assert.Equal(t, tc.wantType, decision.Type, "reason=%s stage=%s", decision.Reason, decision.Stage)
			assert.Equal(t, tc.wantStage, decision.Stage, "reason=%s", decision.Reason)
			assert.Contains(t, decision.Reason, tc.wantReasonIn)
			assert.Equal(t, tc.wantApprovalCalls, handler.calls, "审批次数不符（reason=%s stage=%s）", decision.Reason, decision.Stage)
			if tc.wantApprovalReasonIn != "" {
				require.NotEmpty(t, handler.requests, "期望发生一次审批，但 AskHandler 未被调用")
				assert.Contains(t, handler.requests[0].Reason, tc.wantApprovalReasonIn)
			}
		})
	}
}

// TestDisableBypassEnforced 用对照实验证明 disable_bypass 的语义（§1.3）：
// 同一份规则，不开 disable_bypass 时 bypass 直接放行；开了之后必须走审批
// （headless 场景直接拒绝）。差异只能来自 disable_bypass 字段。
func TestDisableBypassEnforced(t *testing.T) {
	ctx := toolctx.WithWorkspaceRoot(context.Background(), t.TempDir())
	writeReq := policy.EvalRequest{
		ToolName: "write",
		Args:     map[string]interface{}{"file_path": "notes/todo.md"},
		Mode:     policy.ModeBypassPermissions,
	}

	t.Run("example_policy_asks_instead_of_allow", func(t *testing.T) {
		handler := &recordingApprovalHandler{response: policy.ApprovalResponse{Allowed: true}}
		engine := buildEngine(t, handler, true)
		overlay := loadExampleOverlay(t)
		require.True(t, overlay.DisableBypass)
		require.True(t, engine.DisableBypass, "宿主必须把 overlay.DisableBypass 落到引擎（§1.3）")
		decision, err := engine.Evaluate(ctx, writeReq)
		require.NoError(t, err)
		assert.Equal(t, policy.DecisionAllow, decision.Type)
		assert.Equal(t, policy.StageAsk, decision.Stage, "disable_bypass 必须把 bypass 降级为 default 审批")
		assert.Equal(t, 1, handler.calls)
		require.Len(t, handler.requests, 1)
		assert.Contains(t, handler.requests[0].Reason, "permission_mode_requires_approval")
	})

	t.Run("example_policy_headless_denies", func(t *testing.T) {
		// headless（无 AskHandler）时 disable_bypass 让 bypass 请求按 default
		// fail closed：approval_required / headless_deny（§1.3 / §3.3）。
		engine := buildEngine(t, nil, true)
		decision, err := engine.Evaluate(ctx, writeReq)
		require.NoError(t, err)
		assert.Equal(t, policy.DecisionDeny, decision.Type)
		assert.Equal(t, policy.StageHeadlessDeny, decision.Stage)
		assert.Contains(t, decision.Reason, "approval_required")
	})

	t.Run("control_without_disable_bypass_allows", func(t *testing.T) {
		// 对照组：同样的规则、显式不启用 disable_bypass → bypass 直接放行，
		// reason=mode:bypass_permissions（§1.1）。
		handler := &recordingApprovalHandler{response: policy.ApprovalResponse{Allowed: true}}
		engine := buildEngine(t, handler, false)
		decision, err := engine.Evaluate(ctx, writeReq)
		require.NoError(t, err)
		assert.Equal(t, policy.DecisionAllow, decision.Type)
		assert.Equal(t, policy.StageMode, decision.Stage)
		assert.Contains(t, decision.Reason, "bypass_permissions")
		assert.Equal(t, 0, handler.calls)
	})
}

// TestHardGatesFromTheEngine 覆盖两条不写在 yaml 里的引擎硬门（手册 §3.2）：
//
//   - 根/主目录断路器：HardAsk，连 bypass 也要人工确认，plan/dont_ask 直接拒绝；
//   - 敏感写保护：default/accept_edits 询问、dont_ask 拒绝，但 raw bypass 会跳过它。
//
// 这正是示例文件写 disable_bypass 的原因：降级 default 后敏感写门重新生效；
// 要连 bypass 也硬保证，请用 deny 规则或 hook block（§4.2 注意）。
func TestHardGatesFromTheEngine(t *testing.T) {
	ctx := toolctx.WithWorkspaceRoot(context.Background(), t.TempDir())

	t.Run("breaker_asks_even_under_bypass", func(t *testing.T) {
		handler := &recordingApprovalHandler{response: policy.ApprovalResponse{Allowed: true}}
		engine := buildEngine(t, handler, false) // 真 bypass（无 disable_bypass 降级）
		decision, err := engine.Evaluate(ctx, policy.EvalRequest{
			ToolName: "shell",
			Args:     map[string]interface{}{"command": "rm -rf /"},
			Mode:     policy.ModeBypassPermissions,
		})
		require.NoError(t, err)
		// HardAsk：bypass 不能自行放行，必须有人批准（§3.1 第 4 阶 / §3.2 第 1 条）。
		require.Equal(t, 1, handler.calls)
		require.Len(t, handler.requests, 1)
		assert.Contains(t, handler.requests[0].Reason, "shell_breaker:root_home_removal")
		assert.Equal(t, policy.DecisionAllow, decision.Type)
		assert.Equal(t, policy.StageAsk, decision.Stage)
	})

	t.Run("breaker_denied_under_dont_ask", func(t *testing.T) {
		// dont_ask 对断路器直接拒绝，Stage=shell_breaker（§3.2 第 1 条 / §4.5）。
		engine := buildEngine(t, nil, true)
		decision, err := engine.Evaluate(ctx, policy.EvalRequest{
			ToolName: "shell",
			Args:     map[string]interface{}{"command": "rm -rf /"},
			Mode:     policy.ModeDontAsk,
		})
		require.NoError(t, err)
		assert.Equal(t, policy.DecisionDeny, decision.Type)
		assert.Equal(t, policy.StageShellBreaker, decision.Stage)
		assert.Contains(t, decision.Reason, "shell_breaker:root_home_removal")
	})

	t.Run("sensitive_write_asks_in_default", func(t *testing.T) {
		handler := &recordingApprovalHandler{response: policy.ApprovalResponse{Allowed: true}}
		engine := buildEngine(t, handler, true)
		decision, err := engine.Evaluate(ctx, policy.EvalRequest{
			ToolName: "write",
			Args:     map[string]interface{}{"file_path": ".env"},
		})
		require.NoError(t, err)
		// default 下敏感写走审批而不是快车道（§3.2 第 2 条 / §4.4）。
		require.Equal(t, 1, handler.calls)
		assert.Contains(t, handler.requests[0].Reason, "sensitive_write:secret")
		assert.Equal(t, policy.DecisionAllow, decision.Type)
		assert.Equal(t, policy.StageAsk, decision.Stage)
	})

	t.Run("sensitive_write_denied_under_dont_ask", func(t *testing.T) {
		engine := buildEngine(t, nil, true)
		decision, err := engine.Evaluate(ctx, policy.EvalRequest{
			ToolName: "write",
			Args:     map[string]interface{}{"file_path": ".env"},
			Mode:     policy.ModeDontAsk,
		})
		require.NoError(t, err)
		assert.Equal(t, policy.DecisionDeny, decision.Type)
		assert.Equal(t, policy.StageSensitiveWrite, decision.Stage)
		assert.Contains(t, decision.Reason, "sensitive_write:secret")
	})

	t.Run("sensitive_write_is_skipped_by_raw_bypass", func(t *testing.T) {
		// 边界（§0.1 / §3.2 第 2 条）：bypass 会跳过敏感写门，直接按模式放行。
		handler := &recordingApprovalHandler{response: policy.ApprovalResponse{Allowed: true}}
		engine := buildEngine(t, handler, false)
		decision, err := engine.Evaluate(ctx, policy.EvalRequest{
			ToolName: "write",
			Args:     map[string]interface{}{"file_path": ".env"},
			Mode:     policy.ModeBypassPermissions,
		})
		require.NoError(t, err)
		assert.Equal(t, policy.DecisionAllow, decision.Type)
		assert.Equal(t, policy.StageMode, decision.Stage)
		assert.Contains(t, decision.Reason, "bypass_permissions")
		assert.Equal(t, 0, handler.calls)
	})

	t.Run("sensitive_write_gate_returns_after_disable_bypass_downgrade", func(t *testing.T) {
		// 示例策略（disable_bypass:true）下同样的 bypass 请求被降级 default，
		// 敏感写门重新生效 → 询问（§1.3 + §3.2 第 2 条）。
		handler := &recordingApprovalHandler{response: policy.ApprovalResponse{Allowed: true}}
		engine := buildEngine(t, handler, true)
		decision, err := engine.Evaluate(ctx, policy.EvalRequest{
			ToolName: "write",
			Args:     map[string]interface{}{"file_path": ".env"},
			Mode:     policy.ModeBypassPermissions,
		})
		require.NoError(t, err)
		require.Equal(t, 1, handler.calls)
		assert.Contains(t, handler.requests[0].Reason, "sensitive_write:secret")
		assert.Equal(t, policy.DecisionAllow, decision.Type)
		assert.Equal(t, policy.StageAsk, decision.Stage)
	})
}
