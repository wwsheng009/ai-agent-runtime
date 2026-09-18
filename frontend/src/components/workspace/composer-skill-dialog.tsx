// P2-7 子片：`/skill` 弹窗（参考 composer-model-dialog）。

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { useTranslation } from "react-i18next";

import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { cn } from "@/lib/utils";

export type ComposerSkillDialogProps = {
  error: string | null;
  skills: readonly string[];
  loading: boolean;
  onClose: () => void;
  /** 选择 skill 后执行。 */
  onSelect: (skill: string) => void;
  open: boolean;
};

/**
 * 关闭即卸载：候选面的检索词与高亮随重开归零。
 */
export function ComposerSkillDialog(props: ComposerSkillDialogProps) {
  useDialogLifecycle(props.open, props.onClose);
  if (!props.open) {
    return null;
  }
  return (
    <ComposerSkillDialogBody
      error={props.error}
      skills={props.skills}
      loading={props.loading}
      onClose={props.onClose}
      onSelect={props.onSelect}
    />
  );
}

function ComposerSkillDialogBody({
  error,
  skills,
  loading,
  onClose,
  onSelect,
}: Omit<ComposerSkillDialogProps, "open">) {
  const { t } = useTranslation("workspace");

  const [search, setSearch] = useState("");
  // null = 未人工导航：高亮在首行
  const [activeOverride, setActiveOverride] = useState<number | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const focusPendingRef = useRef(true);

  const filteredSkills = useMemo(() => {
    if (!search.trim()) {
      return skills;
    }
    const lower = search.toLowerCase();
    return skills.filter((skill) => skill.toLowerCase().includes(lower));
  }, [skills, search]);

  // 计算高亮索引：未导航时默认首行，已导航则保持在范围内
  const requestedIndex = activeOverride ?? 0;
  const activeIndex =
    filteredSkills.length === 0
      ? -1
      : Math.max(0, Math.min(requestedIndex, filteredSkills.length - 1));

  // 自动聚焦检索框
  useEffect(() => {
    if (focusPendingRef.current && searchRef.current) {
      searchRef.current.focus();
      focusPendingRef.current = false;
    }
  }, [error, loading, filteredSkills]);

  // 滚动高亮行入视口
  useEffect(() => {
    if (listRef.current && filteredSkills.length > 0) {
      const activeElement = listRef.current.querySelector(
        `[data-skill-index="${activeIndex}"]`,
      );
      if (activeElement) {
        activeElement.scrollIntoView({ block: "nearest" });
      }
    }
  }, [activeIndex, filteredSkills.length, search]);

  const handleSelect = useCallback(
    (skill: string) => {
      onSelect(skill);
      onClose();
    },
    [onSelect, onClose],
  );

  const handleKeyDown = useCallback(
    (e: KeyboardEvent<HTMLInputElement>) => {
      if (filteredSkills.length === 0) {
        return;
      }

      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActiveOverride((prev) => {
          const current = prev ?? 0;
          return Math.min(current + 1, filteredSkills.length - 1);
        });
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setActiveOverride((prev) => {
          const current = prev ?? 0;
          return Math.max(current - 1, 0);
        });
      } else if (e.key === "Enter") {
        e.preventDefault();
        if (activeIndex >= 0) {
          handleSelect(filteredSkills[activeIndex]);
        }
      } else if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
    },
    [filteredSkills, activeIndex, handleSelect, onClose],
  );

  return (
    <DialogOverlay onDismiss={onClose}>
      <DialogPanel className="max-h-[32rem] w-full max-w-md overflow-hidden">
        <div className="flex flex-col gap-3 p-4">
          <h2 className="text-lg font-semibold text-[--text-primary]">
            {t("composer.builtin.skill.dialog.title")}
          </h2>

          <input
            ref={searchRef}
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={t("composer.builtin.skill.dialog.searchPlaceholder")}
            className="w-full rounded-lg border border-[--border-subtle] bg-[--surface-level-2] px-3 py-2 text-sm text-[--text-primary] placeholder:text-[--text-tertiary] focus:border-[--accent-primary] focus:outline-none"
          />

          <div
            ref={listRef}
            className="max-h-[20rem] min-h-[8rem] overflow-y-auto rounded-lg border border-[--border-subtle]"
          >
            {loading ? (
              <div className="flex h-32 items-center justify-center text-sm text-[--text-secondary]">
                {t("composer.builtin.skill.dialog.loading")}
              </div>
            ) : error ? (
              <div className="flex h-32 items-center justify-center text-sm text-[--text-error]">
                {error}
              </div>
            ) : filteredSkills.length === 0 ? (
              <div className="flex h-32 items-center justify-center text-sm text-[--text-secondary]">
                {search.trim()
                  ? t("composer.builtin.skill.dialog.noMatch")
                  : t("composer.builtin.skill.dialog.empty")}
              </div>
            ) : (
              <div className="divide-y divide-[--border-subtle]">
                {filteredSkills.map((skill, index) => (
                  <button
                    key={skill}
                    data-skill-index={index}
                    onClick={() => handleSelect(skill)}
                    onMouseEnter={() => setActiveOverride(index)}
                    className={cn(
                      "w-full px-4 py-2.5 text-left text-sm transition-colors",
                      index === activeIndex
                        ? "bg-[--accent-primary] text-white"
                        : "text-[--text-primary] hover:bg-[--surface-level-3]",
                    )}
                  >
                    {skill}
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>
      </DialogPanel>
    </DialogOverlay>
  );
}
