package output

import (
	"encoding/json"
	"fmt"
	"os"
)

// ============================================================================
// Phase 6：离线回放 archive 序列化（B2 场景）
//
// 录屏文件的磁盘格式：顶层 ReplayArchiveFile（JSON），内含多个
// ReplayArchiveEntry。每个 entry 携带独立 checksum（ReplayArchiveChecksum），
// 读取时按 ReplayEnvelopeFromArchive 的 fail-closed 规则逐个校验。
// ============================================================================

// ReplayArchiveFileSchemaVersion 是 archive 文件（容器）自身的版本；与
// ReplayArchiveSchemaMajor/Minor（entry 内 schema）相互独立。未知版本拒绝。
const ReplayArchiveFileSchemaVersion = 1

// ReplayArchiveFile 是录屏文件的磁盘容器。Entries 不可为空。
type ReplayArchiveFile struct {
	FileSchemaVersion uint32               `json:"file_schema_version"`
	Entries           []ReplayArchiveEntry `json:"entries"`
}

// NewReplayArchiveFile 构造带当前容器版本的空 archive。
func NewReplayArchiveFile() *ReplayArchiveFile {
	return &ReplayArchiveFile{FileSchemaVersion: ReplayArchiveFileSchemaVersion}
}

// WriteReplayArchive 把 entries 序列化为 JSON 落盘（原子：先写临时文件再 rename）。
// 每个 entry 的 PayloadChecksum 必须已正确计算（调用方用 ReplayArchiveChecksum）。
func WriteReplayArchive(path string, entries []ReplayArchiveEntry) error {
	if len(entries) == 0 {
		return fmt.Errorf("replay archive: empty entries")
	}
	file := NewReplayArchiveFile()
	file.Entries = entries
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("replay archive: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("replay archive: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replay archive: rename: %w", err)
	}
	return nil
}

// ReadReplayArchive 读取并反序列化录屏文件。只做容器级校验（文件 schema
// 版本、entries 非空）；entry 级 fail-closed 校验由 ReplayEnvelopeFromArchive
// 负责（见 DecodeReplayArchive 与 VerifyReplayArchive）。
func ReadReplayArchive(path string) (*ReplayArchiveFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("replay archive: read %s: %w", path, err)
	}
	return DecodeReplayArchive(data)
}

// DecodeReplayArchive 从字节解码 archive 容器并校验容器级约束。
func DecodeReplayArchive(data []byte) (*ReplayArchiveFile, error) {
	var file ReplayArchiveFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("replay archive: decode: %w", err)
	}
	if file.FileSchemaVersion != ReplayArchiveFileSchemaVersion {
		return nil, fmt.Errorf("replay archive: unsupported file schema %d", file.FileSchemaVersion)
	}
	if len(file.Entries) == 0 {
		return nil, fmt.Errorf("replay archive: empty entries")
	}
	return &file, nil
}

// VerifyReplayArchive 按给定 mode 对 archive 全部 entry 做 fail-closed 校验。
// 返回 (validCount, firstErr)：任一 entry 不合法即返回错误，不产生部分结果。
// 该函数只读不执行回放（--replay-verify 语义）。
func VerifyReplayArchive(file *ReplayArchiveFile, mode ReplayMode) (int, error) {
	if file == nil {
		return 0, fmt.Errorf("replay archive: nil file")
	}
	for i := range file.Entries {
		if _, err := ReplayEnvelopeFromArchive(file.Entries[i], mode); err != nil {
			return 0, fmt.Errorf("replay archive entry %d: %w", i, err)
		}
	}
	return len(file.Entries), nil
}
