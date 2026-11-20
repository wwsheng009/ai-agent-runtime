package agentconfig

import (
	"fmt"
	"sort"
	"strings"
)

// crossLayerProviderDeleteBlockers prevents a provider delete from silently
// leaving references behind in a different config file. The common split setup
// keeps providers in providers.yaml while aicli.chat / skills_runtime /
// provider_groups may still live in the main config.yaml; deleting only the
// provider item would leave a dangling default/reference.
//
// The first implementation blocks with an actionable message instead of trying
// to write two files in one transaction. The migration command moves the
// related keys into the same file, after which the normal same-file cleanup
// applies.
func crossLayerProviderDeleteBlockers(target string, deleteSet map[string]struct{}) []ProviderDeleteBlocker {
	current := globalConfig
	if current == nil || current.ConfigMergeMode != MergeModeOn || len(deleteSet) == 0 {
		return nil
	}
	targetPath := normalizeLayerPath(target)
	if targetPath == "" {
		return nil
	}

	blockers := make([]ProviderDeleteBlocker, 0, 4)
	addIfOtherLayer := func(provider, key, code, message string) {
		provider = strings.TrimSpace(provider)
		if provider == "" {
			return
		}
		if _, deleting := deleteSet[strings.ToLower(provider)]; !deleting {
			return
		}
		origin := current.originFileFor(key)
		if origin == "" || normalizeLayerPath(origin) == targetPath {
			return
		}
		blockers = append(blockers, ProviderDeleteBlocker{
			Provider: provider,
			Code:     code,
			Message:  fmt.Sprintf(message, origin),
		})
	}

	addIfOtherLayer(current.Providers.DefaultProvider, "providers.default_provider",
		"default_provider_other_layer",
		"provider is providers.default_provider in %s; switch the default provider or run 'aicli config split-providers' first")
	if current.AICLI != nil && current.AICLI.Chat != nil {
		addIfOtherLayer(current.AICLI.Chat.DefaultProvider, "aicli.chat.default_provider",
			"chat_default_provider_other_layer",
			"provider is aicli.chat.default_provider in %s; switch the chat default provider first")
	}
	if current.SkillsRuntime != nil {
		addIfOtherLayer(current.SkillsRuntime.GatewayProviderName, "skills_runtime.gateway_provider_name",
			"gateway_provider_other_layer",
			"provider is skills_runtime.gateway_provider_name in %s; switch or replace it first")
	}

	groupRefs := providerGroupNamesByProvider(current.ProviderGroups)
	if origin := current.originFileFor("provider_groups"); origin != "" && normalizeLayerPath(origin) != targetPath {
		for name, groups := range groupRefs {
			if _, deleting := deleteSet[strings.ToLower(name)]; !deleting {
				continue
			}
			sort.Strings(groups)
			blockers = append(blockers, ProviderDeleteBlocker{
				Provider:   name,
				Code:       "provider_group_reference_other_layer",
				Message:    fmt.Sprintf("provider is referenced by provider_groups in %s; migrate provider_groups or remove the reference first", origin),
				References: append([]string(nil), groups...),
			})
		}
	}

	return dedupeProviderDeleteBlockers(blockers)
}
