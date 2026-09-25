package commands

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"
)

// chat_profile_editor.go 解析 $EDITOR/$VISUAL 并拉起编辑器（D27 的 --open 路径）。
//
// 为什么不能只做 strings.Fields：Windows 上编辑器路径几乎总是含空格
// （C:\Program Files\Microsoft VS Code\Code.exe），按空白切分会把 "C:\Program"
// 当成可执行文件，报 exec: "C:\\Program": executable file not found in %PATH%。
// 用户看到的错误来自这里，而不是编辑器本身。

// resolveEditorCommand 把 $EDITOR/$VISUAL 解析为可执行文件与前置参数。
//
// 规则（从强到弱）：
//  1. 整串本身就是一个存在的文件 → 直接作为可执行文件（Windows 最常见形态，无需引号）；
//  2. 引号（"..." / '...'）包裹的 token 保留内部空格，其余按空白切分；
//  3. 未加引号且首 token 既不是文件也不在 PATH 上时，贪心向后合并 token，
//     直到命中存在的文件（覆盖 `C:\Program Files\...\Code.exe --wait` 这类写法）。
//
// 解析失败（空串、引号未闭合）时如实报错，不静默回退到错误的可执行文件。
func resolveEditorCommand(value string) (string, []string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil, fmt.Errorf("编辑器命令为空")
	}
	if isExecutableFile(value) {
		return value, nil, nil
	}
	tokens, err := splitEditorCommandLine(value)
	if err != nil {
		return "", nil, err
	}
	if len(tokens) == 0 {
		return "", nil, fmt.Errorf("编辑器命令为空")
	}
	if !isExecutableFile(tokens[0]) && !commandOnPath(tokens[0]) {
		for index := 1; index < len(tokens); index++ {
			joined := strings.Join(tokens[:index+1], " ")
			if isExecutableFile(joined) {
				return joined, tokens[index+1:], nil
			}
		}
	}
	return tokens[0], tokens[1:], nil
}

// splitEditorCommandLine 按空白切分命令串，但保留引号内的空格。
func splitEditorCommandLine(value string) ([]string, error) {
	tokens := make([]string, 0, 4)
	current := strings.Builder{}
	var quote rune
	for _, char := range value {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
				continue
			}
			current.WriteRune(char)
		case char == '"' || char == '\'':
			quote = char
		case unicode.IsSpace(char):
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("编辑器命令引号未闭合: %s", value)
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}

// isExecutableFile 判断路径是否指向一个存在的普通文件（不做权限判定：Windows
// 没有 Unix 的可执行位，是否可执行交给 exec 的启动结果）。
func isExecutableFile(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// commandOnPath 判断名字能否在 PATH 上解析（Windows 下含 PATHEXT 扩展名）。
func commandOnPath(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}
