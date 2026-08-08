package scene

import "fmt"

// CellMutation 是一次 transcript cell 变更（unified plan §6.1 的
// AppendCell/UpdateCell/FinalizeCell/RemoveMutableCell 投影）。
type CellMutation interface {
	mutation()
}

func (*AppendCell) mutation()        {}
func (*InsertCell) mutation()        {}
func (*InsertCellBefore) mutation()  {}
func (*UpdateCell) mutation()        {}
func (*FinalizeCell) mutation()      {}
func (*RemoveMutableCell) mutation() {}

// FlushPolicy 控制事务提交后的 presenter flush 策略（unified plan §6.2）。
// 不能绕过 Scene：任何策略下 Scene 都先完整提交。
type FlushPolicy uint8

const (
	FlushImmediate            FlushPolicy = iota // 提交后立即 flush（默认）
	FlushCoalescable                             // 可合并：status/mutable update 等可延迟
	FlushNoPrimaryDuringLease                    // fullscreen lease 期间不 flush 主屏
)

// SceneTransaction 是一次原子提交（unified plan §6.2 / INV-FRAME-01）：
// 要么完整应用，要么整体不应用。
type SceneTransaction struct {
	Cause     string         // 触发原因（事件类型/来源，诊断用）
	Mutations []CellMutation // 同一 batch 内的全部变更
	Flush     FlushPolicy    // 提交后的 flush 策略
}

// SceneController 串行消费事务并维护唯一权威 Scene（unified plan §4.1）。
//
// 生命周期（§6.2）：Validate -> Reduce to candidate -> Check invariants
// -> Commit Scene revision -> Produce immutable snapshot。失败时 Scene
// revision 不变（整体回滚）。
type SceneController struct {
	scene *TuiScene
}

// NewController 创建绑定给定 Scene 的控制器。
func NewController(s *TuiScene) *SceneController {
	return &SceneController{scene: s}
}

// Scene 返回控制器持有的 Scene。
func (c *SceneController) Scene() *TuiScene {
	if c == nil {
		return nil
	}
	return c.scene
}

// Submit 提交一个事务。任一 mutation 失败则整体回滚（Scene 不变）。
// 成功返回新 Scene revision 与受影响 cell 副本列表。
// 回滚实现：应用前捕获 Scene 全量快照，失败时整体还原（INV-FRAME-01）。
func (c *SceneController) Submit(tx SceneTransaction) (uint64, []*TranscriptCell, error) {
	if c == nil || c.scene == nil {
		return 0, nil, fmt.Errorf("nil controller or scene")
	}
	if tx.Flush > FlushNoPrimaryDuringLease {
		return 0, nil, fmt.Errorf("invalid flush policy %d", tx.Flush)
	}
	before := c.scene.Snapshot()
	applied := make([]*TranscriptCell, 0, len(tx.Mutations))
	for _, m := range tx.Mutations {
		cell, err := c.scene.ApplyCellMutation(m)
		if err != nil {
			c.restore(before)
			return 0, nil, err
		}
		applied = append(applied, cell)
	}
	c.scene.revision++
	return c.scene.revision, applied, nil
}

// restore 用快照整体还原 Scene（失败回滚的唯一路径）。
func (c *SceneController) restore(snap *Snapshot) {
	if c == nil || c.scene == nil || snap == nil {
		return
	}
	c.scene.cells = make([]*TranscriptCell, 0, len(snap.Cells))
	c.scene.byID = make(map[CellID]*TranscriptCell)
	c.scene.byChain = make(map[string]*TranscriptCell)
	for _, c0 := range snap.Cells {
		cp := cloneCell(*c0)
		c.scene.cells = append(c.scene.cells, &cp)
		c.scene.byID[cp.ID] = &cp
		if cp.ChainKey != "" {
			c.scene.byChain[cp.ChainKey] = &cp
		}
	}
	c.scene.revision = snap.Revision
	c.scene.contentVersion = snap.ContentVersion
	c.scene.lastSnapshot = nil
	// nextID/nextSeq 不因回滚而回退（单调性由 ID/Sequence 保证，
	// 分配过的 ID 不复用——INV-SCENE-02 只要求不复用已存在的 ID）。
}
