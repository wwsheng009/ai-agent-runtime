// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeTeamRecord, type RuntimeTeamSummaryEntry } from "@/lib/runtime-api";

export type RuntimeTeamsProps = {
  className?: string;
  error: string | null;
  isLoading: boolean;
  isRefreshing?: boolean;
  onRefresh?: () => void;
  showHeader?: boolean;
  summaries: RuntimeTeamSummaryEntry[];
  teams: RuntimeTeamRecord[];
};

export type RuntimeTeamsView = "teams" | "dispatch";
