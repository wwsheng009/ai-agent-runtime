package profile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// 本文件是"内置模板 ↔ 参考样例同源"的一致性测试（实施方案 Batch 2）：
//
//  1. 每个内置模板渲染出来的 profile 必须可解析、可校验、可解析出 agent，
//     并且能被 portable agentdef 读取（同一个 agent.yaml 有两个消费者）。
//  2. examples/profiles/* 必须与当前 schema 一致：嵌套 profile.name /
//     profile.default_agent、无 error 级校验问题、每个 agent 目录都能解析。
//
// 这两条正是历史上漂移过的位置：扁平 profile.yaml（default_agent 静默失效）
// 与 agent.yaml 里的 portable tools 列表（profile.AgentSpec 解析失败）。

func TestBuiltinTemplatesRenderAndValidate(t *testing.T) {
	names := profilesys.TemplateNames()
	if len(names) == 0 {
		t.Fatal("内置模板为空")
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			profileName := "sample-" + name
			files, err := profilesys.RenderTemplate(name, profileName, "")
			if err != nil {
				t.Fatalf("RenderTemplate(%s): %v", name, err)
			}
			root := t.TempDir()
			for rel, content := range files {
				target := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, content, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			spec, err := profilesys.LoadProfile(root)
			if err != nil {
				t.Fatalf("LoadProfile: %v", err)
			}
			if got := strings.TrimSpace(spec.Profile.Name); got != profileName {
				t.Fatalf("profile.name = %q, want %q", got, profileName)
			}
			if got := strings.TrimSpace(spec.Profile.DefaultAgent); got != profilesys.TemplateDefaultAgent {
				t.Fatalf("profile.default_agent = %q, want %q", got, profilesys.TemplateDefaultAgent)
			}
			for _, issue := range profilesys.ValidateProfileSpec(spec) {
				if issue.Severity == profilesys.ProfileSpecIssueError {
					t.Fatalf("模板 %s 声明错误：%s: %s", name, issue.Path, issue.Message)
				}
			}

			resolved, err := profilesys.Resolve(profilesys.ResolveOptions{Root: root})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if resolved.AgentID != profilesys.TemplateDefaultAgent {
				t.Fatalf("AgentID = %q, want %q", resolved.AgentID, profilesys.TemplateDefaultAgent)
			}
			if _, err := agentdef.AdaptProfileAgent(root, resolved.AgentID, resolved.Paths.AgentConfigFile); err != nil {
				t.Fatalf("portable agentdef 无法读取模板 agent.yaml：%v", err)
			}
		})
	}
}

func TestExampleProfilesMatchCurrentSchema(t *testing.T) {
	root := filepath.Join("..", "..", "..", "examples", "profiles")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("examples/profiles 不存在")
		}
		t.Fatalf("read examples/profiles: %v", err)
	}

	checked := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		checked++
		name := entry.Name()
		profileRoot := filepath.Join(root, name)

		spec, err := profilesys.LoadProfile(profileRoot)
		if err != nil {
			t.Fatalf("%s: LoadProfile: %v", name, err)
		}
		if strings.TrimSpace(spec.Profile.Name) == "" {
			t.Fatalf("%s: profile.yaml 缺少嵌套 profile.name（旧的扁平格式会让 default_agent 静默失效）", name)
		}
		if strings.TrimSpace(spec.Profile.DefaultAgent) == "" {
			t.Fatalf("%s: profile.yaml 缺少 profile.default_agent", name)
		}
		for _, issue := range profilesys.ValidateProfileSpec(spec) {
			if issue.Severity == profilesys.ProfileSpecIssueError {
				t.Fatalf("%s: 声明错误 %s: %s", name, issue.Path, issue.Message)
			}
		}

		resolved, err := profilesys.Resolve(profilesys.ResolveOptions{Root: profileRoot})
		if err != nil {
			t.Fatalf("%s: Resolve: %v", name, err)
		}
		if _, err := agentdef.AdaptProfileAgent(profileRoot, resolved.AgentID, resolved.Paths.AgentConfigFile); err != nil {
			t.Fatalf("%s: portable agentdef 无法读取 agents/%s/agent.yaml：%v", name, resolved.AgentID, err)
		}

		agentDirs, err := os.ReadDir(filepath.Join(profileRoot, "agents"))
		if err != nil {
			continue
		}
		for _, dir := range agentDirs {
			if !dir.IsDir() || dir.Name() == resolved.AgentID {
				continue
			}
			agentID := dir.Name()
			one, err := profilesys.Resolve(profilesys.ResolveOptions{Root: profileRoot, Agent: agentID})
			if err != nil {
				t.Fatalf("%s: Resolve(agent=%s): %v", name, agentID, err)
			}
			if _, err := agentdef.AdaptProfileAgent(profileRoot, agentID, one.Paths.AgentConfigFile); err != nil {
				t.Fatalf("%s: portable agentdef 无法读取 agents/%s/agent.yaml：%v", name, agentID, err)
			}
		}
	}
	if checked == 0 {
		t.Skip("examples/profiles 下没有样例")
	}
}
