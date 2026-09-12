// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { XIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type Artifact } from "@/data/mock";
import {
  type ArtifactCategory,
  formatArtifactCategory,
} from "@/lib/workspace-artifacts";
import { cn } from "@/lib/utils";

type ArtifactDetailHeaderProps = {
  artifact: Artifact;
  category: ArtifactCategory;
  descriptionId: string;
  onClose: () => void;
  titleId: string;
};

export function ArtifactDetailHeader({
  artifact,
  category,
  descriptionId,
  onClose,
  titleId,
}: ArtifactDetailHeaderProps) {
  return (
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
  );
}
