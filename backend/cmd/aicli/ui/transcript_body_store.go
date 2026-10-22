package ui

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_body_store.go —— 冷区 cell body 的写穿契约与编解码（任务 T1 / Spike-1）。
//
// 设计文档：§14.2（CellBodyStore）、§14.3（先写穿后切换）、§14.5（回填与校验）。
//
// 职责边界：
//   - 本文件定义 UI 侧的 body 编解码、指纹与校验；落盘实现由 commands 的
//     sqlite 适配器提供（internal/chat.SessionCellStore，opaque payload）。
//   - 重建校验失败必须 fail-closed（T3/T4 据此标记 Unrebuildable），因此
//     指纹覆盖所有影响“同一 cell 的同一渲染”的字段。

// CellBodyEncodingJSONV1 是 Spike-1 使用的编解码标识。JSON 便于对拍；
// 后续若需要更紧凑的格式，新增 encoding 常量并保持向后读取。
const CellBodyEncodingJSONV1 = "json-v1"

const cellBodySchemaVersion = 1

// CellBodyMeta 与设计文档 §14.2 的 ColdCellRef 对齐（写穿侧最小集）。
type CellBodyMeta struct {
	CellID    scene.CellID `json:"cell_id"`
	Revision  uint64       `json:"revision"`
	SourceFP  uint64       `json:"source_fp"`
	RowsAtGen int32        `json:"rows_at_gen"`
	Gen       uint64       `json:"gen"`
	Encoding  string       `json:"encoding"`
}

// CellBodyPayload 是不可变的写穿单元（meta + 编码后的 body）。
type CellBodyPayload struct {
	Meta CellBodyMeta
	Data []byte
}

// CellBodyStore 是写穿存储的接口（T1 冻结；T3/T4 的逐出路径先行 Put，
// T5 的懒加载路径按身份 Get）。
type CellBodyStore interface {
	Put(ctx context.Context, sessionID string, body CellBodyPayload) error
	Get(ctx context.Context, sessionID string, cellID scene.CellID, revision uint64) (CellBodyPayload, bool, error)
}

type cellBodyEnvelope struct {
	SchemaVersion int                  `json:"schema_version"`
	RowsAtGen     int32                `json:"rows_at_gen"`
	Gen           uint64               `json:"gen"`
	Cell          scene.TranscriptCell `json:"cell"`
}

// MarshalTranscriptCellBody 把 cell 编码为写穿 payload。
//
// rowsAtGen/gen 是**交付语境**（行数在哪一代计算、对应哪次交付），
// 不参与 SourceFP（它们不是 cell 语义的一部分，而是账本事实）。
func MarshalTranscriptCellBody(cell scene.TranscriptCell, rowsAtGen int32, gen uint64) (CellBodyPayload, error) {
	envelope := cellBodyEnvelope{
		SchemaVersion: cellBodySchemaVersion,
		RowsAtGen:     rowsAtGen,
		Gen:           gen,
		Cell:          cell,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return CellBodyPayload{}, fmt.Errorf("marshal cell %d body: %w", cell.ID, err)
	}
	return CellBodyPayload{
		Meta: CellBodyMeta{
			CellID:    cell.ID,
			Revision:  cell.Revision,
			SourceFP:  CellBodySourceFingerprint(cell),
			RowsAtGen: rowsAtGen,
			Gen:       gen,
			Encoding:  CellBodyEncodingJSONV1,
		},
		Data: raw,
	}, nil
}

// UnmarshalTranscriptCellBody 解码 payload；只认已知 encoding。
func UnmarshalTranscriptCellBody(payload CellBodyPayload) (scene.TranscriptCell, error) {
	if payload.Meta.Encoding != CellBodyEncodingJSONV1 {
		return scene.TranscriptCell{}, fmt.Errorf("cell %d: unsupported body encoding %q",
			payload.Meta.CellID, payload.Meta.Encoding)
	}
	var envelope cellBodyEnvelope
	if err := json.Unmarshal(payload.Data, &envelope); err != nil {
		return scene.TranscriptCell{}, fmt.Errorf("cell %d: decode body: %w", payload.Meta.CellID, err)
	}
	if envelope.SchemaVersion != cellBodySchemaVersion {
		return scene.TranscriptCell{}, fmt.Errorf("cell %d: schema version %d unsupported",
			payload.Meta.CellID, envelope.SchemaVersion)
	}
	return envelope.Cell, nil
}

