export const enUS = {
  common: {
    actions: {
      close: "Close",
      cancel: "Cancel",
      confirm: "Confirm",
      reset: "Reset",
      refresh: "Refresh",
      copied: "Copied",
      copyLink: "Copy link",
      clearAll: "Clear all",
      clearSearch: "Clear search",
      copyValue: "Copy value",
      copyMetadata: "Copy metadata",
      copyPreview: "Copy preview",
      copyFields: "Copy fields",
      copyJson: "Copy JSON",
    },
    loading: {
      page: "Loading page...",
      details: "Loading details...",
      logs: "Loading logs...",
    },
    states: {
      justNow: "just now",
      none: "None",
      live: "Live",
      online: "Online",
      offline: "Offline",
      connecting: "Connecting",
      reconnecting: "Reconnecting",
      streamError: "Stream error",
      idle: "Idle",
      fileDetected: "File detected",
      waitingForLogFile: "Waiting for log file",
      generated: "Generated",
      notGenerated: "Not generated",
      synced: "Synced",
      unsynced: "Unsynced",
      enabled: "Enabled",
      disabled: "Disabled",
    },
  },
  landing: {
    header: {
      productLabel: "Agent Workspace Platform",
      productName: "AI Agent Runtime",
      productTour: "Product tour",
      openWorkspace: "Open workspace",
    },
    hero: {
      eyebrow: "Browser-native AI workspace for research, execution, and review",
      rotatingWord1: "Deep Research",
      rotatingWord2: "Agent Workspaces",
      rotatingWord3: "Artifact Reviews",
      rotatingWord4: "Runtime Teams",
      titlePrefix: "Research, orchestrate, and ship",
      titleSuffix: "from one browser-native workspace.",
      body:
        "AI Agent Runtime brings live threads, runtime events, artifact evidence, and teammate coordination into a single product surface.",
      primaryCta: "Enter workspace",
      secondaryCta: "See product highlights",
      unifiedFlowTitle: "Unified flow",
      unifiedFlowBody:
        "Move from prompt to action to review without jumping across disconnected tools or hidden execution surfaces.",
      teamReadyTitle: "Team-ready workspace",
      teamReadyBody:
        "Keep thread context, runtime teams, and operational detail in the same shell so handoffs stay readable.",
      verifiableOutputTitle: "Verifiable output",
      verifiableOutputBody:
        "Inspect artifacts, stream runtime events, and keep the evidence next to the work that produced it.",
      snapshotEyebrow: "Product snapshot",
      snapshotTitle: "One shell, three visible layers",
      snapshotBody:
        "Keep discovery, active work, and runtime evidence legible at the same time: the product story up front, the active thread in the middle, and the operational detail around it.",
      productSiteTitle: "Product site",
      productSiteBody:
        "A clear product narrative explains what the workspace does, where it fits, and why teams can trust the output.",
      workspaceEntryTitle: "Workspace entry",
      workspaceEntryBody:
        "Start with /workspace and land directly in an active thread, so the app feels immediate instead of route-driven.",
      runtimeEvidenceTitle: "Runtime evidence",
      runtimeEvidenceBody1: "Chat turns and replies stay attached to the thread.",
      runtimeEvidenceBody2: "History sync keeps the workspace aligned with the session.",
      runtimeEvidenceBody3: "Runtime streams expose tool calls, routes, and artifacts.",
    },
    deferred: {
      productHighlights: "Product highlights",
    },
    community: {
      eyebrow: "Community",
      title: "Bring teams, evidence, and runtime control into one flow",
      subtitle:
        "AI Agent Runtime is built for teams that want a product-grade workspace without giving up operational detail. The landing page introduces the flow; the workspace carries it through execution.",
      pillarsBadge: "Product pillars",
      layersBadge: "three visible layers",
      pillars: {
        focusedWork: {
          title: "Focused work",
          summary:
            "Start with a clear thread, gather only the context that matters, and keep the task moving without losing the original request.",
        },
        sharedVisibility: {
          title: "Shared visibility",
          summary:
            "Keep teammates, runtime events, and task dispatch readable from one operating surface instead of scattering work across tabs.",
        },
        inspectableResults: {
          title: "Inspectable results",
          summary:
            "Review artifacts, receipts, and streamed execution detail without losing the thread that explains why the work happened.",
        },
      },
      readyBadge: "Ready to start",
      readyTitle:
        "Open the workspace and carry the full thread from request to review.",
      readyPoint1:
        "Stay inside one operating surface for prompts, runtime context, team coordination, and artifact review.",
      readyPoint2:
        "Use the product tour first, then jump directly into the active workspace when you are ready to act.",
      launchWorkspace: "Launch workspace",
      browseHighlights: "Browse product highlights",
    },
    caseStudy: {
      eyebrow: "Product highlights",
      title: "See how the workspace turns agent work into something reviewable",
      subtitle:
        "Each card maps to a real surface in AI Agent Runtime: active threads, runtime teams, streamed execution, and artifact detail that stays attached to the work.",
      exploreInWorkspace: "Explore in workspace",
      cards: {
        liveExecution: {
          label: "Live execution",
          title: "Follow the full turn lifecycle in one workspace",
          description:
            "Trace a live agent turn from submit to completion while session history and runtime events keep feeding the same thread.",
        },
        teamCoordination: {
          label: "Team coordination",
          title:
            "Coordinate multiple runtime teammates from the same control rail",
          description:
            "Keep multi-team summaries, teammate readiness, and dispatch detail visible without leaving the working thread.",
        },
        artifactDetail: {
          label: "Artifact detail",
          title: "Inspect outputs like a product surface, not a JSON dump",
          description:
            "Switch between source and preview without losing the thread, so evidence and output stay close to the messages that produced them.",
        },
        agentReasoning: {
          label: "Agent reasoning",
          title: "See how the assistant moved from plan to execution",
          description:
            "Map planning, routing, orchestration, and tool events into a readable message stream with attached receipts.",
        },
        operationalClarity: {
          label: "Operational clarity",
          title: "Keep the runtime legible without hiding the underlying system",
          description:
            "Expose chat, session, and runtime capabilities through a clear product surface with explicit operational controls.",
        },
        productExperience: {
          label: "Product experience",
          title: "Keep the website and workspace visually connected",
          description:
            "Treat the landing page and workspace as connected surfaces: one explains the value, the other lets teams act on it.",
        },
      },
    },
    sandbox: {
      eyebrow: "Runtime environment",
      title: "A real execution environment behind every agent turn",
      subtitle:
        "Agents need files, commands, session state, and runtime feedback to do useful work. AI Agent Runtime keeps those surfaces explicit, so teams can inspect what happened instead of guessing after the fact.",
      terminalLabel: "Runtime terminal",
      featuresLabel: "Runtime features",
      featuresTitle:
        "Explicit control surfaces for threads, tools, artifacts, and teams.",
      featuresBody1:
        "The workspace is designed to feel operational, not decorative. You can send turns, inspect history, follow runtime events, and review outputs from the same place.",
      featuresBody2:
        "Runtime APIs stay visible where that matters, but the product surface stays calm enough for daily use by engineers, operators, and reviewers.",
      surfacesLabel: "Available surfaces",
      tags: {
        shellAccess: "Shell access",
        workspaceFiles: "Workspace files",
        sessionHistory: "Session history",
        runtimeSse: "Runtime SSE",
        artifactPreview: "Artifact preview",
      },
    },
    skills: {
      eyebrow: "Core capabilities",
      title: "Capabilities that stay visible while agents work",
      subtitle:
        "AI Agent Runtime keeps the workflow legible as work moves from understanding to coordination to delivery. The same workspace holds the thread, the supporting context, and the runtime evidence.",
      ladderLabel: "Capability ladder",
      stageLabel: "Stage {{index}}",
      columns: {
        understand: {
          title: "Understand",
          description:
            "Start with the thread, repository, and supporting evidence that matter, so the workspace stays focused on the job instead of dumping raw context everywhere.",
          items: [
            "Repository scan",
            "Targeted reads",
            "Evidence-first context",
          ],
        },
        coordinate: {
          title: "Coordinate",
          description:
            "Keep active tasks, teammate state, and runtime signals aligned so the whole team can see what is blocked, what is running, and what needs attention.",
          items: ["Thread planning", "Team handoff", "Runtime checkpoints"],
        },
        deliver: {
          title: "Deliver",
          description:
            "Edit, inspect, and verify from the same workspace, with artifacts and execution detail staying next to the messages that created them.",
          items: ["Code changes", "Artifact previews", "Verification loops"],
        },
      },
    },
    whatsNew: {
      eyebrow: "Why it works",
      title: "Everything needed to move from prompt to verified output",
      subtitle:
        "The landing page now explains the product in user terms: how you enter the workspace, how execution stays visible, and how artifacts remain tied to the thread that produced them.",
      cards: {
        productShell: {
          label: "Product shell",
          title: "Structured for first-time understanding",
          description:
            "A clear sequence of hero, highlights, capabilities, runtime detail, and CTA helps explain the product before a user enters the workspace.",
        },
        workspaceEntry: {
          label: "Workspace entry",
          title: "A cleaner path into active work",
          description:
            "Primary calls to action now land on /workspace, reducing route noise and getting users into the active work surface faster.",
        },
        runtimeSignal: {
          label: "Runtime",
          title: "Live signals stay attached to the thread",
          description:
            "Prompt submission, session history sync, and runtime streaming keep the thread current while work is still in flight.",
        },
        artifactEvidence: {
          label: "Artifacts",
          title: "Evidence remains inspectable",
          description:
            "Planning, orchestration, route, subagent, and tool payloads stay attached back into inspectable artifacts and receipts.",
        },
        conversation: {
          label: "Conversation",
          title: "Thread-first workspace flow",
          description:
            "The message surface keeps focusing on the selected thread instead of scattering execution state across disconnected screens.",
        },
        extension: {
          label: "Extension",
          title: "Ready to grow with the runtime",
          description:
            "The structure leaves room for richer markdown, deeper artifact review, and more advanced team controls without changing the core flow.",
        },
      },
    },
    footer: {
      quote:
        "\"Keep the thread clear, the runtime visible, and the output reviewable.\"",
      body:
        "AI Agent Runtime gives teams a product-grade entry point into active work, with threads, runtime events, and artifacts kept close enough to support real review and handoff.",
      openWorkspace: "Open the workspace",
      viewProductHighlights: "View product highlights",
    },
  },
  workspace: {
    shell: {
      newChatEyebrow: "New workspace chat",
      newChatTitle:
        "Start from a blank thread, then let runtime state attach as work begins.",
      newChatBody:
        "This route now behaves like a real new chat entry without depending on seeded mock threads.",
      loadingSettingsPanel: "Loading settings panel...",
      loadingArtifactPanel: "Loading artifact panel...",
      loadingArtifactDetails: "Loading artifact details...",
    },
    topbar: {
      home: "Home",
      newChat: "New chat",
      logs: "Logs",
      runtime: "Runtime",
      settings: "Settings",
      showFiles: "Show files",
      hideFiles: "Hide files",
      newThreadTitle: "New chat",
      newThreadSubtitle:
        "Start a thread, then let runtime state attach as work begins.",
      threadTransport: {
        live: "Live runtime",
        error: "Runtime degraded",
        seeded: "Seeded preview",
      },
      threadStatus: {
        sessionAttached: "Session attached",
        previewThread: "Preview thread",
        newThread: "New thread",
      },
      subtitle: {
        needsRestoreWithSession: "Session {{sessionId}} needs restore attention",
        needsRestore: "Runtime restore needs attention",
        viaSource: "{{transportLabel}} via {{source}}",
        session: "Session {{sessionId}}",
      },
    },
    sidebar: {
      workspaceLabel: "Workspace",
      appName: "AI Agent Runtime",
      refreshRuntimeTeams: "Refresh runtime teams",
      openSettings: "Open settings",
      startNewChat: "Start new chat",
      searchPlaceholder: "Search threads",
      sections: {
        chats: "Local chats",
        sessions: "Sessions",
        runtime: "Runtime overview",
      },
      threadStatuses: {
        review: "Waiting for review",
        draft: "Draft thread",
        active: "Active thread",
      },
      sessionStatuses: {
        error: "Session sync error",
        restored: "Restored session",
        attached: "Attached runtime session",
        pending: "No runtime session attached yet",
      },
      sessionDetails: {
        pending: "No runtime session attached yet.",
        error:
          "The session exists, but the latest sync failed and needs another restore attempt.",
        restored: "Recovered from runtime session history and ready to continue.",
        attached: "Attached to a live runtime session from the active workspace flow.",
      },
      emptyChats: {
        search: "No local chats match the current search.",
        default:
          "Local-only chats will appear here before runtime session attachment.",
      },
      emptySessions: {
        search: "No sessions match the current search.",
        default:
          "Recoverable runtime sessions will appear here after loading.",
      },
      runtimeStats: {
        sessions: "{{count}} sessions",
        recoverable: "{{count}} recoverable",
        pending: "{{count}} pending",
        syncing: "syncing",
      },
      runtimeTeamsUnavailable: "No runtime teams available.",
      openRuntimeTeamDetails: "Open runtime team details",
      backendConfigPage: "Backend config page",
      active: "{{count}} active",
      unknown: "unknown",
    },
    composer: {
      transport: {
        live: "live runtime",
        error: "runtime error",
        seeded: "seeded",
      },
      sessionState: {
        attached: "session attached",
        new: "new session",
      },
      placeholder: {
        newThread:
          "Ask the workspace to inspect, build, review, or coordinate the next step...",
        thread:
          "Ask the workspace to research, change, verify, or coordinate the next step...",
      },
      submit: {
        stopResponse: "Stop response",
        startNewThread: "Start new thread",
        sendTurn: "Send turn",
        startThread: "Start thread",
      },
      promptTips: "prompt tips",
      promptTipsMenuTitle: "Prompt tips",
      responseActive: "response active",
      provider: "Provider",
      model: "Model",
      loadingModels: "loading models",
      modelCatalogUnavailable: "model catalog unavailable",
      runtimeDefaultModel: "runtime default model",
      modelWithName: "model {{model}}",
      shortcuts: "Ctrl/Cmd + Enter",
      stop: "stop",
      submitShort: "submit",
      filesCount: "{{count}} files",
    },
  },
  runtimeConfig: {
    page: {
      badge: "Runtime config",
      independentPage: "Independent page",
      title: "Backend config workspace",
      description:
        "Manage runtime backend configuration separately, with a dedicated entry for providers.",
      backToWorkspace: "Back to workspace",
      logs: "Logs",
    },
    editor: {
      title: "Backend config workspace",
      description:
        "Manage runtime backend configuration separately, with dedicated forms first and YAML as the fallback.",
      independentBadge: "Independent backend config page",
      unsavedBadge: "Unsaved draft",
      usage: {
        title: "How to use it",
        body:
          "The structured config tree is gone. Use the dedicated controls for common config areas; switch to YAML mode when you need to fill in fields that are not covered.",
      },
      currentFocusPrefix: "Current focus:",
      sourceFocus: "Source fallback editor",
      structuredFocus: "Dedicated config mode",
      controls: {
        reload: "Reload",
        preview: "Generate preview",
        save: "Save to file",
        restartWithEffect: "Restart to apply",
        restart: "Restart runtime-server",
      },
      panels: {
        editorTitle: "Config editor",
        editorDescription: "Switch config areas on the left and edit with dedicated controls on the right.",
        modeTitle: "Config areas",
        modeDescription: "Switch between dedicated editors and source mode.",
        summaryTitle: "Draft summary",
        summaryDescription:
          "Shows draft size, preview state, and the key configuration summary.",
      },
      source: {
        title: "Raw YAML draft",
        preserveComments: "Preserve comments",
        lines: "Lines",
        chars: "Characters",
        helpTitle: "YAML help",
        helpBody:
          "When dedicated controls do not yet cover a config area, or when you need to preserve comments and formatting exactly, edit the source directly here.",
      },
      preview: {
        title: "Change preview",
        description: "Inspect the text diff before saving.",
        added: "Added",
        removed: "Removed",
        latest: "Latest preview",
        latestWithCount: "Latest preview, {{count}} lines",
        expired: "Preview expired",
        needsRestart: "Restart required after save",
        helpTitle: "Preview help",
        helpFresh: "This diff matches the latest draft and can be saved directly.",
        helpStale:
          "The draft changed after the preview was generated; regenerate the preview.",
      },
      sticky: {
        unsaved: "Unsaved draft",
        hint: "Preview the diff first, then save it to the current runtime config document.",
        previewButton: "Preview first",
        saveButton: "Save now",
        saveAndRestartButton: "Save and restart",
      },
      modes: {
        providers: {
          label: "Provider config",
          description: "Manage the main provider config with tables and popup forms.",
        },
        providerGroups: {
          label: "Provider Groups",
          description: "Maintain routing groups, failover, truncation strategy, and member lists.",
        },
        networkProxy: {
          label: "Network proxy",
          description: "Maintain runtime upstream HTTP/HTTPS/SOCKS5 proxies and no_proxy.",
        },
        auth: {
          label: "Auth config",
          description: "Maintain JWT, admin, and Access Key authentication settings.",
        },
        routing: {
          label: "Routing config",
          description: "Maintain the routing root config and route ordering.",
        },
        rateLimit: {
          label: "Rate Limit",
          description: "Maintain root rate limiting, API key rules, and path overrides.",
        },
        resourceManager: {
          label: "Resource Manager",
          description:
            "Maintain the resource manager switch, default algorithms, health checks, and stats retention.",
        },
        providerQueue: {
          label: "Provider Queue",
          description:
            "Maintain provider-level slots, overflow strategy, heartbeat waits, and override rules.",
        },
        concurrency: {
          label: "Concurrency",
          description:
            "Maintain the global concurrency cap, queue parameters, and provider-level limits.",
        },
        retry: {
          label: "Retry",
          description: "Maintain the global retry default, enhancement strategy, and rule order.",
        },
        monitor: {
          label: "Monitor",
          description:
            "Maintain metrics, tracing, alert, pprof, and memory monitoring config.",
        },
        websocket: {
          label: "WebSocket",
          description:
            "Maintain responses / realtime WebSocket and bridge-related config.",
        },
        circuitBreaker: {
          label: "Circuit Breaker",
          description:
            "Maintain failure thresholds, time windows, and half-open recovery parameters.",
        },
        transformer: {
          label: "Transformer",
          description:
            "Maintain HTTPTransformer switches and request/response body modifiers.",
        },
        source: {
          label: "Raw YAML",
          description:
            "Keep comments, blank lines, and the original layout as the fallback editor mode.",
        },
      },
      counts: {
        providers: "{{count}} providers",
        providerGroups: "{{count}} groups",
        routes: "{{count}} routes",
        rules: "{{count}} rules",
        providerQueueProviders: "{{count}} provider overrides",
        concurrencyProviders: "{{count}} provider limits",
        retryRules: "{{count}} rules",
        transformerModifiers: "{{count}} modifiers",
        lines: "{{count}} lines",
        enabledProviders: "{{count}} enabled",
        changeCount: "{{count}} changes",
        appliedCount: "{{count}} applied",
        pathCount: "{{count}} paths",
        hiddenCount: "{{count}} more hidden.",
      },
      proxySummary: {
        fallback: "Environment variables / direct connection",
      },
      saveStatus: {
        defaultPath: "current runtime config document",
        separator: ", ",
        basic: "Wrote config back to {{targetPath}}.",
        detailed: "Wrote config back to {{targetPath}}. {{details}}.",
        applied: "{{count}} changes applied immediately",
        hotReload: "{{count}} changes support hot reload",
        restart: "{{count}} changes still require a restart",
        inactive: "{{count}} changes do not affect runtime-server yet",
      },
      status: {
        switchingMode: "Syncing draft and switching mode...",
      },
      scopes: {
        request: "request",
        response: "response",
      },
      messages: {
        loadServiceStatusFailed: "Failed to load service status",
        loadBackendConfigFailed: "Failed to load backend config",
        syncedToSource: "Synced the YAML view from the current config-area draft.",
        syncedToStructured: "Synced the dedicated config view from the current YAML draft.",
        switchSyncFailed: "Failed to sync draft before switching edit mode",
        reloadConfirm: "Reloading will discard unsaved draft changes. Continue?",
        reloadedFromDisk: "Reloaded backend config from disk.",
        reloadFailed: "Failed to reload backend config",
        saveFailed: "Failed to save backend config",
        previewFailed: "Failed to generate preview",
        restartConfirm: "runtime-server will briefly disconnect and restart. Continue?",
        runtimeServerReconnected: "runtime-server is connected again.",
        restartRequested: "Requested a runtime-server restart; check service status again shortly.",
        restartFailed: "Failed to restart runtime-server",
        saveAndRestartConfirm:
          "Saving will automatically restart runtime-server. Changes that require a restart will take effect in the new process. Continue?",
        saveAndRestartDone:
          "Saved the config and restarted runtime-server. Restart-required changes are now in the new process.",
        defaultProviderSet: 'Switched the default provider draft to "{{name}}".',
        globalProxyUpdated: "Updated the global proxy draft: {{summary}}.",
        globalProxyCleared:
          "Cleared the global proxy draft; runtime will fall back to environment variables or direct connection.",
        providerCreated: 'Created provider "{{name}}" draft.',
        providerUpdated: 'Updated provider "{{name}}" draft.',
        relatedProviderGroups: " It is still referenced by {{count}} provider groups.",
        confirmDeleteProvider: 'Delete provider "{{name}}"?{{relatedHint}}',
        providerDeleted: 'Removed provider "{{name}}" from the draft.',
        providerGroupCreated: 'Created provider group "{{name}}" draft.',
        providerGroupUpdated: 'Updated provider group "{{name}}" draft.',
        confirmDeleteProviderGroup: 'Delete provider group "{{name}}"?',
        providerGroupDeleted: 'Removed provider group "{{name}}" from the draft.',
        routeCreated: "Created a routing route draft.",
        routeUpdated: "Updated Route #{{index}} draft.",
        confirmDeleteRoute: "Delete Route #{{index}}?",
        routeDeleted: "Removed Route #{{index}} from the draft.",
        routeMovedUp: "Moved Route #{{index}} up.",
        routeMovedDown: "Moved Route #{{index}} down.",
        apiKeyLimitCreated: "Created API key rate-limit rule draft.",
        apiKeyLimitUpdated: "Updated API key rule #{{index}} draft.",
        confirmDeleteApiKeyLimit: "Delete API key rule #{{index}}?",
        apiKeyLimitDeleted: "Removed API key rule #{{index}} from the draft.",
        pathLimitCreated: 'Created path rate-limit rule "{{path}}".',
        pathLimitUpdated: 'Updated path rate-limit rule "{{path}}".',
        confirmDeletePathLimit: 'Delete path rule "{{path}}"?',
        pathLimitDeleted: 'Removed path rate-limit rule "{{path}}" from the draft.',
        providerQueueCreated: 'Created provider queue override "{{provider}}".',
        providerQueueUpdated: 'Updated provider queue override "{{provider}}".',
        confirmDeleteProviderQueueProvider:
          'Delete provider queue override "{{provider}}"?',
        providerQueueDeleted:
          'Removed provider queue override "{{provider}}" from the draft.',
        concurrencyLimitCreated: 'Created provider concurrency limit "{{provider}}".',
        concurrencyLimitUpdated: 'Updated provider concurrency limit "{{provider}}".',
        confirmDeleteConcurrencyLimit:
          'Delete provider concurrency limit "{{provider}}"?',
        concurrencyLimitDeleted:
          'Removed provider concurrency limit "{{provider}}" from the draft.',
        retryRuleCreated: 'Created retry rule "{{name}}".',
        retryRuleUpdated: 'Updated retry rule "{{name}}".',
        confirmDeleteRetryRule: "Delete Retry rule #{{index}}?",
        retryRuleDeleted: "Removed Retry rule #{{index}} from the draft.",
        retryRuleMovedUp: "Moved Retry rule #{{index}} up.",
        retryRuleMovedDown: "Moved Retry rule #{{index}} down.",
        transformerModifierCreated: "Created {{scopeLabel}} transformer modifier.",
        transformerModifierUpdated:
          "Updated {{scopeLabel}} transformer modifier #{{index}}.",
        confirmDeleteTransformerModifier:
          "Delete {{scopeLabel}} modifier #{{index}}?",
        transformerModifierDeleted:
          "Removed {{scopeLabel}} modifier #{{index}} from the draft.",
        transformerModifierMovedUp:
          "Moved {{scopeLabel}} modifier #{{index}} up.",
        transformerModifierMovedDown:
          "Moved {{scopeLabel}} modifier #{{index}} down.",
      },
      validation: {
        defaultPath: "current runtime config document",
        providerNameRequired: "Provider name is required.",
        providerExists: 'Provider "{{name}}" already exists. Choose another name.',
        providerInvalid: "Invalid provider config.",
        providerGroupExists: 'Provider group "{{name}}" already exists. Choose another name.',
        providerGroupInvalid: "Invalid provider group config.",
        routeInvalid: "Invalid route config.",
        apiKeyLimitInvalid: "Invalid API key rate-limit rule.",
        pathLimitInvalid: "Invalid path rate-limit rule.",
        pathLimitExists: 'Path rule "{{path}}" already exists.',
        providerQueueInvalid: "Invalid provider queue override config.",
        concurrencyProviderExists: 'Provider "{{provider}}" already has a concurrency limit.',
        concurrencyLimitRequired: "Concurrency limit is required.",
        retryRuleNameRequired: "Rule name is required.",
        retryRuleExists: 'Rule "{{name}}" already exists.',
        transformerModifierInvalid: "Invalid transformer modifier config.",
        previewWarning: "This is a preview result and has not been written to disk yet.",
      },
      impact: {
        previewTitle: "Preview impact",
        runtimeTitle: "Runtime impact",
        previewDescription:
          "This draft shows how many changes would apply immediately and how many would still require a restart.",
        runtimeDescription:
          "This shows the actual effect of the most recently saved config on the current runtime-server.",
        previewBadge: "Based on preview",
        savedBadge: "Latest saved result",
        changedCount: "{{count}} changes",
        needsRestart: "Includes restart-required items",
        noRestart: "No restart required",
        appliedCount: "{{count}} applied immediately",
        restartToApply: "Restart to apply config",
        stats: {
          changed: "Changed",
          hotReload: "Hot reload",
          restart: "Restart",
          inactive: "Inactive",
        },
        details: {
          changed: "Total number of paths matched by this config diff.",
          hotReloadApplied: "These paths have already been applied in the current process.",
          hotReload: "These paths support immediate hot reload.",
          restart: "These paths are startup-injected config and still require a restart.",
          inactive: "These paths do not affect the runtime-server process right now.",
        },
        paths: {
          applied: "Applied immediately",
          restart: "Requires restart",
          hotReload: "Hot reload",
          inactive: "Not currently active",
          emptyApplied: "This save did not apply any runtime paths immediately.",
          emptyRestart: "This change did not match any restart-required paths.",
          emptyHotReload: "This change did not match any hot-reload paths.",
          emptyInactive: "This change did not match any inactive paths.",
        },
        warningsTitle: "Backend notes",
      },
      cards: {
        provider: "Provider",
        providerDefault: "Default provider: {{defaultProvider}}",
        providerEmpty: "No default provider set yet",
        providerGroup: "Provider Group",
        providerGroupSummary: "{{count}} member references",
        providerGroupEmpty: "No provider group created yet",
        auth: "Auth",
        authAdminEnabled: "Admin auth is enabled",
        authAdminDisabled: "Admin auth is disabled",
        routing: "Routing",
        routingStrategy: "strategy: {{strategy}}",
        routingEmpty: "routing.strategy is not set yet",
        rateLimit: "Rate Limit",
        rateLimitSummary:
          "{{apiKeyCount}} API key rules · {{pathCount}} path rules",
        rateLimitEmpty: "No override rules configured yet",
        resourceManager: "Resource Manager",
        resourceManagerSummary:
          "{{groupAlgorithm}} / {{providerAlgorithm}} / {{keyAlgorithm}}",
        resourceManagerEmpty: "Still using the legacy load-balancing path",
        providerQueue: "Provider Queue",
        providerQueueSummary:
          "{{count}} provider overrides · default concurrency {{defaultMaxConcurrency}}",
        providerQueueHeartbeat: "wait_heartbeat {{interval}}",
        providerQueueEmpty: "No provider-level overrides configured yet",
        concurrency: "Concurrency",
        concurrencySummary:
          "global {{maxConcurrentRequests}} · {{count}} provider limits",
        concurrencyQueueTimeout: "queue_timeout {{queueTimeout}}",
        concurrencyEmpty: "No provider-level concurrency limits configured yet",
        retry: "Retry",
        retrySummary: "{{count}} rules · default {{defaultMaxRetries}} retries",
        retryEmpty: "No retry rules configured yet",
        monitor: "Monitor",
        monitorSummary: "metrics {{metricsState}} · tracing {{tracingState}}",
        monitorEmpty: "Metrics and tracing are both disabled",
        websocket: "WebSocket",
        websocketSummary:
          "responses {{responsesState}} · realtime {{realtimeState}}",
        websocketEmpty: "WebSocket is disabled globally",
        circuitBreaker: "Circuit Breaker",
        circuitBreakerSummary:
          "open_timeout {{openTimeout}} · failure_rate {{failureRate}}",
        circuitBreakerEmpty: "No circuit-breaker timing parameters configured yet",
        transformer: "Transformer",
        transformerSummary:
          "{{requestCount}} request modifiers · {{responseCount}} response modifiers",
        transformerHighPerf: "High-performance transform mode is enabled",
        transformerEmpty: "No body modifiers configured yet",
        configFile: "Config file",
        configFileLoaded: "{{path}} · {{timestamp}}",
        configFileEmpty: "Not loaded yet",
        runtimeServer: "runtime-server",
        runtimeServerEmpty: "No listen information returned yet",
      },
      summary: {
        lines: "Lines",
        providers: "Providers",
        groups: "Groups",
        proxy: "Proxy",
        routes: "Routes",
        limits: "Limits",
        resourceManager: "Resource manager",
        providerQueue: "Queue",
        concurrency: "Concurrency",
        retry: "Retry",
        monitor: "Monitor",
        websocket: "WebSocket",
        circuitBreaker: "Breaker",
        transformer: "Transformer",
        preview: "Preview",
      },
      loadingCard: {
        title: "Loading {{label}} editor...",
        body:
          "Only the config area you are currently viewing is loaded on demand; unopened editors do not enter the initial page bundle.",
      },
    },
  },
  settings: {
    dialog: {
      eyebrow: "Workspace settings",
      title: "Frontend workspace settings",
      description:
        "This panel only stores local frontend preferences. Backend config.yaml has moved to the dedicated Runtime Config page.",
      backendConfig: "Backend config",
      resetFrontendDefaults: "Reset frontend defaults",
      close: "Close settings",
      localStorageFooter:
        "Frontend settings are written to the current browser localStorage immediately. Use the Runtime Config page for backend configuration. Open this panel again with Ctrl/Cmd + ,.",
    },
    sections: {
      appearance: {
        label: "Appearance",
        description: "Accent, theme, fonts, and motion",
      },
      workspace: {
        label: "Workspace",
        description: "Layout and file bar behavior",
      },
      chat: {
        label: "Chat defaults",
        description: "Provider, model, and reasoning effort",
      },
      notifications: {
        label: "Notifications",
        description: "Desktop alerts and permissions",
      },
      about: {
        label: "About",
        description: "Runtime summary and local storage",
      },
    },
    localization: {
      title: "Language and region",
      description: "Controls interface language, date/time, and relative time formats.",
      system: "Follow system",
      simplifiedChinese: "Simplified Chinese",
      english: "English",
    },
    appearance: {
      theme: "Theme",
      themeDescription:
        "Controls the entire frontend color mode, including workspace, settings, and landing page.",
      themeApplied: "Applied",
      currentlySetTo: "Currently set to",
      themeSystemResolved: "Follow system (currently resolves to {{resolved}})",
      themeOptions: {
        system: {
          label: "Follow system",
          description: "Listen to the system color preference and sync automatically when it changes.",
        },
        light: {
          label: "Light",
          description: "Best for daytime use, documentation, and bright environments.",
        },
        dark: {
          label: "Dark",
          description: "Best for terminal-style workflows, nights, and low light.",
        },
      },
      accent: "Accent",
      accentDescription:
        "Affects the settings dialog, primary action buttons, and any workspace surfaces wired to variables.",
      accentOptions: {
        gold: {
          label: "Amber relay",
          description: "Keep the current gold-highlighted workspace tone.",
        },
        cyan: {
          label: "Cool signal",
          description: "Switch the main accent to a cooler system-state tone.",
        },
        violet: {
          label: "Route focus",
          description: "Use a violet accent closer to routing and planning semantics.",
        },
      },
      fontFamily: "Font family",
      fontFamilyDescription:
        "Switch the typeface consistently between interface body copy and code surfaces.",
      bodyFont: "Interface and body",
      bodyFontDescription:
        "Applies to the landing page, workspace, settings panel, and serif display headings.",
      codeFont: "Code and logs",
      codeFontDescription:
        "Applies to code blocks, log JSON previews, and monospace editor surfaces.",
      fontFamilyOptions: {
        system: {
          label: "System UI",
          description: "Segoe UI / Helvetica Neue / system-ui",
          sample: "Operational detail stays readable during long sessions.",
        },
        humanist: {
          label: "Readable humanist",
          description: "Trebuchet MS / Verdana / Palatino",
          sample: "Comfortable for dense workspace copy and settings text.",
        },
        editorial: {
          label: "Editorial modern",
          description: "Aptos / Cambria / Georgia",
          sample: "Softer rhythm for landing copy and long-form reading.",
        },
      },
      codeFontOptions: {
        jetbrains: {
          label: "JetBrains stack",
          description: "JetBrains Mono / Cascadia Code / Consolas",
          sample: 'const traceId = receipts.latest()?.trace_id ?? "";',
        },
        cascadia: {
          label: "Cascadia stack",
          description: "Cascadia Code / JetBrains Mono / Consolas",
          sample: "await runtime.follow({ requestId, sessionId });",
        },
        classic: {
          label: "Classic console",
          description: "Consolas / SFMono / Menlo / Monaco",
          sample: 'if (event.level === "error") return halt(event);',
        },
      },
      size: "Size",
      sizeDescription:
        "Controls the base rhythm of the entire site; chat and code sizes override their own high-frequency reading areas.",
      sizeHint: "You can type an exact size directly, in the range {{min}}-{{max}}px.",
      customPixels: "Custom pixel value",
      restoreDefaultWithValue: "Restore default {{value}}",
      motion: "Motion",
      motionDescription:
        "Useful for remote desktops, screen recordings, or when you want a more restrained interface.",
      reducedMotion: "Reduce motion",
      reducedMotionDescription:
        "Turns off pulses, floating effects, and most transitions, and makes scrolling immediate.",
      preview: "Live preview",
      previewDescription:
        "The sample below refreshes immediately with your current font and size choices.",
      workspaceSample: "Workspace reading sample",
      chatSample: "Chat reading sample",
      codeSample: "Code and font stack preview",
      previewSampleText: "Operational detail stays readable in long sessions.",
      previewWorkspaceBody:
        "Task threads, runtime context, and artifacts stay readable in this area.",
      currentUIStack: "Current interface font stack:",
      currentSerifStack: "Current heading serif stack:",
      currentCodeStack: "Current code font stack:",
      currentScope: "Current scope",
      immediate: "Immediate",
      browserPersistence: "Browser persistence",
      uiTypography: "Interface and body",
      codeTypography: "Code and logs",
    },
    workspace: {
      density: "Interface density",
      densityDescription:
        "Controls the vertical spacing of the sidebar, message area, and input area.",
      densityOptions: {
        comfortable: {
          label: "Comfortable",
          description: "Keep larger paragraph spacing and sidebar breathing room for long reading sessions.",
        },
        compact: {
          label: "Compact",
          description: "Reduce message and navigation spacing to fit more content on smaller screens.",
        },
      },
      compact: "Compact",
      comfortable: "Comfortable",
      fileBar: "File bar behavior",
      fileBarDescription:
        "Controls how the right-side file panel opens when the runtime produces a new artifact.",
      autoOpenArtifacts: "Auto open file panel",
      autoOpenArtifactsDescription:
        "When enabled, the right-side panel expands automatically for newly selected artifacts; when disabled, the selection is recorded without forcing the panel open.",
    },
    chat: {
      title: "Default model routing",
      description:
        "Updates the provider and model attached when the workspace starts a new turn.",
      defaultProvider: "Provider",
      defaultModel: "Model",
      loadingProvider: "Loading provider...",
      noProvider: "No provider available",
      loadingModel: "Loading models...",
      noModel: "This provider has no selectable models",
      summaryLoading: "Runtime model catalog is loading.",
      summaryTemplate:
        "Detected {{providerCount}} providers. The default session now routes to {{provider}} / {{model}}.",
      openBackendConfig: "Open backend config page",
      manageProviders: "Manage Provider list",
      executionMode: "Execution mode",
      executionModeDescription:
        "Controls whether workspace chat enters the backend ReAct tool loop.",
      enableReact: "Enable ReAct tool loop",
      enableReactDescription:
        "When enabled, requests carry <code>enable_react: true</code>, the backend exposes tool definitions to the model, and the agent enters the tool-calling loop. When disabled, skill routes or direct LLM fallback still work, but the model itself will not trigger tool calls.",
      currentMode: "Current mode",
      reactMode: "ReAct tool mode",
      routeDirectMode: "Route / direct mode",
      reasoning: "Reasoning effort",
      reasoningDescription:
        "Choose the default reasoning level that shapes planning depth and tool budget for new turns.",
      reasoningOptions: {
        default: {
          label: "Runtime default",
          description: "Let the backend default strategy handle reasoning effort completely.",
        },
        minimal: {
          label: "Minimal",
          description: "Uses the smallest reasoning budget for simple follow-ups and very short turns.",
        },
        low: {
          label: "Low",
          description: "Returns faster and suits normal Q&A and small edits.",
        },
        medium: {
          label: "Medium",
          description: "Balances speed and quality for most daily tasks.",
        },
        high: {
          label: "High",
          description: "Favors deeper decomposition and multi-step reasoning.",
        },
      },
      maxSteps: "Max steps",
      maxStepsDescription:
        "Limits the maximum number of planning / routing / tool execution steps per turn.",
      currentMaxSteps: "Current value: {{count}}.",
      maxStepsAdvice:
        "Usually 8 to 12 is enough for common workspace tasks; more complex orchestration can move up to 15 or 20.",
    },
    notifications: {
      title: "Workspace notifications",
      description:
        "Only used when the current tab is hidden, to notify you when a response completes or the runtime errors.",
      desktop: "Desktop alerts",
      desktopDescription:
        "If turned off, no desktop notification will appear even when browser permission is granted.",
      permission: "Permission status",
      permissionDescription: "Browser permission is required. It will not interrupt you while the page is visible.",
      permissionStates: {
        granted: "Granted. Background completion or error notifications can appear.",
        denied: "Blocked by the browser. Re-enable the site permission manually.",
        default: "Not requested yet. Click the button on the right to ask the browser for permission.",
        unsupported: "This environment does not support browser desktop notifications.",
      },
      currentConfig: "Current configuration",
      effectiveCondition: "Effective conditions",
      effectiveConditionDescription:
        "Notifications only actually appear when the master switch is on, desktop alerts are enabled, permission is granted, and the page is in the background.",
      requestPermission: "Request permission",
      enableDesktop: "Enable desktop alerts",
      disableDesktop: "Disable desktop alerts",
      enabled: "On",
      disabled: "Off",
      currentConfigMasterSwitch: "Notification master switch",
      currentConfigDesktopSwitch: "Desktop alerts",
    },
    about: {
      currentWorkspace: "Current workspace",
      description:
        "These details quickly confirm where the frontend is sending requests and where local preferences are stored.",
      runtimeIdentityDescription:
        "The frontend creates a persistent runtime client id for the current browser and uses it to derive the userId. Resetting switches to a new session namespace.",
      apiBase: "API base",
      apiBaseFallback: "same-origin /api proxy",
      currentRoute: "Current route",
      runtimeIdentity: "Runtime identity",
      runtimeUserId: "Runtime user id",
      workspacePath: "Workspace path",
      resetRuntimeClientId: "Reset local runtime client id",
      runtimeOverview: "Runtime overview",
      runtimeOverviewDescription:
        "This reads the runtime summary already loaded on the page and does not send any extra request.",
      localStorage: "Local storage",
      localStorageDescription:
        "Settings are stored in browser localStorage and are not written back to the repo config file.",
      settingsKey: "settings localStorage key",
      runtimeClientKey: "runtime client localStorage key",
      selectedProvider: "Available providers",
      selectedModel: "Current model",
      sessionCount: "Session count",
      recoverableSessions: "Recoverable sessions",
      activeTeams: "Active teams",
      activeTeamsSummary: "Loaded {{count}} team summaries",
      sessionBreakdown: "{{active}} active / {{archived}} archived",
      latestUpdated: "Updated {{time}}",
      noSessions: "No sessions found yet",
      runtimeDefault: "runtime default",
      scopeLabel: "scope",
      notSet: "not set",
    },
  },
  logs: {
    title: "Live runtime logs",
    subtitle: "Tail + Stream",
    backToWorkspace: "Back to workspace",
    home: "Home",
    searchPlaceholder:
      "Search request_id, trace_id, session_id, message, provider, or raw JSON",
    levelLabel: "Level",
    allLevels: "All",
    tokenLabel: "Token",
    tokenPlaceholder: "Optional for remote deployments",
    refresh: "Refresh",
    copyLink: "Copy link",
    followLatest: "Follow latest",
    fileLabel: "Log file:",
    fileFallback: "runtime log file_path not configured yet",
    streamHint: "Stream hint:",
    loadError: "Load failed:",
    currentView: "Current view",
    clearAll: "Clear all",
    showOnly: "Show only",
    listTitle: "Log list",
    listSubtitle: "Scan and locate with minimal width.",
    loading: "Loading",
    entries: "entries",
    time: "Time",
    levelShort: "Lv",
    event: "Event",
    readingLogs: "Reading logs...",
    logLoadFailed: "Log load failed",
    noLogs: "No logs match the current filters.",
    detailsTitle: "Log details",
    detailsDescription: "This area is for long text, JSON, and error bodies.",
    detailsLoading: "Loading log details...",
    selectPrompt: "Select a log on the left to inspect its details.",
    copiedLog: "Copied log",
    insights: "Skill exposure & cache",
    insightsHelp: "Cache state and skill exposure counters captured on this log line.",
    identifiers: "Identifiers",
    identifiersHelp:
      "Copy an identifier or write the current value back into the search box to keep tracking it.",
    clearSearch: "Clear search",
    filterSameValue: "Filter same value",
    cancelFilter: "Cancel filter",
    metadata: "Metadata",
    responsePreview: "Response Preview",
    extraFields: "Extra Fields",
    rawJson: "Raw JSON",
    connectionLive: "Live",
    connectionConnecting: "Connecting",
    connectionReconnecting: "Reconnecting",
    connectionError: "Stream error",
    connectionIdle: "Idle",
    identifierRequest: "Request ID",
    identifierTrace: "Trace ID",
    identifierSession: "Session ID",
    levelError: "Error",
    levelWarn: "Warn",
    levelInfo: "Info",
    levelDebug: "Debug",
    levelOther: "Other",
    levelShortError: "ERR",
    levelShortWarn: "WRN",
    levelShortInfo: "INF",
    levelShortDebug: "DBG",
    levelShortOther: "LOG",
    chipQuery: "Search",
    chipLevel: "Level",
    chipFollow: "Follow",
    chipCursor: "Cursor",
    activeChipOff: "off",
    copied: "Copied",
    cursorLabel: "Cursor",
    levelFallback: "log",
    requestPrefix: "request",
    runtimeLogFallback: "runtime log",
    statusPrefix: "status",
    timestamp: "Timestamp",
    level: "Level",
    module: "Module",
    caller: "Caller",
    requestId: "Request ID",
    traceId: "Trace ID",
    sessionId: "Session ID",
    provider: "Provider",
    model: "Model",
    method: "Method",
    url: "URL",
    responseStatus: "Response status",
    upstreamError: "Upstream error",
    cacheHit: "Cache hit",
    cacheHitValueHit: "Hit",
    cacheHitValueMiss: "Miss",
    skillExposureMode: "Skill exposure mode",
    finalFunctionCount: "Final function count",
    routedSkillCount: "Routed skill count",
    candidateCount: "Candidate count",
    exposedFunctionCount: "Exposed function count",
  },
} as const;
