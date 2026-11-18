// MessageComposer 的 props 契约（从组件文件拆出，控制单文件非空行数）。
import type { ReactNode } from "react";

import { type ComposerCommandResultNotice } from "@/components/workspace/composer-command-result-notice";
import { type Thread } from "@/data/mock";
import { type ComposerAttachmentsController } from "@/hooks/workspace/composer/use-composer-attachments";
import { type ComposerMenuState } from "@/hooks/workspace/composer/use-composer-menu";
import {
  type ComposerCommand,
  type ComposerCommandDefinition,
} from "@/lib/composer-commands";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";

export type MessageComposerProps = {
  /** P1-4 子片 2：附件草稿轨（数据与上传状态由 owner hook 持有）。 */
  attachments: ComposerAttachmentsController;
  /** P1-4 子片 3：斜杠命令表（内置命令清单与执行器归 P2-7）。 */
  commands?: readonly ComposerCommandDefinition[];
  /** P2-7：命令执行结果通知（宿主已本地化；错误 alert、成功 status）。 */
  commandResultNotice?: ComposerCommandResultNotice | null;
  /**
   * 会话上下文进度控件（宿主注入，composer 不直接耦合 runtime API）。
   * 座位固定在发送按钮左侧：进度环 + 上下文明细/手动压缩面板。
   */
  contextUsageControl?: ReactNode;
  density: "comfortable" | "compact";
  draft: string;
  /** 会话身份；变化（切换会话/新建线程落地）时输入框回焦。 */
  focusKey?: string;
  hasSession: boolean;
  isNewThread?: boolean;
  isResponding: boolean;
  modelOptions: string[];
  onModelChange: (value: string) => void;
  /**
   * P0：`@` 菜单可见状态回调（宿主用 query 拉取工作区文件候选；菜单关闭时不得触发请求）。
   */
  onMenuStateChange?: (state: ComposerMenuState) => void;
  /** P1-4 子片 3：`/` 命令派发；返回 true 表示已处理（否则给出未接入提示，不降级为 prompt）。 */
  onCommand?: (
    command: ComposerCommand,
    args: string,
    source: "pick" | "submit",
  ) => boolean | void;
  onProviderChange: (value: string) => void;
  onDismissCommandResult?: () => void;
  onReasoningEffortChange: (value: string) => void;
  /** P1-x：会话权限选择器（由宿主注入，composer 不直接耦合 runtime API）。 */
  permissionModeControl?: ReactNode;
  /** Composer profile 切换器（宿主注入；渲染在 context 用量图标左侧，纪律同 permissionModeControl）。 */
  profileControl?: ReactNode;
  providerOptions: string[];
  reasoningEffortDefault: string;
  reasoningEffortError: string | null;
  reasoningEffortOptions: string[];
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  /** P1-4 子片 3：`@` 引用候选分组（文件/会话/子代理）。 */
  referenceGroups?: readonly ComposerReferenceGroup[];
  /** 文本类 skill 的 `$` 提及候选分组（运行时技能目录）。 */
  skillGroups?: readonly ComposerReferenceGroup[];
  selectedModel: string;
  selectedProvider: string;
  selectedReasoningEffort: string;
  transport?: Thread["transport"];
  onDraftChange: (value: string) => void;
  onStop: () => void;
  /**
   * 提交回合；返回 false = 被拦下（草稿为空 / 会话未就绪 / 同会话仍有在途
   * 回合 / 附件未上传完成）。composer 据此给出可见反馈，绝不静默吞掉一次点击。
   *
   * `images` 只包含**已上传成功**的服务端路径（S5）；调用方按
   * `submit_prompt.images` 投递，绝不代发未上传项。
   */
  onSubmit: (options?: { images?: readonly string[] }) => boolean | void;
};