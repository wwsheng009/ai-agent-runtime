package commands

import (
	"strings"
	"testing"
)

// TestProviderRefreshArgumentDomain 锁定 `/provider refresh` 的参数候选域。
//
// 回归背景：refresh 之后曾直接沿用 /model 的 general 候选，于是
//   - `refresh` 自己、模型名、`--provider`/`--model` 都会混进 refresh 的参数位；
//   - 选好的候选会被粘住，`/provider refresh magpie` 再回车只是把 magpie（或
//     refresh）又补一次，命令永远提交不了。
func TestProviderRefreshArgumentDomain(t *testing.T) {
	t.Parallel()

	session := newSlashCompletionArgumentTestSession()
	cases := []struct {
		name     string
		argsText string
		want     []string
		wantNil  bool
	}{
		{
			name:     "第一个取值位给 provider 名与 refresh flag",
			argsText: "refresh ",
			want:     []string{"anthropic", "openai", "--all", "--dry-run", "--models-path", "--timeout"},
		},
		{
			name:     "按前缀过滤 provider",
			argsText: "refresh ope",
			want:     []string{"openai"},
		},
		{
			name:     "第二个取值位仍可补第二个 provider 名",
			argsText: "refresh openai open",
			want:     []string{"openai"},
		},
		{
			name:     "第二个取值位输入 flag 前缀只给 refresh flag",
			argsText: "refresh openai --",
			want:     []string{"--all", "--dry-run", "--models-path", "--timeout"},
		},
		{
			name:     "provider 之后的空位不再弹候选",
			argsText: "refresh openai ",
			wantNil:  true,
		},
		{
			name:     "timeout 给常用秒数",
			argsText: "refresh --timeout ",
			want:     []string{"15", "30", "60", "120"},
		},
		{
			name:     "timeout 等号形式同样给秒数",
			argsText: "refresh --timeout=1",
			want:     []string{"15", "120"},
		},
		{
			name:     "models-path 是自由文本",
			argsText: "refresh --models-path ",
			wantNil:  true,
		},
		{
			name:     "models-path 等号形式不再弹层",
			argsText: "refresh --models-path=/api/v1/models",
			wantNil:  true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			candidates := completeProviderSlashArgs(session, tc.argsText, len([]rune(tc.argsText)))
			if tc.wantNil {
				if candidates != nil {
					t.Fatalf("期望不弹候选（nil），得到 %v", completionCommandNames(candidates))
				}
				return
			}
			got := completionCommandNames(candidates)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("候选 = %v, want %v", got, tc.want)
			}
			for _, name := range got {
				if name == chatProviderRefreshToken {
					t.Fatalf("refresh 参数域里不得再出现子命令名 refresh: %v", got)
				}
				if name == "--provider" || name == "--model" || strings.HasPrefix(name, "gpt-") {
					t.Fatalf("refresh 参数域里混入了 /model 的候选: %v", got)
				}
			}
		})
	}
}

// TestProviderRefreshSubcommandNameStillCompletes 确认收紧参数域之后，二级子命令
// 名本身（以及 /provider 的通用候选）仍然可补全。
func TestProviderRefreshSubcommandNameStillCompletes(t *testing.T) {
	t.Parallel()

	session := newSlashCompletionArgumentTestSession()

	// 光标停在 refresh 这个 token 内 -> 仍走通用 provider 候选，能补出 refresh。
	if got := completionCommandNames(completeProviderSlashArgs(session, "ref", 3)); !containsStringValue(got, chatProviderRefreshToken) {
		t.Fatalf("/provider ref 仍应补出 refresh: %v", got)
	}
	// 裸 /provider 的通用候选不受影响。
	if got := completionCommandNames(completeProviderSlashArgs(session, "", 0)); !containsStringValue(got, chatProviderRefreshToken) {
		t.Fatalf("裸 /provider 候选应包含 refresh: %v", got)
	}
	// /model 不受影响。
	if got := completionCommandNames(completeModelSlashArgs(session, "ope", 3)); !containsStringValue(got, "openai") {
		t.Fatalf("/model 的 provider 取值域应保持可用: %v", got)
	}
}

