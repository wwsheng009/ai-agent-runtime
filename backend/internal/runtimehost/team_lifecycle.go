package runtimehost

import (
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/teamsupervisor"
)

// BuildTeamSupervisorConfig maps the shared runtime config to the shared team
// reconciler config. Hosts still provide DesiredTeams/RunLoop/OwnerAllowed/
// event hooks; only timing and restart policy are centralized.
func BuildTeamSupervisorConfig(config *runtimecfg.RuntimeConfig) teamsupervisor.Config {
	supervisorConfig := teamsupervisor.Config{}
	if config == nil {
		return supervisorConfig
	}
	orchestrator := config.Team.Orchestrator
	supervisorConfig.ScanInterval = orchestrator.ReconcileInterval
	supervisorConfig.RestartBackoff = orchestrator.RestartBackoff
	supervisorConfig.MaxRestartBackoff = orchestrator.MaxRestartBackoff
	return supervisorConfig
}
