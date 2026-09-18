import { useEffect, useState } from "react";

import { listRuntimeSkills } from "@/api/runtime/skills";
import type { RuntimeSkillCatalog } from "@/types/runtime";

export function useRuntimeSkillCatalog() {
  const [runtimeSkills, setRuntimeSkills] = useState<RuntimeSkillCatalog | null>(null);
  const [runtimeSkillsError, setRuntimeSkillsError] = useState<string | null>(null);
  const [runtimeSkillsLoading, setRuntimeSkillsLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;

    const loadSkills = async () => {
      setRuntimeSkillsLoading(true);
      setRuntimeSkillsError(null);

      try {
        const response = await listRuntimeSkills();
        if (!cancelled) {
          setRuntimeSkills(response);
          setRuntimeSkillsError(null);
        }
      } catch (error) {
        if (!cancelled) {
          setRuntimeSkillsError(
            error instanceof Error ? error.message : String(error),
          );
        }
      } finally {
        if (!cancelled) {
          setRuntimeSkillsLoading(false);
        }
      }
    };

    void loadSkills();

    return () => {
      cancelled = true;
    };
  }, []);

  return {
    runtimeSkills,
    runtimeSkillsError,
    runtimeSkillsLoading,
  };
}
