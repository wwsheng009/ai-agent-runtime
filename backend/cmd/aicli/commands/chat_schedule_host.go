package commands

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// newLocalScheduleService opens the shared schedule store when the schedule
// subsystem is enabled. Disabled or unopenable returns nil, so the schedule_*
// tools stay out of the model tool surface entirely (the broker only advertises
// them when Broker.Schedule is non-nil).
func newLocalScheduleService(cfg *config.RuntimeConfig) *schedule.Service {
	if cfg == nil || !cfg.Schedule.Enabled {
		return nil
	}
	sc := cfg.Schedule.WithDefaults()
	path := strings.TrimSpace(sc.StorePath)
	if path == "" {
		base := filepath.Dir(agentconfig.DefaultAuthStorePath())
		if base == "" || base == "." {
			return nil
		}
		path = filepath.Join(base, "data", "schedule", "schedule.sqlite")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	store, err := schedule.OpenStore(path)
	if err != nil {
		return nil
	}
	return schedule.NewService(store, schedule.SystemClock)
}
