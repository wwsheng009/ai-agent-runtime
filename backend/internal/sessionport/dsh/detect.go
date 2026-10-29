package dsh

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
)

// Detect probes explicit roots, or the standard DSH_HOME/~/.dsh roots when
// none are supplied. A missing default root is not an error; a missing explicit
// root is.
func Detect(roots []string, agent string) ([]sessionport.Instance, error) {
	if value := strings.TrimSpace(agent); value != "" && value != agentID {
		return nil, fmt.Errorf("不支持的 Agent: %s（当前支持 %s）", value, agentID)
	}
	explicit := len(roots) > 0
	if !explicit {
		roots = DefaultRoots()
	}
	seen := map[string]struct{}{}
	instances := make([]sessionport.Instance, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		key := strings.ToLower(root)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if !explicit {
			if _, err := os.Stat(root); err != nil {
				continue
			}
		}
		instance, err := DetectInstance(root, false)
		if err != nil {
			if explicit {
				return nil, err
			}
			continue
		}
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Root < instances[j].Root
	})
	return instances, nil
}
