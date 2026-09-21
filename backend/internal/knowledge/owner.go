package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// 锁文件策略（ADR-0001 单写者 / 多读者）：
//
//	owner  : 以 O_CREATE|O_EXCL 独占创建 <store>.lock，持有到进程退出；
//	reader : 锁已被活着的进程持有，只读打开数据库，绝不写。
//
// 判定"持有者是否还活着"有两道防线：
//  1. 锁内记录的 pid 是否存活（processAlive，平台相关）；
//  2. 锁文件的 mtime 是否超过 maxLockAge——进程被 kill -9 且 pid 被复用时，
//     第一道防线会失效，第二道给出上界。
//
// 只有两道防线都判定为死锁时才会抢占；任何不确定都按"活着"处理，
// 因为误抢写锁会破坏单写者不变量，而误判为 reader 只是暂时降级为只读。
const (
	// maxLockAge 是锁的最长可信年龄；超过则视为陈旧锁。
	maxLockAge = 2 * time.Hour
	// corruptLockGrace 是损坏锁文件的宽限期；超过则视为陈旧锁。
	corruptLockGrace = 15 * time.Minute
)

// lockPayload 是锁文件内容：可读、可诊断，不参与正确性判定之外的用途。
type lockPayload struct {
	PID       int    `json:"pid"`
	Host      string `json:"host,omitempty"`
	StartedAt int64  `json:"started_at_unix_ms"`
	Version   int    `json:"knowledge_version"`
}

// ownership 表示本进程对某个 workspace store 的角色。
type ownership struct {
	lockPath string
	file     *os.File
	state    Role
	holder   int // reader 角色下，锁持有者的 pid（仅诊断用）
}

// acquireOwnership 尝试成为 owner；失败则降级为 reader。
//
// 只有真正的 I/O 错误（例如目录不可写）才返回 error：锁冲突不是错误，
// 而是"另一个进程正在写"的正常状态。
func acquireOwnership(cfg Config) (*ownership, error) {
	if err := cfg.ensureDir(); err != nil {
		return nil, err
	}
	lockPath := cfg.storePath() + ".lock"
	own := &ownership{lockPath: lockPath}

	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			own.file = file
			own.state = RoleOwner
			if err := own.writePayload(); err != nil {
				// 写不进内容不影响独占性：锁已由 O_EXCL 创建。
				return own, nil
			}
			return own, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("knowledge: acquire store lock %s: %w", lockPath, err)
		}

		stale, holder := lockIsStale(lockPath)
		if !stale {
			own.state = RoleReader
			own.holder = holder
			return own, nil
		}
		// 陈旧锁：删掉后重试一次；若期间有别的进程抢先创建，下一轮会按 reader 处理。
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			own.state = RoleReader
			own.holder = holder
			return own, nil
		}
	}

	own.state = RoleReader
	return own, nil
}

// writePayload 写入锁的元数据；失败只影响可诊断性。
func (o *ownership) writePayload() error {
	if o.file == nil {
		return errors.New("knowledge: lock file is not open")
	}
	host, _ := os.Hostname()
	payload, err := json.Marshal(lockPayload{
		PID:       os.Getpid(),
		Host:      host,
		StartedAt: time.Now().UnixMilli(),
		Version:   KnowledgeVersion,
	})
	if err != nil {
		return err
	}
	if _, err := o.file.Write(append(payload, '\n')); err != nil {
		return err
	}
	return o.file.Sync()
}

// lockIsStale 判定锁文件是否属于已死进程，并返回锁中记录的 pid。
func lockIsStale(lockPath string) (bool, int) {
	info, err := os.Stat(lockPath)
	if err != nil {
		// 锁刚被释放或无法读取：交给下一轮重试。
		return true, 0
	}
	age := time.Since(info.ModTime())

	body, err := os.ReadFile(lockPath)
	if err != nil {
		return age > corruptLockGrace, 0
	}
	var payload lockPayload
	if err := json.Unmarshal(body, &payload); err != nil || payload.PID <= 0 {
		return age > corruptLockGrace, 0
	}
	if age > maxLockAge {
		return true, payload.PID
	}
	return !processAlive(payload.PID), payload.PID
}

// role 返回本进程在仲裁后的角色。
func (o *ownership) role() Role {
	if o == nil || o.state == "" {
		return RoleNone
	}
	return o.state
}

// readOnly 报告 store 是否必须以只读方式打开。
func (o *ownership) readOnly() bool { return o == nil || o.state != RoleOwner }

// release 释放 owner 锁；对 reader 是空操作。幂等。
func (o *ownership) release() error {
	if o == nil || o.file == nil {
		return nil
	}
	err := o.file.Close()
	o.file = nil
	if removeErr := os.Remove(o.lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
		err = removeErr
	}
	o.state = RoleNone
	return err
}
