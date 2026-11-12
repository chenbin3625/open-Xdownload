package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// JobCheckpoint 是长任务（列表/关注归档、失败批次重试等）在用户/批次边界保存的
// 分段进度。payload 由 jobs 包定义具体格式；storage 只负责持久化。
//
// 检查点必须在本段数据已落库、可重试失败已入队之后写入，保证进程重启或限流让出后
// 从已提交位置继续，而不是跳过历史数据或重复处理。
type JobCheckpoint struct {
	JobID     int64
	Payload   []byte
	UpdatedAt time.Time
}

// SaveJobCheckpoint 覆盖写入任务检查点。与任务行在同一 SQLite 写者上串行，无需额外锁。
func (s *Store) SaveJobCheckpoint(ctx context.Context, jobID int64, payload []byte) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO job_checkpoints (job_id, payload, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(job_id) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at`,
		jobID, string(payload), time.Now().UTC())
	return err
}

// LoadJobCheckpoint 读取任务检查点；不存在时返回 (nil, false, nil)。
func (s *Store) LoadJobCheckpoint(ctx context.Context, jobID int64) ([]byte, bool, error) {
	var payload string
	err := s.db.GetContext(ctx, &payload, `SELECT payload FROM job_checkpoints WHERE job_id = ?`, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(payload), true, nil
}

// DeleteJobCheckpoint 删除任务检查点（任务完成或永久失败时调用）。
func (s *Store) DeleteJobCheckpoint(ctx context.Context, jobID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM job_checkpoints WHERE job_id = ?`, jobID)
	return err
}
