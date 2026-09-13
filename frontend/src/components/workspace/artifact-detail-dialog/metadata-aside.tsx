// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact } from "@/data/mock";

import type { ArtifactMetaItem } from "./types";

type ArtifactMetadataAsideProps = {
  artifact: Artifact;
  metaItems: ArtifactMetaItem[];
};

export function ArtifactMetadataAside({
  artifact,
  metaItems,
}: ArtifactMetadataAsideProps) {
  return (
    <aside className="app-scrollbar min-h-0 overflow-y-auto border-b border-border px-4 py-4 xl:border-b-0 xl:border-r">
      <div className="space-y-4">
        <section className="rounded-panel border border-border bg-surface-softer px-3.5 py-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            Artifact path
          </div>
          <div className="app-inline-mono mt-2 break-all text-sm text-foreground">
            {artifact.path}
          </div>
        </section>

        <section className="grid gap-2">
          {metaItems.map((item) => (
            <div
              key={`${artifact.id}-${item.label}`}
              className="rounded-card border border-border bg-surface-softer px-3 py-2.5"
            >
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {item.label}
              </div>
              <div className="mt-1.5 break-all text-sm text-foreground">
                {item.value}
              </div>
            </div>
          ))}
        </section>
      </div>
    </aside>
  );
}
