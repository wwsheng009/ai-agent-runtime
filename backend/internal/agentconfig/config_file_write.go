package agentconfig

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// 配置文件写事务（方案 §3.4 M11 / §12 R4）。
//
// 本包内每一份配置文件的写入都是整份「读 → 改 → 写」：读到的旧快照 + 本次变更
// 落盘。并发写者若不串行化，后写者会带着旧快照覆盖前者的字段（丢更新）。因此本
// 文件提供**唯一**的写入通道：把「取锁 → 读 → 解析 → 变更 → 编码 → 原子写」收进
// 一个事务，任何新写点只要经过它，就自动获得与其它写点相同的互斥。

// configDocumentWriteOptions 描述一次配置文件写事务的读取语义。
type configDocumentWriteOptions struct {
	// createStarterWhenMissing：目标文件不存在时先落一份 starter 配置再读取
	// （既有语义：UpdateAICLIChatPreferences / UpdateAICLIThemePreferences /
	// UpdateProviderConfig / UpdateAICLIRoutingSection 都是这样处理首次写入的）。
	createStarterWhenMissing bool
}

// updateConfigFileDocument 在**同一把文件写锁内**完成一份 YAML 配置文件的
// 「读 → 解析文档 → 变更 → 编码 → 原子写」事务。
//
// 为什么读也必须在锁内：只锁写入等于没锁——两个写者各自在锁外读到同一份旧快照，
// 再排队落盘，后写者仍然丢掉前者的字段。锁必须覆盖整个事务。
//
// mutate 就地修改 document（YAML 文档节点）与 root（根 mapping）；返回错误则不落盘。
// 文件不存在且未开启 createStarterWhenMissing 时按读失败返回（错误文案与既有写点
// 一致：`read config file %s`）。
func updateConfigFileDocument(path string, opts configDocumentWriteOptions, mutate func(document, root *yaml.Node) error) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("config path is required")
	}
	return updateConfigFileDocumentLocked(path, opts, mutate, LockConfigFileWrite(path))
}

// updateConfigFileDocumentLocked 是不取锁的变体：调用方必须已持有 path 的写锁
// （用于已持锁的复合写点）。unlock 由调用方提供，函数返回前统一释放。
func updateConfigFileDocumentLocked(path string, opts configDocumentWriteOptions, mutate func(document, root *yaml.Node) error, unlock func()) error {
	defer unlock()

	raw, err := readConfigFileForWrite(path, opts)
	if err != nil {
		return err
	}

	document, err := parseYAMLDocument(raw)
	if err != nil {
		return err
	}
	root, err := ensureYAMLRootMapping(document)
	if err != nil {
		return err
	}
	if mutate != nil {
		if err := mutate(document, root); err != nil {
			return err
		}
	}

	out, err := encodeYAMLDocument(document)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

// readConfigFileForWrite 读取配置文件的原始字节；开启 createStarterWhenMissing 时，
// 文件缺失先落一份 starter 配置再读（锁内调用 locked 变体，避免自锁）。
func readConfigFileForWrite(path string, opts configDocumentWriteOptions) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return raw, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	if !opts.createStarterWhenMissing {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	if _, _, starterErr := ensureStarterConfigAtPathLocked(path); starterErr != nil {
		return nil, starterErr
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read starter config file %s: %w", path, err)
	}
	return raw, nil
}

// encodeYAMLDocument 以既有写点一致的 2 空格缩进编码 YAML 文档。
func encodeYAMLDocument(document *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("encode config yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("finalize config yaml: %w", err)
	}
	return output.Bytes(), nil
}
