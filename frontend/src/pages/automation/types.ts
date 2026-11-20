import type { CreateFormState } from "@/components/workspace/schedule-panel/form-state";
import type {
  RuntimeScheduleStatus,
  RuntimeScheduleTargetKind,
} from "@/types/runtime";

export type AutomationView = "list" | "runs" | "templates";

export type AutomationStatusFilter = "all" | RuntimeScheduleStatus;

export type AutomationTargetFilter = "all" | RuntimeScheduleTargetKind;

export type AutomationCreateRequest = {
  key: string;
  preset: Partial<CreateFormState>;
};
