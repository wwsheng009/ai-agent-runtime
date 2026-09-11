import { type TFunction } from "i18next";

import { type RuntimeProviderGroupSummary } from "../runtime-config-domain-utils";
import { type ProviderGroupDraftValidationIssue } from "../runtime-provider-groups-domain-form-utils";
import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";

import { describeMemberProviderHint } from "./draft-utils";

export function ProviderReferenceBadge({
  member,
  provider,
  t,
}: {
  member: RuntimeProviderGroupSummary["providers"][number];
  provider?: RuntimeProviderSummary;
  t: TFunction<"runtimeConfig">;
}) {
  const label = [member.name, provider?.protocol || "", member.role || ""]
    .filter(Boolean)
    .join(" · ");

  return (
    <span
      title={describeMemberProviderHint(member.name, provider, t)}
      className={`inline-flex max-w-full items-center rounded-[0.6rem] border px-2 py-0.5 text-[11px] ${
        provider
          ? "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]"
          : "border-[#f59e7d]/30 bg-[#f59e7d]/10 text-[#f5c7b8]"
      }`}
    >
      <span className="truncate">
        {label || member.name || t("editor.providerGroups.row.unnamedMember")}
      </span>
    </span>
  );
}

export function FieldIssueText({
  issue,
}: {
  issue: ProviderGroupDraftValidationIssue | null;
}) {
  if (!issue) {
    return null;
  }

  return <div className="mt-1 text-xs text-[#f5c7b8]">{issue.message}</div>;
}
