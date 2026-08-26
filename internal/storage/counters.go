package storage

import (
	"context"
	"database/sql"
	"errors"
)

const dashboardCountersQuery = `
SELECT total, active, completed, failed, failed_tweet_count
FROM dashboard_counters WHERE id = 1`

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
	if _, err := s.db.Exec(`
DROP TRIGGER IF EXISTS trg_jobs_ai;
DROP TRIGGER IF EXISTS trg_jobs_au;
DROP TRIGGER IF EXISTS trg_jobs_ad;
DROP TRIGGER IF EXISTS trg_failed_tweets_ai;
DROP TRIGGER IF EXISTS trg_failed_tweets_ad;
`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`
CREATE TRIGGER trg_jobs_ai AFTER INSERT ON jobs
BEGIN
	UPDATE dashboard_counters SET
		total = total + 1,
		active = active + CASE WHEN NEW.status IN ('pending', 'resolving', 'downloading') THEN 1 ELSE 0 END,
		completed = completed + CASE WHEN NEW.status = 'completed' THEN 1 ELSE 0 END,
		failed = failed + CASE WHEN NEW.status IN ('failed', 'completed_with_errors') THEN 1 ELSE 0 END
	WHERE id = 1;
END;
CREATE TRIGGER trg_jobs_au AFTER UPDATE OF status ON jobs
BEGIN
	UPDATE dashboard_counters SET
		active = active
			- CASE WHEN OLD.status IN ('pending', 'resolving', 'downloading') THEN 1 ELSE 0 END
			+ CASE WHEN NEW.status IN ('pending', 'resolving', 'downloading') THEN 1 ELSE 0 END,
		completed = completed
			- CASE WHEN OLD.status = 'completed' THEN 1 ELSE 0 END
			+ CASE WHEN NEW.status = 'completed' THEN 1 ELSE 0 END,
		failed = failed
			- CASE WHEN OLD.status IN ('failed', 'completed_with_errors') THEN 1 ELSE 0 END
			+ CASE WHEN NEW.status IN ('failed', 'completed_with_errors') THEN 1 ELSE 0 END
	WHERE id = 1 AND OLD.status != NEW.status;
END;
CREATE TRIGGER trg_jobs_ad AFTER DELETE ON jobs
BEGIN
	UPDATE dashboard_counters SET
		total = MAX(0, total - 1),
		active = MAX(0, active - CASE WHEN OLD.status IN ('pending', 'resolving', 'downloading') THEN 1 ELSE 0 END),
		completed = MAX(0, completed - CASE WHEN OLD.status = 'completed' THEN 1 ELSE 0 END),
		failed = MAX(0, failed - CASE WHEN OLD.status IN ('failed', 'completed_with_errors') THEN 1 ELSE 0 END)
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
`); err != nil {
		return err
	}
	return s.runMigrationOnce("dashboard_counters_v1", func(exec migrationExecutor) error {
		_, err := exec.Exec(`
UPDATE dashboard_counters SET
	total = (SELECT COUNT(*) FROM jobs),
	active = (SELECT COUNT(*) FROM jobs WHERE status IN ('pending', 'resolving', 'downloading')),
	completed = (SELECT COUNT(*) FROM jobs WHERE status = 'completed'),
	failed = (SELECT COUNT(*) FROM jobs WHERE status IN ('failed', 'completed_with_errors')),
	failed_tweet_count = (SELECT COUNT(*) FROM failed_tweets)
WHERE id = 1`)
		return err
	})
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
