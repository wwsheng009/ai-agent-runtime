// P2-1A: workspace.panels.filePreview copy (runtime file preview dialog).
// Keep keys aligned with zh-CN/workspace/panels-file-preview.ts.
import type { DeepStringShape } from "../../shape";
import type { zhWorkspaceFilePreview } from "../../zh-CN/workspace/panels-file-preview";

export const enWorkspaceFilePreview = {
  ariaLabel: "Runtime file preview",
  eyebrow: "Runtime · File preview",
  title: "Runtime file preview",
  close: "Close file preview",
  description:
    "Content is read from a local file visible to the runtime process (read-only, POST /api/runtime/fs/read-file); nothing is cached or rewritten locally.",
  meta: {
    requestedPath: "Requested path",
    resolvedPath: "Resolved path",
    bytes: "{{count}} bytes",
    size: "{{size}}",
    lines: "{{count}} lines",
  },
  body: {
    loading: "Reading file…",
    empty: "The file is empty (0 bytes); nothing to display.",
    binary:
      "Detected binary content ({{reason}}); text is not rendered, only the real byte count is shown.",
    binaryNul: "NUL byte present",
    binaryUtf8: "not UTF-8 text",
    tooLarge:
      "The file is {{size}}, above the {{limit}} preview limit; content is not rendered to keep the page responsive.",
  },
  error: {
    title: "File read failed",
    retry: "Retry",
    unavailable:
      "The runtime file read endpoint is unavailable (HTTP {{status}}); the file content was not fetched and no local substitute is used.",
    unavailableUnknown:
      "The runtime file read endpoint is unavailable; the file content was not fetched and no local substitute is used.",
  },
} satisfies DeepStringShape<typeof zhWorkspaceFilePreview>;
