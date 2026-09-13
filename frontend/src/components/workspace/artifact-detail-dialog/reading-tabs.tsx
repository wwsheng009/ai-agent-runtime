// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { EyeIcon, FileCode2Icon } from "lucide-react";
import { useRef } from "react";
import { useTranslation } from "react-i18next";

import {
  handleHorizontalTabKeyDown,
  surfaceButtonClass,
} from "./dialog-helpers";
import type { ArtifactDetailView } from "./types";

type ArtifactReadingTabsProps = {
  onSelectView: (view: ArtifactDetailView) => void;
  previewPanelId: string;
  previewTabId: string;
  sourcePanelId: string;
  sourceTabId: string;
  view: ArtifactDetailView;
};

export function ArtifactReadingTabs({
  onSelectView,
  previewPanelId,
  previewTabId,
  sourcePanelId,
  sourceTabId,
  view,
}: ArtifactReadingTabsProps) {
  const { t } = useTranslation("workspace");
  const viewTabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  return (
    <div
      aria-label={t("panels.artifacts.detail.tabsLabel")}
      aria-orientation="horizontal"
      className="flex flex-wrap gap-2 border-b border-border px-4 py-3"
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
        onClick={() => onSelectView("preview")}
        onKeyDown={(event) =>
          handleHorizontalTabKeyDown(event, {
            currentIndex: 0,
            disabledStates: [false, false],
            onSelectIndex: (index) =>
              onSelectView(index === 0 ? "preview" : "source"),
            refs: viewTabRefs.current,
          })
        }
        className={surfaceButtonClass(view === "preview")}
      >
        <EyeIcon size={14} />
        {t("panels.artifacts.detail.tabPreview")}
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
        onClick={() => onSelectView("source")}
        onKeyDown={(event) =>
          handleHorizontalTabKeyDown(event, {
            currentIndex: 1,
            disabledStates: [false, false],
            onSelectIndex: (index) =>
              onSelectView(index === 0 ? "preview" : "source"),
            refs: viewTabRefs.current,
          })
        }
        className={surfaceButtonClass(view === "source")}
      >
        <FileCode2Icon size={14} />
        {t("panels.artifacts.detail.tabSource")}
      </button>
    </div>
  );
}
