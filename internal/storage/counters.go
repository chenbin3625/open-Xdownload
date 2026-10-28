package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const dashboardCountersQuery = `
SELECT total, active, completed, failed, failed_tweet_count
FROM dashboard_counters WHERE id = 1`

// dashboardCounterTriggers 返回物化计数器触发器 DDL。状态集合来自 models/status.go 的
// 唯一来源（§3.1-11），避免触发器、对账 SQL 与 Go 判断三处各写一份字面量而静默漂移。
// 触发器是 DDL，无法绑定参数，所以这里拼接的是本包固定的枚举值。
func dashboardCounterTriggers() string {
	active := activeJobStatusesSQL
	completed := completedStatusesSQL
	failed := failedJobStatusesSQL
	return fmt.Sprintf(`
CREATE TRIGGER trg_jobs_ai AFTER INSERT ON jobs
BEGIN
	UPDATE dashboard_counters SET
		total = total + 1,
		active = active + CASE WHEN NEW.status IN (%[1]s) THEN 1 ELSE 0 END,
		completed = completed + CASE WHEN NEW.status IN (%[2]s) THEN 1 ELSE 0 END,
		failed = failed + CASE WHEN NEW.status IN (%[3]s) THEN 1 ELSE 0 END
	WHERE id = 1;
END;
CREATE TRIGGER trg_jobs_au AFTER UPDATE OF status ON jobs
BEGIN
	UPDATE dashboard_counters SET
		active = MAX(0, active
			- CASE WHEN OLD.status IN (%[1]s) THEN 1 ELSE 0 END
			+ CASE WHEN NEW.status IN (%[1]s) THEN 1 ELSE 0 END),
		completed = MAX(0, completed
			- CASE WHEN OLD.status IN (%[2]s) THEN 1 ELSE 0 END
			+ CASE WHEN NEW.status IN (%[2]s) THEN 1 ELSE 0 END),
		failed = MAX(0, failed
			- CASE WHEN OLD.status IN (%[3]s) THEN 1 ELSE 0 END
			+ CASE WHEN NEW.status IN (%[3]s) THEN 1 ELSE 0 END)
	WHERE id = 1 AND OLD.status != NEW.status;
END;
CREATE TRIGGER trg_jobs_ad AFTER DELETE ON jobs
BEGIN
	UPDATE dashboard_counters SET
		total = MAX(0, total - 1),
		active = MAX(0, active - CASE WHEN OLD.status IN (%[1]s) THEN 1 ELSE 0 END),
		completed = MAX(0, completed - CASE WHEN OLD.status IN (%[2]s) THEN 1 ELSE 0 END),
		failed = MAX(0, failed - CASE WHEN OLD.status IN (%[3]s) THEN 1 ELSE 0 END)
	WHERE id = 1;
END;
CREATE TRIGGER trg_failed_tweets_ai AFTER INSERT ON failed_tweets
BEGIN
	UPDATE dashboard_counters SET failed_tweet_count = failed_tweet_count + 1 WHERE id = 1;
END;
CREATE TRIGGER trg_failed_tweets_ad AFTER DELETE ON failed_tweets
BEGIN
	UPDATE dashboard_counters SET failed_tweet_count = MAX(0, failed_tweet_count - 1) WHERE id = 1;
END;
`, active, completed, failed)
}

// ensureDashboardCounters materializes job/failed-tweet totals so DashboardMeta
// is a single-row PK lookup instead of COUNT(*) over jobs on every request.
func (s *Store) ensureDashboardCounters() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS dashboard_counters (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	total INTEGER NOT NULL DEFAULT 0,
	active INTEGER NOT NULL DEFAULT 0,
	completed INTEGER NOT NULL DEFAULT 0,
	failed INTEGER NOT NULL DEFAULT 0,
	failed_tweet_count INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO dashboard_counters (id) VALUES (1);
`); err != nil {
		return err
	}
	// DROP + CREATE 必须原子：触发器每次 Open 都重建（以便同步定义变更），多个进程
	// 同时启动时若交错执行，会有一方撞上 "trigger already exists" 而启动失败。
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback() // Commit 后为 no-op
	if _, err := tx.Exec(`
DROP TRIGGER IF EXISTS trg_jobs_ai;
DROP TRIGGER IF EXISTS trg_jobs_au;
DROP TRIGGER IF EXISTS trg_jobs_ad;
DROP TRIGGER IF EXISTS trg_failed_tweets_ai;
DROP TRIGGER IF EXISTS trg_failed_tweets_ad;
`); err != nil {
		return err
	}
	if _, err := tx.Exec(dashboardCounterTriggers()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// 每次 Open 都对账一次，而不是只跑一次的 runMigrationOnce：触发器每次启动都重建，
	// 而 DashboardMeta 只读物化行，一旦计数器被外部改动/在触发器缺失期间发生过写入，
	// 偏差会永久留下且没有任何自愈路径。对账只是 5 个 COUNT(*)，只在启动时执行。
	return s.RecountDashboardCounters(context.Background())
}

// RecountDashboardCounters 用 jobs / failed_tweets 的真实数量重算物化计数器，
// 修复任何已经产生的偏差。正常运行时触发器不会漂移，这里是启动时的兜底修复路径。
// 状态集合与触发器共用同一份唯一来源。
func (s *Store) RecountDashboardCounters(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
UPDATE dashboard_counters SET
	total = (SELECT COUNT(*) FROM jobs),
	active = (SELECT COUNT(*) FROM jobs WHERE status IN (%s)),
	completed = (SELECT COUNT(*) FROM jobs WHERE status IN (%s)),
	failed = (SELECT COUNT(*) FROM jobs WHERE status IN (%s)),
	failed_tweet_count = (SELECT COUNT(*) FROM failed_tweets)
WHERE id = 1`, activeJobStatusesSQL, completedStatusesSQL, failedJobStatusesSQL))
	return err
}

func (s *Store) DashboardMeta(ctx context.Context) (JobStats, int, error) {
	var row struct {
		Total            int `db:"total"`
		Active           int `db:"active"`
		Completed        int `db:"completed"`
		Failed           int `db:"failed"`
		FailedTweetCount int `db:"failed_tweet_count"`
	}
	err := s.db.GetContext(ctx, &row, dashboardCountersQuery)
	if errors.Is(err, sql.ErrNoRows) {
		return JobStats{}, 0, nil
	}
	if err != nil {
		return JobStats{}, 0, err
	}
	return JobStats{Total: row.Total, Active: row.Active, Completed: row.Completed, Failed: row.Failed}, row.FailedTweetCount, nil
}
