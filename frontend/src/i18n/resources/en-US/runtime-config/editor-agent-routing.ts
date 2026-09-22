// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigEditorAgentRouting } from "../../zh-CN/runtime-config/editor-agent-routing";

export const enRuntimeConfigEditorAgentRouting = {
  agentRouting: {
    title: "Subagent / team difficulty routing",
    description:
      "Map task difficulty to provider, model, and reasoning effort for newly created subagents and team tasks. Running tasks are not rewritten.",
    scopes: {
      subagents: "Subagents",
      teams: "Teams",
    },
    inheritTeam: {
      label: "Teams use subagent routing",
      description:
        "When enabled, no independent team policy is stored and team tasks use the subagent difficulty map.",
    },
    enabled: {
      label: "Enable difficulty routing",
      description: "When disabled, execution continues with the parent agent provider and model.",
    },
    defaultDifficulty: {
      label: "Default difficulty",
      description: "Used when a task does not declare a difficulty.",
    },
    inheritParent: {
      label: "Inherit parent when missing",
      description: "Fall back to the parent agent when a level has no provider or model.",
    },
    validateModels: {
      label: "Validate model capabilities",
      description: "Validate model and reasoning effort against the provider model catalog.",
    },
    columns: {
      difficulty: "Difficulty",
      provider: "Provider",
      model: "Model",
      reasoning: "Reasoning",
    },
    difficulties: {
      easy: "Easy",
      normal: "Normal",
      hard: "Hard",
      expert: "Expert",
    },
    taskTypes: {
      title: "Task-type routing",
      description:
        "Override difficulty levels per task type; an empty level means no override and falls back to the difficulty mapping. task_type is a closed set of 12 values, and legacy roles config is mapped on read.",
      add: "Add task type",
      empty: "No task-type overrides yet; routing falls back to the difficulty mapping.",
      keyLabel: "Task type",
      legacyBadge: "Legacy role",
      legacyHint: "Read from legacy roles without a matching enum key, so it is written back to roles.",
      remove: "Remove",
      keys: {
        config: "Config",
        explore: "Explore",
        generate: "Generate",
        implement: "Implement",
        integration: "Integration",
        migrate: "Migrate",
        modify: "Modify",
        refactor: "Refactor",
        security: "Security",
        test: "Test",
        understand: "Understand",
        verify: "Verify",
      },
    },
    defaultBadge: "Default",
    inheritPlaceholder: "Inherit parent",
    modelPlaceholder: "Enter model name",
    noProviders:
      "No enabled providers are available. Enable at least one provider in Provider config first.",
    missingRequiredRoutes:
      "Parent fallback is disabled. Configure both provider and model for: {{difficulties}}.",
    health: {
      teamInherited: "Source: subagent policy",
      configuredCount: "{{count}} configured",
      inheritedCount: "{{count}} inherited",
      errorCount: "{{count}} blocking issues",
      warningCount: "{{count}} checks",
      ready: "Configuration ready",
      errorsPreventRouting:
        "{{count}} current issues would prevent the affected difficulty levels from using this policy. Fix routes marked 'Fix required' before saving.",
      configured: "Independent route",
      providerDefault: "Provider default model",
      routingDisabled: "Routing off",
      parentInherited: "Parent inherited",
      routeError: "Fix required",
      routeWarning: "Check",
      issues: {
        ambiguousProviderAlias:
          "Provider '{{provider}}' is a model alias shared by multiple providers, so runtime resolution is unstable. Select an explicit provider name.",
        disabledProvider:
          "Provider '{{provider}}' is disabled. Runtime execution may fall back to the parent agent or reject this route.",
        missingRequiredRoute:
          "This level does not have a complete provider/model pair and parent fallback is disabled, so runtime execution will reject the route.",
        modelNotListed:
          "Model '{{model}}' is not in supported_models for provider '{{resolvedProvider}}'. The catalog may be non-exhaustive; verify the model name and capability config.",
        modelWithoutProvider:
          "Only model '{{model}}' is configured, so it will be combined with the parent agent provider. Verify that they are compatible.",
        providerAlias:
          "'{{provider}}' resolves as a model alias for provider '{{resolvedProvider}}'. Use the explicit provider name to avoid alias conflicts.",
        providerWithoutModel:
          "Provider '{{provider}}' has no recognized default model. Runtime execution will try the parent model, which may be incompatible with this provider.",
        reasoningUnsupported:
          "The capability declaration for model '{{model}}' does not support reasoning effort '{{effort}}'. Runtime execution will downgrade, clear, or reject it according to the selected policy.",
        unknownProvider:
          "Provider '{{provider}}' was not found. Runtime execution may fall back to the parent agent or reject this route.",
      },
    },
    maxExpertConcurrency: {
      label: "Max expert concurrency",
      description:
        "Limits concurrent expert tasks; a positive number is the limit and -1 means explicitly unlimited (0 is an alias of -1 and is saved as -1).",
    },
    reasoningPolicy: {
      label: "Unsupported reasoning policy",
      description: "ignore clears it, downgrade lowers it, and fail rejects the route.",
    },
    reasoningPolicies: {
      ignore: "Ignore and clear",
      downgrade: "Downgrade automatically",
      fail: "Reject route",
    },
    preview: {
      title: "Effective route preview",
      difficulty: "Task difficulty",
      taskType: "Task type",
      taskTypeAny: "Unspecified",
      taskSubject: "Task subject",
      role: "Task role",
      rolePlaceholder: "For example, researcher",
      goal: "Task goal",
      goalPlaceholder: "Enter the task goal used for difficulty inference",
      run: "Run preview",
      running: "Previewing",
      failed: "Route preview failed",
      documentUnavailable: "The current config draft is not loaded, so route preview cannot run.",
      routingEnabled: "Routing enabled",
      routingDisabled: "Routing disabled",
      provider: "Provider",
      model: "Model",
      reasoning: "Reasoning",
      parent: "Parent agent",
      fallback: "Fallback used",
      routingSources: {
        subagent: "Subagent policy",
        team_independent: "Independent team policy",
        subagent_inherited: "Inherited subagent policy",
      },
      sources: {
        disabled: "Parent passthrough",
        explicit_override: "Explicit override",
        role_override: "Role override",
        task_type_override: "Task-type override",
        difficulty_level: "Difficulty level",
        parent_inherit: "Parent inherited",
        fallback: "Fallback result",
      },
      difficultySources: {
        explicit: "Explicitly declared",
        explicit_promoted: "Explicit promoted",
        inferred: "Inferred from content",
        default: "Default difficulty",
        task_type_floor: "Task-type floor",
        task_type_downgrade: "Task-type downgrade",
      },
      difficulties: {
        easy: "Easy",
        normal: "Normal",
        hard: "Hard",
        expert: "Expert",
      },
      warnings: {
        difficulty_missing_defaulted: "No task difficulty was supplied, so the default difficulty was used.",
        difficulty_invalid_defaulted: "The task difficulty was invalid, so the default difficulty was used.",
        difficulty_promoted_by_heuristic: "Task content triggered a difficulty promotion rule.",
        explicit_provider_override_not_allowed: "The explicit provider is not in the allowlist.",
        explicit_provider_override_denied: "This policy does not allow explicit provider overrides.",
        explicit_model_override_not_allowed: "The explicit model is not in the allowlist.",
        explicit_model_override_denied: "This policy does not allow explicit model overrides.",
        explicit_reasoning_override_denied: "This policy does not allow explicit reasoning overrides.",
        provider_missing_inherited_parent: "No route provider was configured, so the parent provider was inherited.",
        provider_unresolved: "The route provider could not be resolved.",
        provider_fallback_parent: "The provider fell back to the parent agent.",
        model_default_provider: "No route model was configured, so the provider default model was used.",
        model_missing_inherited_parent: "No route model was configured, so the parent model was inherited.",
        model_unsupported: "The model is not present in the provider's known capability catalog.",
        model_fallback_parent: "The model fell back to the parent agent.",
        reasoning_effort_capability_unknown: "The model does not declare reasoning capabilities, so the current effort was retained.",
        reasoning_effort_unsupported_downgraded: "The reasoning effort is unsupported and was downgraded automatically.",
        reasoning_effort_unsupported_downgrade_unavailable: "The reasoning effort is unsupported and no downgrade is available.",
        reasoning_effort_unsupported_ignored: "The reasoning effort is unsupported and was cleared.",
        budget_tokens_capped_by_route: "The task token budget exceeds the route limit and was capped.",
      },
    },
  },
} satisfies DeepStringShape<typeof zhRuntimeConfigEditorAgentRouting>;
