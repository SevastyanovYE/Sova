package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const dailyOverviewEnabledKey = "daily_overview_enabled"

// DailyOverviewEnabled returns true until the user explicitly changes the
// setting. This preserves the scheduler behavior of installations that predate
// the persistent Nest setting.
func (s *Store) DailyOverviewEnabled(ctx context.Context) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `
		SELECT value
		FROM nest_settings
		WHERE key = ?
	`, dailyOverviewEnabledKey).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return true, nil
		}
		return false, fmt.Errorf("read daily overview setting: %w", err)
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid daily overview setting %q", value)
	}
}

func (s *Store) SetDailyOverviewEnabled(ctx context.Context, enabled bool, now time.Time) error {
	value := "false"
	if enabled {
		value = "true"
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO nest_settings(key, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at
	`, dailyOverviewEnabledKey, value, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("write daily overview setting: %w", err)
	}
	return nil
}