// ValidateTranscriptCellBody 解码并校验身份与指纹。任何不一致都返回错误，
// 调用方必须 fail-closed（保持 cell 驻留并计入 rematerialize_failures）。
func ValidateTranscriptCellBody(payload CellBodyPayload) (scene.TranscriptCell, error) {
	cell, err := UnmarshalTranscriptCellBody(payload)
	if err != nil {
		return scene.TranscriptCell{}, err
	}
	if cell.ID != payload.Meta.CellID {
		return cell, fmt.Errorf("cell body identity mismatch: payload=%d decoded=%d", payload.Meta.CellID, cell.ID)
	}
	if cell.Revision != payload.Meta.Revision {
		return cell, fmt.Errorf("cell %d revision mismatch: payload=%d decoded=%d",
			cell.ID, payload.Meta.Revision, cell.Revision)
	}
	if got := CellBodySourceFingerprint(cell); got != payload.Meta.SourceFP {
		return cell, fmt.Errorf("cell %d fingerprint mismatch: payload=%d decoded=%d", cell.ID, payload.Meta.SourceFP, got)
	}
	return cell, nil
}

// CellBodySourceFingerprint 计算 cell 语义指纹（FNV-1a 64）。
//
// 覆盖：ID/Revision/Sequence/Kind/Phase/Boundary/Blocked/Provenance/ChainKey/
// BoundaryGroupKey/Source/Presentation（JSON 编码）。time 字段（CreatedAt/
// FinalizedAt）不参与——它们不改变同一 cell 的渲染与行数。
func CellBodySourceFingerprint(cell scene.TranscriptCell) uint64 {
	h := fnv.New64a()
	var scratch [8]byte
	writeUint64 := func(value uint64) {
		binary.LittleEndian.PutUint64(scratch[:], value)
		_, _ = h.Write(scratch[:])
	}
	writeString := func(value string) {
		writeUint64(uint64(len(value)))
		_, _ = h.Write([]byte(value))
	}

	writeUint64(uint64(cell.ID))
	writeUint64(cell.Revision)
	writeUint64(cell.Sequence)
	writeUint64(uint64(cell.Kind))
	writeUint64(uint64(cell.Phase))
	writeUint64(uint64(cell.Boundary))
	if cell.HistoryCommitBlocked {
		writeUint64(1)
	} else {
		writeUint64(0)
	}
	writeString(cell.Source)
	writeString(cell.ChainKey)
	writeString(cell.BoundaryGroupKey)
	writeString(cell.Provenance)
	// Presentation 是结构化投影（Document/DiffLabel/…）；JSON 编码保证
	// 新增字段时指纹自然覆盖（顺序稳定：encoding/json 按字段名排序）。
	if raw, err := json.Marshal(cell.Presentation); err == nil {
		writeString(string(raw))
	} else {
		writeString(fmt.Sprintf("%#v", cell.Presentation))
	}
	return h.Sum64()
}

// MemoryCellBodyStore 是 CellBodyStore 的内存实现：测试与影子对账用
// （生产路径使用 sqlite 适配器，见 commands/transcript_body_store_sqlite.go）。
type MemoryCellBodyStore struct {
	rows map[memoryCellBodyKey]CellBodyPayload
}

type memoryCellBodyKey struct {
	sessionID string
	cellID    scene.CellID
	revision  uint64
}

func NewMemoryCellBodyStore() *MemoryCellBodyStore {
	return &MemoryCellBodyStore{rows: make(map[memoryCellBodyKey]CellBodyPayload)}
}

func (m *MemoryCellBodyStore) Put(_ context.Context, sessionID string, body CellBodyPayload) error {
	if m.rows == nil {
		m.rows = make(map[memoryCellBodyKey]CellBodyPayload)
	}
	m.rows[memoryCellBodyKey{sessionID: sessionID, cellID: body.Meta.CellID, revision: body.Meta.Revision}] = body
	return nil
}

func (m *MemoryCellBodyStore) Get(_ context.Context, sessionID string, cellID scene.CellID, revision uint64) (CellBodyPayload, bool, error) {
	if m == nil || m.rows == nil {
		return CellBodyPayload{}, false, nil
	}
	body, ok := m.rows[memoryCellBodyKey{sessionID: sessionID, cellID: cellID, revision: revision}]
	return body, ok, nil
}
