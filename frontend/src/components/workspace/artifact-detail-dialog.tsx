import {
  EyeIcon,
  FileCode2Icon,
  XIcon,
} from "lucide-react";
import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { createPortal } from "react-dom";

import { injectPreviewDocumentSettings } from "@/components/workspace/artifact-preview-document";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CodeBlock } from "@/components/ui/code-block";
import { useAppSettings } from "@/core/settings";
import { type Artifact } from "@/data/mock";
import {
  classifyArtifactCategory,
  formatArtifactCategory,
} from "@/lib/workspace-artifacts";
import { cn } from "@/lib/utils";

type ArtifactDetailDialogProps = {
  artifact: Artifact | null;
  onClose: () => void;
  open: boolean;
};

function surfaceButtonClass(active: boolean) {
  return cn(
    "inline-flex items-center gap-2 rounded-[0.65rem] border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--dialog-bg)]",
    active
      ? "border-[#f0c77b]/30 bg-[#f0c77b]/8 text-[#f0c77b]"
      : "border-white/10 bg-white/4 text-[var(--muted-foreground)] hover:border-white/16 hover:bg-white/8",
  );
}

function getEnabledTabIndices(disabledStates: boolean[]) {
  return disabledStates.flatMap((disabled, index) => (disabled ? [] : [index]));
}

function handleHorizontalTabKeyDown(
  event: ReactKeyboardEvent<HTMLButtonElement>,
  options: {
    currentIndex: number;
    disabledStates: boolean[];
    onSelectIndex: (index: number) => void;
    refs: Array<HTMLButtonElement | null>;
  },
) {
  const enabledIndices = getEnabledTabIndices(options.disabledStates);
  if (enabledIndices.length === 0) {
    return;
  }

  const currentEnabledIndex = enabledIndices.indexOf(options.currentIndex);
  let nextIndex = -1;

  if (event.key === "Home") {
    nextIndex = enabledIndices[0] ?? -1;
  } else if (event.key === "End") {
    nextIndex = enabledIndices[enabledIndices.length - 1] ?? -1;
  } else if (event.key === "ArrowRight" || event.key === "ArrowDown") {
    const targetEnabledIndex =
      currentEnabledIndex >= 0
        ? (currentEnabledIndex + 1) % enabledIndices.length
        : 0;
    nextIndex = enabledIndices[targetEnabledIndex] ?? -1;
  } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
    const targetEnabledIndex =
      currentEnabledIndex >= 0
        ? (currentEnabledIndex - 1 + enabledIndices.length) % enabledIndices.length
        : enabledIndices.length - 1;
    nextIndex = enabledIndices[targetEnabledIndex] ?? -1;
  }

  if (nextIndex < 0) {
    return;
  }

  event.preventDefault();
  options.onSelectIndex(nextIndex);
  options.refs[nextIndex]?.focus();
}

function buildMetaItems(artifact: Artifact, view: "preview" | "source") {
  const lines = artifact.kind === "image" ? null : artifact.content.split(/\r?\n/).length;
  const category = classifyArtifactCategory(artifact);
  const readingMode =
    artifact.kind === "image"
      ? "Image"
      : view === "preview"
        ? "Preview"
        : "Source";

  return [
    { label: "Category", value: formatArtifactCategory(category) },
    { label: "Path", value: artifact.path },
    { label: "Language", value: artifact.language ?? "—" },
    { label: "Format", value: artifact.kind },
    { label: "Reading mode", value: readingMode },
    {
      label: artifact.kind === "image" ? "Byte count" : "Lines",
      value:
        artifact.kind === "image"
          ? artifact.byteCount != null
            ? formatBytes(artifact.byteCount)
            : "—"
          : `${lines}`,
    },
  ];
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value < 0) {
    return "—";
  }
  if (value < 1024) {
    return `${value} B`;
  }
  const units = ["KB", "MB", "GB", "TB"];
  let current = value / 1024;
  let unitIndex = 0;
  while (current >= 1024 && unitIndex < units.length - 1) {
    current /= 1024;
    unitIndex++;
  }
  return `${current.toFixed(current >= 10 ? 0 : 1)} ${units[unitIndex]}`;
}

