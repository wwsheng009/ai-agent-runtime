// Schedule 面板的任务分区：活动 / 暂停 / 未运行 / 需要处理 / 失败。
// 分区顺序与标题由调用方按原有顺序传入，行级动作通过 rowProps 统一转发。

import type { RuntimeScheduleTask } from "@/types/runtime";

import { ScheduleTaskRow, type ScheduleTaskRowProps } from "./task-row";

export type ScheduleTaskGroup = {
  destructive?: boolean;
  live: boolean;
  tasks: RuntimeScheduleTask[];
  title: string;
};

type ScheduleTaskSectionsProps = {
  groups: ScheduleTaskGroup[];
  rowProps: Omit<ScheduleTaskRowProps, "live" | "task">;
};

export function ScheduleTaskSections({
  groups,
  rowProps,
}: ScheduleTaskSectionsProps) {
  return (
    <>
      {groups.map((group) =>
        group.tasks.length > 0 ? (
          <section key={group.title}>
            <h3
              className={
                group.destructive
                  ? "mb-1 text-xs font-medium uppercase text-destructive"
                  : "mb-1 text-xs font-medium uppercase text-muted-foreground"
              }
            >
              {group.title}
            </h3>
            <ul className="space-y-2">
              {group.tasks.map((task) => (
                <ScheduleTaskRow
                  key={task.id}
                  live={group.live}
                  task={task}
                  {...rowProps}
                />
              ))}
            </ul>
          </section>
        ) : null,
      )}
    </>
  );
}