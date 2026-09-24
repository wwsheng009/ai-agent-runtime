package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RewriteProfileName 把 <root>/profile.yaml 里顶层 profile.name 改写为 newName。
//
// 为什么需要它（D34）：rename 是唯一的"改名"操作。如果只改目录名而不改声明名，
// "目录名 vs 声明名"就永久分叉——`/profile show`、导出包、前端列表会同时显示两个
// 名字，用户无法判断哪个才是身份。这里只替换 name 值的行内区间（定位靠 yaml.v3 的
// 节点行列号，与校验同一套解析器，不用正则猜），注释、字段顺序、缩进、引号风格与
// 换行符全部保持原样。
//
// 返回值 changed=false 表示文件未声明 profile.name（此时不动文件，由调用方决定
// 是提示还是报错）；error 表示解析或写盘失败（调用方应回滚目录改名）。
func RewriteProfileName(root, newName string) (bool, error) {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return false, fmt.Errorf("profile name is required")
	}
	path := filepath.Join(root, "profile.yaml")
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(raw)

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	valueNode := profileNameValueNode(&doc)
	if valueNode == nil {
		return false, nil
	}
	updated, err := spliceYAMLScalar(text, valueNode, newName)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if updated == text {
		return true, nil
	}
	if err := os.WriteFile(path, []byte(updated), info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

// profileNameValueNode 定位顶层 profile.name 的值节点；结构不符（不是映射、没有
// profile 块、name 不是标量）时返回 nil——此时调用方按"未声明"处理，绝不猜测。
func profileNameValueNode(doc *yaml.Node) *yaml.Node {
	node := doc
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	profileNode := yamlMappingValue(node, "profile")
	if profileNode == nil || profileNode.Kind != yaml.MappingNode {
		return nil
	}
	nameNode := yamlMappingValue(profileNode, "name")
	if nameNode == nil || nameNode.Kind != yaml.ScalarNode {
		return nil
	}
	return nameNode
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// spliceYAMLScalar 用 newValue 替换 text 中 node 覆盖的标量区间，返回新文本。
// 区间必须与解析结果自洽（明文标量逐字节相等；引号标量引号闭合），否则报错而不是
// 写出一个半截文件。
func spliceYAMLScalar(text string, node *yaml.Node, newValue string) (string, error) {
	lines := strings.Split(text, "\n")
	if node.Line < 1 || node.Line > len(lines) {
		return "", fmt.Errorf("值位置越界（line %d，共 %d 行）", node.Line, len(lines))
	}
	line := lines[node.Line-1]
	start, ok := byteOffsetForYAMLColumn(line, node.Column)
	if !ok {
		return "", fmt.Errorf("无法定位第 %d 行第 %d 列的值区间", node.Line, node.Column)
	}

	quote := byte(0)
	if start < len(line) && (line[start] == '"' || line[start] == '\'') {
		quote = line[start]
	}
	replacement := newValue
	var end int
	if quote != 0 {
		offset := strings.IndexByte(line[start+1:], quote)
		if offset < 0 {
			return "", fmt.Errorf("第 %d 行的引号未闭合", node.Line)
		}
		end = start + 1 + offset + 1
		replacement = string(quote) + newValue + string(quote)
	} else {
		end = start + len(node.Value)
		if end > len(line) || line[start:end] != node.Value {
			return "", fmt.Errorf("第 %d 行的值区间与解析结果不一致，拒绝改写", node.Line)
		}
	}
	if end > len(line) {
		return "", fmt.Errorf("第 %d 行的值区间越界", node.Line)
	}
	lines[node.Line-1] = line[:start] + replacement + line[end:]
	return strings.Join(lines, "\n"), nil
}

// byteOffsetForYAMLColumn 把 yaml.v3 的 1-based 列号（按 rune 计）换算成字节偏移。
func byteOffsetForYAMLColumn(line string, column int) (int, bool) {
	if column < 1 {
		return 0, false
	}
	runeIndex := 0
	for byteIndex := range line {
		if runeIndex == column-1 {
			return byteIndex, true
		}
		runeIndex++
	}
	if runeIndex == column-1 {
		return len(line), true
	}
	return 0, false
}
