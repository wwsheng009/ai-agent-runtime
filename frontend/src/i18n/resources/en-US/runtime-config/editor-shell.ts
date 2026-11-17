// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigEditorShell } from "../../zh-CN/runtime-config/editor-shell";

export const enRuntimeConfigEditorShell = {
  title: "Backend config workspace",
  independentBadge: "Independent backend config page",
  unsavedBadge: "Unsaved draft",
  configDialog: {
    eyebrow: "Config editor",
    close: "Close {{title}}",
  },
  configNode: {
    fieldsBadge: "{{count}} fields",
    itemsBadge: "{{count}} items",
    delete: "Delete",
    emptyObject: "This object is empty — add a field directly.",
    addField: "Add field",
    newFieldPlaceholder: "new_key",
    newFieldKindAriaLabel: "New field type",
    addItem: "Add item",
    newArrayItemKindAriaLabel: "New array item type",
    emptyArray: "This array is empty — insert an item to start.",
    currentValue: "Current value:",
  },
  sourceFocus: "Source fallback editor",
  structuredFocus: "Dedicated config mode",
  table: {
    searchPlaceholder: "Search…",
    noSearchResults: "No matching entries. Try a different keyword.",
    pagination: {
      showing: "Showing {{start}}-{{end}} of {{total}}",
      prev: "Previous page",
      next: "Next page",
    },
  },
  controls: {
    reload: "Reload",
    preview: "Generate preview",
    save: "Save to file",
    restartWithEffect: "Restart to apply",
    restart: "Restart runtime-server",
  },
  panels: {
    editorTitle: "Config editor",
    modeTitle: "Config areas",
    summaryTitle: "Draft summary",
  },
  source: {
    title: "Raw YAML draft",
    preserveComments: "Preserve comments",
    lines: "Lines",
    chars: "Characters",
  },
  preview: {
    title: "Change preview",
    added: "Added",
    removed: "Removed",
    latest: "Latest preview",
    latestWithCount: "Latest preview, {{count}} lines",
    expired: "Preview expired",
    needsRestart: "Restart required after save",
  },
  draftValidation: {
    title: "Draft checks",
    errorBadge: "{{count}} errors",
    warningBadge: "{{count}} warnings",
    severityError: "error",
    severityWarning: "warning",
    blocked:
      "The draft has {{count}} structural errors; writing is blocked until they are fixed.",
    rootNotMapping:
      "The root node must be a mapping (object); it currently has another type.",
    providersNotMapping: "The providers section must be a mapping.",
    providersItemsNotMapping:
      "providers.items must be a mapping of provider name to config.",
    providerNotMapping:
      "The config of provider \"{{name}}\" must be a mapping.",
    providerFieldNotString: "Field \"{{field}}\" must be a string.",
    providerFieldNotBoolean: "Field \"{{field}}\" must be a boolean.",
    providerFieldNotStringList:
      "Field \"{{field}}\" must be a list of strings.",
    providerFieldNotMapping: "Field \"{{field}}\" must be a mapping (object).",
    defaultProviderNotString: "providers.default_provider must be a string.",
    defaultProviderUnknown:
      "default_provider points at provider \"{{name}}\", which is not in providers.items (advisory only).",
    rawEmpty:
      "The source draft is empty; an empty config would stop the runtime from starting, so writing is blocked.",
  },
  sticky: {
    unsaved: "Unsaved draft",
    previewButton: "Preview first",
    saveButton: "Save now",
    saveAndRestartButton: "Save and restart",
  },
} satisfies DeepStringShape<typeof zhRuntimeConfigEditorShell>;
