// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigEditorShell } from "../../zh-CN/runtime-config/editor-shell";

export const enRuntimeConfigEditorShell = {
  title: "Backend config workspace",
  description:
    "Manage runtime backend configuration separately, with dedicated forms first and YAML as the fallback.",
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
  usage: {
    title: "How to use it",
    body:
      "The structured config tree is gone. Use the dedicated controls for common config areas; switch to YAML mode when you need to fill in fields that are not covered.",
  },
  currentFocusPrefix: "Current focus:",
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
  draftValidation: {
    title: "Draft checks",
    description:
      "Draft structure is checked before writing; errors block save and preview, warnings are advisory.",
    errorBadge: "{{count}} errors",
    warningBadge: "{{count}} warnings",
    severityError: "error",
    severityWarning: "warning",
    blocked:
      "The draft has {{count}} structural errors; writing is blocked until they are fixed.",
    blockedHint:
      "Save and preview requests are not sent to the backend while errors remain.",
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
    hint: "Preview the diff first, then save it to the current runtime config document.",
    previewButton: "Preview first",
    saveButton: "Save now",
    saveAndRestartButton: "Save and restart",
  },
} satisfies DeepStringShape<typeof zhRuntimeConfigEditorShell>;
