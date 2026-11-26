package schedule

import "testing"

func TestDescribe(t *testing.T) {
	for _, c := range []struct{ cron, tz, want string }{
		{"* * * * *", "", "Every minute"},
		{"*/15 * * * *", "", "Every 15 minutes"},
		{"@hourly", "", "Every hour"},
		{"30 * * * *", "", "Every hour at minute 30"},
		{"0 */6 * * *", "", "Every 6 hours (UTC)"},
		{"0 3 * * *", "Europe/Paris", "Every day at 03:00 (Europe/Paris)"},
		{"@daily", "", "Every day at 00:00 (UTC)"},
		{"30 9 * * 1", "", "Every Monday at 09:30 (UTC)"},
		{"0 9 * * 1-5", "", "Monday to Friday at 09:00 (UTC)"},
		{"0 9 * * MON,wed,5", "", "Every Monday, Wednesday and Friday at 09:00 (UTC)"},
		{"@weekly", "", "Every Sunday at 00:00 (UTC)"},
		{"0 8 1 * *", "", "On day 1 of every month at 08:00 (UTC)"},
		{"0 0 25 12 *", "", "Every year on December 25 at 00:00 (UTC)"},
		{"@every 15m", "Europe/Paris", "Every 15 minutes"},
		{"@every 1h", "", "Every hour"},
		{"@every 90s", "", "Every 90 seconds"},
		{"0 9 1-7 * 1", "", `Cron "0 9 1-7 * 1" (UTC)`},
	} {
		if got := Describe(c.cron, c.tz); got != c.want {
			t.Errorf("Describe(%q, %q) = %q, want %q", c.cron, c.tz, got, c.want)
		}
	}
}

func TestDefaultName(t *testing.T) {
	for _, c := range []struct {
		workflow string
		n        int
		want     string
	}{
		{"NightlyReport", 0, "nightly-report"},
		{"NightlyReport", 1, "nightly-report-2"},
		{"ETL", 0, "etl"},
		{"sync_users_v2", 0, "sync-users-v2"},
	} {
		if got := DefaultName(c.workflow, c.n); got != c.want {
			t.Errorf("DefaultName(%q, %d) = %q, want %q", c.workflow, c.n, got, c.want)
		}
		s := Schedule{Name: DefaultName(c.workflow, c.n), Workflow: c.workflow, Cron: "@daily"}
		if err := s.Validate(); err != nil {
			t.Errorf("default name is not a valid name: %v", err)
		}
	}
}
