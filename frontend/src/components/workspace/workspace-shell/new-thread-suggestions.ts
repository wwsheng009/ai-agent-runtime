// 新会话建议卡的数据构造。由 workspace-shell.tsx 机械拆分（P0-2），仅搬迁不改语义；
// t 由宿主注入（与 view-tab-bar / new-thread-placeholder 同一约定）。

import { type TFunction } from "i18next";
import {
  Code2Icon,
  FileSearchIcon,
  ListTodoIcon,
  ShieldCheckIcon,
} from "lucide-react";

import { type NewThreadSuggestion } from "./new-thread-placeholder";

export function buildNewThreadSuggestions(
  t: TFunction<"workspace">,
): NewThreadSuggestion[] {
  return [
    {
      key: "analyze",
      icon: FileSearchIcon,
      title: t("shell.suggestions.analyze.title"),
      description: t("shell.suggestions.analyze.description"),
      prompt: t("shell.suggestions.analyze.prompt"),
    },
    {
      key: "implement",
      icon: Code2Icon,
      title: t("shell.suggestions.implement.title"),
      description: t("shell.suggestions.implement.description"),
      prompt: t("shell.suggestions.implement.prompt"),
    },
    {
      key: "review",
      icon: ShieldCheckIcon,
      title: t("shell.suggestions.review.title"),
      description: t("shell.suggestions.review.description"),
      prompt: t("shell.suggestions.review.prompt"),
    },
    {
      key: "plan",
      icon: ListTodoIcon,
      title: t("shell.suggestions.plan.title"),
      description: t("shell.suggestions.plan.description"),
      prompt: t("shell.suggestions.plan.prompt"),
    },
  ];
}