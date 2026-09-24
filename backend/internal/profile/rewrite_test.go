package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRewriteFixture(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "profile.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("写 fixture 失败：%v", err)
	}
	return root
}

func readRewriteFixture(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "profile.yaml"))
	if err != nil {
		t.Fatalf("读 fixture 失败：%v", err)
	}
	return string(raw)
}

// D34：只替换 name 值，注释/字段顺序/其它行逐字节不变。
func TestRewriteProfileNameKeepsCommentsAndOrder(t *testing.T) {
	content := "# 顶部注释\nprofile:\n  name: old-name\n  description: 保留我  # 行尾注释\n\nagents:\n  default:\n    name: agent-name\n"
	root := writeRewriteFixture(t, content)

	changed, err := RewriteProfileName(root, "new-name")
	if err != nil {
		t.Fatalf("RewriteProfileName: %v", err)
	}
	if !changed {
		t.Fatal("应报告已改写")
	}
	got := readRewriteFixture(t, root)
	want := strings.Replace(content, "name: old-name", "name: new-name", 1)
	if got != want {
		t.Fatalf("改写结果不符合预期\n got: %q\nwant: %q", got, want)
	}
}

// 引号风格保留：双引号、单引号都不能被吃掉。
func TestRewriteProfileNamePreservesQuoteStyle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "double",
			content: "profile:\n  name: \"old\"\n",
			want:    "profile:\n  name: \"new\"\n",
		},
		{
			name:    "single",
			content: "profile:\n  name: 'old'\n",
			want:    "profile:\n  name: 'new'\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeRewriteFixture(t, tc.content)
			if _, err := RewriteProfileName(root, "new"); err != nil {
				t.Fatalf("RewriteProfileName: %v", err)
			}
			if got := readRewriteFixture(t, root); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// CRLF 与多字节值：行列换算必须按字节落点，不能把行尾 \r 或中文挤坏。
func TestRewriteProfileNameHandlesCRLFAndMultibyte(t *testing.T) {
	content := "profile:\r\n  name: 旧名字\r\n  description: 说明文字\r\n"
	root := writeRewriteFixture(t, content)

	if _, err := RewriteProfileName(root, "renamed"); err != nil {
		t.Fatalf("RewriteProfileName: %v", err)
	}
	want := "profile:\r\n  name: renamed\r\n  description: 说明文字\r\n"
	if got := readRewriteFixture(t, root); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// 未声明 profile.name：不动文件，changed=false（调用方给提示，不猜名字）。
func TestRewriteProfileNameWithoutDeclaredNameIsNoOp(t *testing.T) {
	content := "profile:\n  description: 只有描述\nagents:\n  default:\n    name: agent-name\n"
	root := writeRewriteFixture(t, content)

	changed, err := RewriteProfileName(root, "new-name")
	if err != nil {
		t.Fatalf("RewriteProfileName: %v", err)
	}
	if changed {
		t.Fatal("未声明 name 时不应报告已改写")
	}
	if got := readRewriteFixture(t, root); got != content {
		t.Fatalf("未声明 name 时文件不应变化，got %q", got)
	}
}

// 同名改写：内容不变，但仍报告已改写（语义是"已同步"）。
func TestRewriteProfileNameSameNameKeepsBytes(t *testing.T) {
	content := "profile:\n  name: same\n"
	root := writeRewriteFixture(t, content)

	changed, err := RewriteProfileName(root, "same")
	if err != nil {
		t.Fatalf("RewriteProfileName: %v", err)
	}
	if !changed {
		t.Fatal("同名也应报告已同步")
	}
	if got := readRewriteFixture(t, root); got != content {
		t.Fatalf("同名不应改写字节，got %q", got)
	}
}

func TestRewriteProfileNameErrors(t *testing.T) {
	if _, err := RewriteProfileName(t.TempDir(), "x"); err == nil {
		t.Fatal("缺 profile.yaml 应报错")
	}
	if _, err := RewriteProfileName(writeRewriteFixture(t, "profile:\n  name: x\n"), "  "); err == nil {
		t.Fatal("空名字应报错")
	}
	broken := writeRewriteFixture(t, "profile: [unclosed\n")
	if _, err := RewriteProfileName(broken, "x"); err == nil {
		t.Fatal("YAML 解析失败应报错")
	}
	if got := readRewriteFixture(t, broken); got != "profile: [unclosed\n" {
		t.Fatalf("解析失败时不应动文件，got %q", got)
	}
}
