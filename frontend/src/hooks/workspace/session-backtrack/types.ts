// 由 hooks/workspace/use-session-backtrack.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Dispatch, type SetStateAction } from "react";

import type { Thread } from "@/data/mock";
import {
  type RuntimeSessionBacktrackMode,
  type RuntimeSessionBacktrackResult,
  type SessionHistoryResponse,
} from "@/lib/runtime-api";

export type SessionBacktrackTarget = {
  messageId: string;
  messageIndex: number;
  userTurnIndex: number;
  preview: string;
  /** Full original user text used to seed the edit box (not truncated). */
  fullText: string;
};

export type SessionBacktrackDialogState = {
  open: boolean;
  busy: boolean;
  error: string | null;
  target: SessionBacktrackTarget | null;
  preview: RuntimeSessionBacktrackResult | null;
  mode: RuntimeSessionBacktrackMode;
  prefillComposer: boolean;
  /** Editable prompt; empty means keep original anchor text for prefill. */
  editPrompt: string;
};

export type SessionBacktrackNavigationState = {
  active: boolean;
  selectedMessageId: string | null;
};

export type UseSessionBacktrackOptions = {
  applySessionHistoryToThread: (
    thread: Thread,
    response: SessionHistoryResponse,
  ) => Thread;
  isResponding: boolean;
  selectedThread: Thread | undefined;
  setDraft: (value: string) => void;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  /** Current composer draft; used for empty-composer Esc navigation. */
  draft?: string;
};

export const initialDialogState: SessionBacktrackDialogState = {
  open: false,
  busy: false,
  error: null,
  target: null,
  preview: null,
  mode: "conversation",
  prefillComposer: true,
  editPrompt: "",
};

export const initialNavigationState: SessionBacktrackNavigationState = {
  active: false,
  selectedMessageId: null,
};
