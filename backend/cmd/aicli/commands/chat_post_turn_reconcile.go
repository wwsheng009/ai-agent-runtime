package commands

import (
	"encoding/json"
	"fmt"
	"strings"
)

type chatPostTurnReconcileFunc func(session *ChatSession) (bool, error)

type chatPostTurnReconciler struct {
	Name string
	Run  chatPostTurnReconcileFunc
}

var chatPostTurnReconcilers = []chatPostTurnReconciler{
	{Name: "goal_completion_from_tool_messages", Run: reconcileGoalCompletionFromToolMessages},
}

func runPostTurnReconcilers(session *ChatSession) (bool, error) {
	var changed bool
	var errs []string
	var names []string
	var changedNames []string
	for _, reconciler := range chatPostTurnReconcilers {
		if reconciler.Run == nil {
			continue
		}
		name := strings.TrimSpace(reconciler.Name)
		if name == "" {
			name = "unnamed"
		}
		names = append(names, name)
		reconciled, err := reconciler.Run(session)
		if reconciled {
			changed = true
			changedNames = append(changedNames, name)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if changed || len(errs) > 0 {
		writeSessionDebugInfo(session, formatPostTurnReconcileDebug(names, changedNames, errs), false)
	}
	if len(errs) > 0 {
		return changed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return changed, nil
}

func runPostTurnReconcilersAndSync(session *ChatSession) error {
	changed, err := runPostTurnReconcilers(session)
	if changed {
		if syncErr := syncRuntimeSessionFromChat(session); syncErr != nil {
			if err != nil {
				return fmt.Errorf("%v; sync after post-turn reconcile: %w", err, syncErr)
			}
			return syncErr
		}
	}
	return err
}

func formatPostTurnReconcileDebug(names []string, changedNames []string, errors []string) string {
	payload := map[string]interface{}{
		"names": names,
	}
	if len(changedNames) > 0 {
		payload["changed"] = changedNames
	}
	if len(errors) > 0 {
		payload["errors"] = errors
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("[post-turn-reconcile] names=%s changed=%s errors=%s", strings.Join(names, ","), strings.Join(changedNames, ","), strings.Join(errors, "; "))
	}
	return "[post-turn-reconcile] " + string(data)
}
