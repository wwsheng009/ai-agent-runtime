package scene

import "fmt"

// scene_trim.go —— T6 scene 层窗口化的可逆裁剪原语。
//
// 设计文档：§14.1(L0)「renderScene 不得无限累积」；本文件只提供**可逆**的
// 账本变换（裁剪/恢复），写穿与策略（keepLast/开关）由 commands 层负责：
// 未先写穿就裁剪会丢数据，因此调用方必须先持久化，再调用 TrimPrefix。

// TrimPrefix 移除最前面的 count 个 cell，并返回它们的**独立副本**
// （供写穿与后续恢复）。
//
// 约束（违反即拒绝且场景不变）：
//   - count 必须为正且不超过当前 cell 数；
//   - 被裁前缀不得包含 mutable cell（frontier barrier 必须常驻）。
//
// 不变量：CellID/Sequence 单调不回退（nextID/nextSeq 不动）；revision 与
// contentVersion 前进；snapshot 缓存失效。
func (s *TuiScene) TrimPrefix(count int) ([]*TranscriptCell, error) {
	if s == nil {
		return nil, fmt.Errorf("nil scene")
	}
	if count <= 0 {
		return nil, fmt.Errorf("trim prefix: count must be positive")
	}
	if count > len(s.cells) {
		return nil, fmt.Errorf("trim prefix: count %d exceeds cells %d", count, len(s.cells))
	}
	for index := 0; index < count; index++ {
		cell := s.cells[index]
		if cell == nil {
			return nil, fmt.Errorf("trim prefix: nil cell at index %d", index)
		}
		if cell.Phase == CellMutable {
			return nil, fmt.Errorf("trim prefix: cell %d is mutable (frontier barrier)", cell.ID)
		}
	}

	detached := make([]*TranscriptCell, 0, count)
	for index := 0; index < count; index++ {
		cp := cloneCell(*s.cells[index])
		detached = append(detached, &cp)
	}
	for index := 0; index < count; index++ {
		cell := s.cells[index]
		delete(s.byID, cell.ID)
		if cell.ChainKey != "" && s.byChain[cell.ChainKey] == cell {
			delete(s.byChain, cell.ChainKey)
		}
	}
	s.cells = append([]*TranscriptCell(nil), s.cells[count:]...)
	s.revision++
	s.contentVersion++
	s.lastSnapshot = nil
	return detached, nil
}

// RestorePrefix 把此前裁掉的 cell 依给定顺序放回**头部**（幂等：已存在的
// ID 跳过）。返回实际插入数量。用于 reconcile / 回填需要完整视图时的恢复，
// 与 TrimPrefix 构成往返：顺序、byID、byChain 与裁剪前一致
// （nextID/nextSeq 不回退，由调用方按单调语义接受）。
func (s *TuiScene) RestorePrefix(cells []*TranscriptCell) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("nil scene")
	}
	if len(cells) == 0 {
		return 0, nil
	}
	seen := make(map[CellID]struct{}, len(cells))
	restored := make([]*TranscriptCell, 0, len(cells))
	restoredIDs := make(map[CellID]struct{}, len(cells))
	for _, cell := range cells {
		if cell == nil {
			continue
		}
		if _, exists := s.byID[cell.ID]; exists {
			continue
		}
		if _, duplicate := seen[cell.ID]; duplicate {
			return 0, fmt.Errorf("restore prefix: duplicate cell id %d", cell.ID)
		}
		seen[cell.ID] = struct{}{}
		cp := cloneCell(*cell)
		restored = append(restored, &cp)
		restoredIDs[cell.ID] = struct{}{}
	}
	if len(restored) == 0 {
		return 0, nil
	}
	remainder := make([]*TranscriptCell, 0, len(s.cells))
	for _, cell := range s.cells {
		if cell == nil {
			continue
		}
		if _, isRestored := restoredIDs[cell.ID]; isRestored {
			continue
		}
		remainder = append(remainder, cell)
	}
	next := make([]*TranscriptCell, 0, len(restored)+len(remainder))
	next = append(next, restored...)
	next = append(next, remainder...)
	s.cells = next
	for _, cp := range restored {
		s.byID[cp.ID] = cp
		if cp.ChainKey != "" {
			if _, exists := s.byChain[cp.ChainKey]; !exists {
				s.byChain[cp.ChainKey] = cp
			}
		}
	}
	s.revision++
	s.contentVersion++
	s.lastSnapshot = nil
	return len(restored), nil
}