export function ArtifactDetailDialog({
  artifact,
  onClose,
  open,
}: ArtifactDetailDialogProps) {
  const { settings } = useAppSettings();
  const viewTabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const titleId = useId();
  const descriptionId = useId();
  const previewTabId = useId();
  const sourceTabId = useId();
  const previewPanelId = useId();
  const sourcePanelId = useId();
  const [preferredView, setPreferredView] = useState<"preview" | "source">(
    "preview",
  );

  useEffect(() => {
    if (!artifact) {
      return;
    }

    setPreferredView(artifact.previewHtml ? "preview" : "source");
  }, [artifact?.id, artifact?.previewHtml]);

  useEffect(() => {
    if (!open || typeof document === "undefined") {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    return () => {
      document.body.style.overflow = previousOverflow;
    };
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onClose, open]);

  if (!open || !artifact) {
    return null;
  }

  if (typeof document === "undefined") {
    return null;
  }

  const category = classifyArtifactCategory(artifact);
  const view = artifact.previewHtml ? preferredView : "source";
  const previewDocument = artifact.previewHtml
    ? injectPreviewDocumentSettings(artifact.previewHtml, {
        codeTextSize: settings.appearance.codeTextSize,
        textSize: settings.appearance.textSize,
      })
    : null;
  const metaItems = buildMetaItems(artifact, view);
  const isImageArtifact = artifact.kind === "image";
  const isRenderableImage =
    isImageArtifact &&
    (!artifact.mimeType || artifact.mimeType.toLowerCase().startsWith("image/"));
  const imageDetails = isImageArtifact
    ? [
        {
          label: "Revised prompt",
          value: artifact.revisedPrompt?.trim() || "—",
        },
        {
          label: "MIME type",
          value: artifact.mimeType?.trim() || "image/png",
        },
        {
          label: "SHA-256",
          value: artifact.sha256?.trim() || "—",
        },
        {
          label: "Byte count",
          value:
            artifact.byteCount != null ? formatBytes(artifact.byteCount) : "—",
        },
      ]
    : [];

  return createPortal(
    <div
      className="fixed inset-0 z-[130] flex items-center justify-center bg-[var(--dialog-backdrop)] px-3 py-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div className="flex max-h-[calc(100vh-1.5rem)] w-full max-w-[min(90rem,calc(100vw-1.5rem))] flex-col overflow-hidden rounded-[0.95rem] border border-[var(--border)] [background:var(--dialog-bg)] shadow-[0_18px_48px_rgba(0,0,0,0.28)]">
        <div className="flex items-start justify-between gap-3 border-b border-[var(--border)] px-4 py-3.5">
          <div className="min-w-0">
            <div
              className={cn(
                "app-text-11 uppercase tracking-[0.16em]",
                category === "evidence"
                  ? "text-[#8fd0c6]"
                  : "text-[var(--accent-primary)]",
              )}
            >
              {category === "evidence" ? "Runtime evidence" : "Output file"}
            </div>
            <h2
              className="mt-1 truncate text-lg font-semibold tracking-[-0.03em] text-[var(--foreground)]"
              id={titleId}
            >
              {artifact.name}
            </h2>
            <p
              className="mt-1 max-w-4xl text-sm leading-6 text-[var(--muted-foreground)]"
              id={descriptionId}
            >
              {artifact.summary}
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Badge>{formatArtifactCategory(category)}</Badge>
            <Badge>{artifact.kind}</Badge>
            <Button
              autoFocus
              variant="ghost"
              size="icon"
              onClick={onClose}
              aria-label="关闭 artifact 详情"
            >
              <XIcon size={16} />
            </Button>
          </div>
        </div>

        <div
          aria-describedby={descriptionId}
          aria-labelledby={titleId}
          className="grid min-h-0 flex-1 gap-0 xl:grid-cols-[18rem_minmax(0,1fr)]"
          role="dialog"
          aria-modal="true"
          data-artifact-detail-dialog="true"
        >
          <aside className="app-scrollbar min-h-0 overflow-y-auto border-b border-[var(--border)] px-4 py-4 xl:border-b-0 xl:border-r">
            <div className="space-y-4">
              <section className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3.5 py-3">
                <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  Artifact path
                </div>
                <div className="app-inline-mono mt-2 break-all text-sm text-[var(--foreground)]">
                  {artifact.path}
                </div>
              </section>

              <section className="grid gap-2">
                {metaItems.map((item) => (
                  <div
                    key={`${artifact.id}-${item.label}`}
                    className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5"
                  >
                    <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      {item.label}
                    </div>
                    <div className="mt-1.5 break-all text-sm text-[var(--foreground)]">
                      {item.value}
                    </div>
                  </div>
                ))}
              </section>
            </div>
          </aside>

          <section className="flex min-h-0 flex-col overflow-hidden">
            {artifact.previewHtml ? (
              <div
                aria-label="Artifact reading modes"
                aria-orientation="horizontal"
                className="flex flex-wrap gap-2 border-b border-[var(--border)] px-4 py-3"
                role="tablist"
              >
                <button
                  aria-controls={previewPanelId}
                  aria-selected={view === "preview"}
                  id={previewTabId}
                  ref={(node) => {
                    viewTabRefs.current[0] = node;
                  }}
                  role="tab"
                  tabIndex={view === "preview" ? 0 : -1}
                  type="button"
                  onClick={() => setPreferredView("preview")}
                  onKeyDown={(event) =>
                    handleHorizontalTabKeyDown(event, {
                      currentIndex: 0,
                      disabledStates: [false, false],
                      onSelectIndex: (index) =>
                        setPreferredView(index === 0 ? "preview" : "source"),
                      refs: viewTabRefs.current,
                    })
                  }
                  className={surfaceButtonClass(view === "preview")}
                >
                  <EyeIcon size={14} />
                  Preview
                </button>
                <button
                  aria-controls={sourcePanelId}
                  aria-selected={view === "source"}
                  id={sourceTabId}
                  ref={(node) => {
                    viewTabRefs.current[1] = node;
                  }}
                  role="tab"
                  tabIndex={view === "source" ? 0 : -1}
                  type="button"
                  onClick={() => setPreferredView("source")}
                  onKeyDown={(event) =>
                    handleHorizontalTabKeyDown(event, {
                      currentIndex: 1,
                      disabledStates: [false, false],
                      onSelectIndex: (index) =>
                        setPreferredView(index === 0 ? "preview" : "source"),
                      refs: viewTabRefs.current,
                    })
                  }
                  className={surfaceButtonClass(view === "source")}
                >
                  <FileCode2Icon size={14} />
                  Source
                </button>
              </div>
            ) : null}

            <div className="app-scrollbar min-h-0 flex-1 overflow-y-auto p-4">
              {isImageArtifact ? (
                isRenderableImage ? (
                  <div className="overflow-hidden rounded-[0.95rem] border border-[var(--border)] bg-black/20">
                    <div className="border-b border-[var(--border)] px-3.5 py-3">
                      <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                        Rendered image
                      </div>
                      <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                        Inspect the generated image at full width. Use the metadata below for prompt and integrity details.
                      </div>
                    </div>
                    <div className="p-4">
                      <div className="flex min-h-[18rem] items-center justify-center rounded-[0.9rem] border border-white/8 bg-black/40 p-4">
                        <img
                          alt={artifact.revisedPrompt?.trim() || artifact.name}
                          className="max-h-[70vh] max-w-full rounded-[0.75rem] border border-white/10 object-contain"
                          src={artifact.content}
                        />
                      </div>
                      <div className="mt-4 grid gap-2">
                        {imageDetails.map((item) => (
                          <div
                            key={`${artifact.id}-${item.label}`}
                            className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5"
                          >
                            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                              {item.label}
                            </div>
                            <div className="mt-1.5 break-all text-sm text-[var(--foreground)]">
                              {item.value}
                            </div>
                          </div>
                        ))}
                      </div>
                    </div>
                  </div>
                ) : (
                  <div className="overflow-hidden rounded-[0.95rem] border border-[var(--border)] bg-black/20">
                    <div className="border-b border-[var(--border)] px-3.5 py-3">
                      <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                        Image unavailable
                      </div>
                      <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                        This artifact was recorded as an image, but the MIME type is not renderable inline.
                      </div>
                    </div>
                    <div className="p-4">
                      <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3.5 py-3">
                        <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                          File name
                        </div>
                        <div className="mt-1.5 break-all text-sm text-[var(--foreground)]">
                          {artifact.name}
                        </div>
                        <div className="mt-3 app-text-11 text-[var(--muted-foreground)]">
                          MIME type {artifact.mimeType?.trim() || "unknown"} cannot be rendered inline.
                        </div>
                        <div className="mt-4">
                          <Button
                            onClick={() => {
                              window.open(artifact.content, "_blank", "noopener,noreferrer");
                            }}
                            variant="secondary"
                          >
                            Open raw file
                          </Button>
                        </div>
                      </div>
                    </div>
                  </div>
                )
              ) : artifact.previewHtml ? (
                <>
                  <div
                    aria-labelledby={previewTabId}
                    className="min-h-full overflow-hidden rounded-[0.95rem] border border-[var(--border)] bg-black/20"
                    hidden={view !== "preview"}
                    id={previewPanelId}
                    role="tabpanel"
                    tabIndex={0}
                  >
                    <div className="border-b border-[var(--border)] px-3.5 py-3">
                      <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                        Rendered preview
                      </div>
                      <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                        Use the full dialog width to inspect the rendered output.
                      </div>
                    </div>
                    <div className="p-4">
                      <div className="h-[min(70vh,56rem)] min-h-[28rem] rounded-[0.85rem] border border-white/8 bg-white p-3">
                        <iframe
                          title={artifact.name}
                          srcDoc={previewDocument ?? artifact.previewHtml}
                          className="h-full w-full rounded-[0.75rem] border border-slate-200 bg-white"
                          sandbox="allow-scripts allow-same-origin"
                        />
                      </div>
                    </div>
                  </div>
                  <div
                    aria-labelledby={sourceTabId}
                    className="overflow-hidden rounded-[0.95rem] border border-[var(--border)] bg-black/20"
                    hidden={view !== "source"}
                    id={sourcePanelId}
                    role="tabpanel"
                    tabIndex={0}
                  >
                    <div className="border-b border-[var(--border)] px-3.5 py-3">
                      <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                        Source reader
                      </div>
                      <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                        Inspect the exact file contents without squeezing them into the rail.
                      </div>
                    </div>
                    <div className="p-4">
                      <CodeBlock
                        code={artifact.content}
                        language={artifact.language ?? "json"}
                        title={artifact.path}
                      />
                    </div>
                  </div>
                </>
              ) : (
                <div className="overflow-hidden rounded-[0.95rem] border border-[var(--border)] bg-black/20">
                  <div className="border-b border-[var(--border)] px-3.5 py-3">
                    <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                      Source reader
                    </div>
                    <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                      Inspect the exact structured payload or file contents in a full-width dialog.
                    </div>
                  </div>
                  <div className="p-4">
                    <CodeBlock
                      code={artifact.content}
                      language={artifact.language ?? "json"}
                      title={artifact.path}
                    />
                  </div>
                </div>
              )}
            </div>
          </section>
        </div>
      </div>
    </div>,
    document.body,
  );
}