// TestProviderRefreshSelectedProviderSubmitsOnEnter 复现用户报告的场景：
// 输入/选中 provider 之后回车应当提交命令，而不是继续把 provider 名补一遍。
func TestProviderRefreshSelectedProviderSubmitsOnEnter(t *testing.T) {
	t.Parallel()

	session := newSlashCompletionArgumentTestSession()
	controller := newChatSlashCompletionController(session)

	text := "/provider refresh ope"
	controller.UpdateAt(text, len([]rune(text)))
	if !controller.state.Active {
		t.Fatalf("期望 provider 前缀有候选，得到 %#v", controller.state)
	}
	selected := controller.state.Candidates[controller.state.Selected].Command
	if selected != "openai" {
		t.Fatalf("选中项应落在 openai，而不是 %q（旧行为会粘住 refresh）", selected)
	}

	text, _, ok := controller.ApplyCompletion(text, len([]rune(text)))
	if !ok {
		t.Fatal("期望 Tab 接受 provider 补全")
	}
	if text != "/provider refresh openai" {
		t.Fatalf("provider 名是终值，不应追加空格，得到 %q", text)
	}

	next, cursor, consumed := controller.ApplySubmission(text, len([]rune(text)))
	if consumed {
		t.Fatalf("选完 provider 后回车必须提交命令，而不是被补全吞掉：%q", next)
	}
	if next != text || cursor != len([]rune(text)) {
		t.Fatalf("回车提交时不应改写输入，得到 %q/%d", next, cursor)
	}
	if strings.Count(next, "openai") != 1 {
		t.Fatalf("provider 名被重复补全：%q", next)
	}
}

// TestProviderRefreshEnterAtFinishedArgumentDoesNotReinsert 锁定「已经选完
// provider 之后再回车」这一步：后续空参数位不弹候选，回车直接提交。
func TestProviderRefreshEnterAtFinishedArgumentDoesNotReinsert(t *testing.T) {
	t.Parallel()

	session := newSlashCompletionArgumentTestSession()
	controller := newChatSlashCompletionController(session)

	for _, text := range []string{
		"/provider refresh openai ",
		"/provider refresh openai --all ",
	} {
		controller.UpdateAt(text, len([]rune(text)))
		if controller.state.Active {
			t.Fatalf("finished 参数位不应再弹候选（否则回车会继续补 provider/flag）：%q -> %v",
				text, completionCommandNames(controller.state.Candidates))
		}
		next, cursor, consumed := controller.ApplySubmission(text, len([]rune(text)))
		if consumed {
			t.Fatalf("%q 的回车应提交命令，实际被补全消费为 %q", text, next)
		}
		if next != text || cursor != len([]rune(text)) {
			t.Fatalf("%q 回车提交不应改写输入，得到 %q/%d", text, next, cursor)
		}
	}
}

// TestProviderRefreshFinalFlagSubmitsOnEnter 确认 flag 终值（--all/--dry-run）
// 在精确命中时同样让回车落到提交路径。
func TestProviderRefreshFinalFlagSubmitsOnEnter(t *testing.T) {
	t.Parallel()

	session := newSlashCompletionArgumentTestSession()
	controller := newChatSlashCompletionController(session)

	for _, text := range []string{
		"/provider refresh openai --all",
		"/provider refresh openai --dry-run",
	} {
		controller.UpdateAt(text, len([]rune(text)))
		if !controller.state.Active {
			t.Fatalf("%q 应有 flag 候选", text)
		}
		next, _, consumed := controller.ApplySubmission(text, len([]rune(text)))
		if consumed {
			t.Fatalf("%q 的终值 flag 不应再消费回车，得到 %q", text, next)
		}
		if next != text {
			t.Fatalf("%q 回车提交不应改写输入，得到 %q", text, next)
		}
	}
}

func containsStringValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
