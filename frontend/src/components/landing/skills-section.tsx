import {
  BookMarkedIcon,
  FolderGit2Icon,
  RocketIcon,
  WrenchIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Section } from "@/components/landing/section";

export function SkillsSection() {
  const { t } = useTranslation("landing");
  const skillColumns = [
    {
      title: t("skills.columns.understand.title"),
      description: t("skills.columns.understand.description"),
      items: t("skills.columns.understand.items", { returnObjects: true }) as string[],
    },
    {
      title: t("skills.columns.coordinate.title"),
      description: t("skills.columns.coordinate.description"),
      items: t("skills.columns.coordinate.items", { returnObjects: true }) as string[],
    },
    {
      title: t("skills.columns.deliver.title"),
      description: t("skills.columns.deliver.description"),
      items: t("skills.columns.deliver.items", { returnObjects: true }) as string[],
    },
  ];

  return (
    <Section
      className="w-full"
      eyebrow={t("skills.eyebrow")}
      title={t("skills.title")}
      subtitle={t("skills.subtitle")}
    >
      <div className="grid gap-4 lg:grid-cols-[0.92fr_1.08fr]">
        <div className="rounded-[1.9rem] border border-white/8 bg-black/22 p-6">
          <div className="flex items-center gap-3 text-sm font-semibold text-[#8fd0c6]">
            <BookMarkedIcon size={18} />
            {t("skills.ladderLabel")}
          </div>
          <div className="mt-6 space-y-4">
            {skillColumns.map((column, index) => (
              <div
                key={column.title}
                className="relative overflow-hidden rounded-[1.4rem] border border-white/8 bg-white/4 px-4 py-4"
              >
                <div className="absolute top-0 left-0 h-full w-1 bg-gradient-to-b from-[#f0c77b] via-[#8fd0c6] to-transparent" />
                <div className="pl-4">
                  <div className="app-text-11 uppercase tracking-[0.2em] text-[#f0c77b]">
                    {t("skills.stageLabel", { index: index + 1 })}
                  </div>
                  <div className="mt-2 text-xl font-semibold">{column.title}</div>
                  <p className="mt-3 text-sm leading-7 text-[var(--muted-foreground)]">
                    {column.description}
                  </p>
                </div>
              </div>
            ))}
          </div>
        </div>

        <div className="grid gap-4 md:grid-cols-3">
          {skillColumns.map((column, index) => (
            <div
              key={column.title}
              className="rounded-[1.8rem] border border-white/8 bg-[linear-gradient(180deg,rgba(255,255,255,0.05),rgba(255,255,255,0.02))] p-5"
            >
              <div className="flex items-center gap-3">
                {index === 0 ? (
                  <FolderGit2Icon size={18} className="text-[#8fd0c6]" />
                ) : index === 1 ? (
                  <WrenchIcon size={18} className="text-[#f0c77b]" />
                ) : (
                  <RocketIcon size={18} className="text-[#8fd0c6]" />
                )}
                <div className="text-lg font-semibold">{column.title}</div>
              </div>
              <ul className="mt-5 space-y-3 text-sm leading-7 text-[var(--muted-foreground)]">
                {column.items.map((item) => (
                  <li key={item} className="flex gap-3">
                    <span className="mt-2 size-1.5 shrink-0 rounded-full bg-[#f0c77b]" />
                    <span>{item}</span>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </div>
    </Section>
  );
}
