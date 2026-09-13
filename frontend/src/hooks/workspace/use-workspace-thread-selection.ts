import {
  startTransition,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";

import { type Artifact, type Thread } from "@/data/mock";
import {
  getFirstArtifactId,
  mergeRuntimeSessionsIntoThreads,
} from "@/lib/workspace-thread-state";
import type { RuntimeSessionRecord } from "@/types/runtime";
import { normalizeSessionId } from "@/lib/session-id";

type WorkspaceThreadSelectionOptions = {
  initialThreads: Thread[];
  runtimeSessions: RuntimeSessionRecord[];
};

type ArtifactSelection = {
  resolvedSelectedArtifactId: string | null;
  selectedArtifact: Artifact | null;
};

type WorkspaceRouteSelection = {
  routeSessionId?: string;
  routeThreadId?: string;
};

export const NEW_THREAD_ID = "new";

// 左侧会话列表点击是否需要弹「切换确认」对话框：只有当前会话仍在生成回复
// （isCurrentThreadResponding）时才确认——空闲状态下的切换不应打断用户；
// 重复点击当前会话、「新建会话」入口同理。
export function shouldConfirmThreadSwitch(
  currentThreadId: string | undefined,
  nextThreadId: string,
  isCurrentThreadResponding: boolean,
): boolean {
  if (
    !isCurrentThreadResponding ||
    !currentThreadId ||
    nextThreadId === NEW_THREAD_ID
  ) {
    return false;
  }

  return nextThreadId !== currentThreadId;
}

// 当前会话是否正在生成回复：本地 turn（activeTurnId）在跑，且这条 turn 的
// 流式占位消息挂在所选线程上。切到别的会话后 turn 仍在后台继续
// （isResponding 依旧为 true），但当前会话已经空闲——只按全局 isResponding
// 判断会把后台会话的生成算到当前会话头上，重复弹确认框。
export function isThreadResponding(
  thread: Thread | undefined,
  activeTurnId: string | null,
): boolean {
  if (!thread || !activeTurnId) {
    return false;
  }

  return thread.messages.some(
    (message) =>
      message.streaming === true && message.runtimeTurnId === activeTurnId,
  );
}

const NEW_THREAD_PROMPTS = [
  "Summarize the repo state before we change anything.",
  "Review the current page for style regressions against DeerFlow.",
  "Plan the next implementation step and then execute it.",
];

export function createDraftThread(): Thread {
  return {
    id: NEW_THREAD_ID,
    title: "New chat",
    summary: "",
    updatedAt: new Date().toISOString(),
    status: "draft",
    lastError: null,
    prompts: NEW_THREAD_PROMPTS,
    tags: ["new"],
    messages: [],
    artifacts: [],
  };
}

function createPendingSessionThread(sessionId: string): Thread {
  return {
    id: sessionId,
    title: `Runtime session ${sessionId.slice(0, 10)}`,
    summary: "Loading the requested runtime session.",
    updatedAt: "1970-01-01T00:00:00.000Z",
    status: "active",
    sessionId,
    transport: "live",
    runtimeSource: "runtime",
    lastError: null,
    prompts: [],
    tags: ["runtime-session", "loading"],
    messages: [],
    artifacts: [],
  };
}

export function resolveSelectedThread(
  threads: Thread[],
  { routeSessionId, routeThreadId }: WorkspaceRouteSelection,
) {
  if (routeThreadId === NEW_THREAD_ID) {
    return createDraftThread();
  }

  const normalizedRouteThreadId = normalizeSessionId(routeThreadId);
  const normalizedRouteSessionId = normalizeSessionId(routeSessionId);

  const directThreadMatch = normalizedRouteThreadId
    ? threads.find(
        (thread) =>
          normalizeSessionId(thread.id) === normalizedRouteThreadId ||
          normalizeSessionId(thread.sessionId) === normalizedRouteThreadId,
      )
    : undefined;

  if (directThreadMatch) {
    return directThreadMatch;
  }

  const directSessionMatch = normalizedRouteSessionId
    ? threads.find(
        (thread) =>
          normalizeSessionId(thread.sessionId) === normalizedRouteSessionId ||
          normalizeSessionId(thread.id) === normalizedRouteSessionId,
      )
    : undefined;

  if (directSessionMatch) {
    return directSessionMatch;
  }
  if (normalizedRouteSessionId) {
    // Keep a deep-linked session selected while the pinned-session request is
    // still in flight. Falling back to threads[0] here would replace the URL
    // before the requested session has a chance to load.
    return createPendingSessionThread(normalizedRouteSessionId);
  }
  return threads[0];
}

export function buildWorkspaceThreadPath(thread: Thread | undefined) {
  if (!thread) {
    return "/workspace/chats/new";
  }

  if (thread.id === NEW_THREAD_ID) {
    return "/workspace/chats/new";
  }

  const sessionId = normalizeSessionId(thread.sessionId);
  if (sessionId) {
    return `/workspace/sessions/${encodeURIComponent(sessionId)}`;
  }

  return `/workspace/chats/${encodeURIComponent(thread.id)}`;
}

export function resolveArtifactSelection(
  selectedThread: Thread | undefined,
  selectedArtifactId: string | null,
): ArtifactSelection {
  if (!selectedThread) {
    return {
      resolvedSelectedArtifactId: null,
      selectedArtifact: null,
    };
  }

  const resolvedSelectedArtifactId = selectedThread.artifacts.some(
    (artifact) => artifact.id === selectedArtifactId,
  )
    ? selectedArtifactId
    : getFirstArtifactId(selectedThread);

  const selectedArtifact =
    selectedThread.artifacts.find(
      (artifact) => artifact.id === resolvedSelectedArtifactId,
    ) ??
    selectedThread.artifacts[0] ??
    null;

  return {
    resolvedSelectedArtifactId,
    selectedArtifact,
  };
}

export function useWorkspaceThreadSelection({
  initialThreads,
  runtimeSessions,
}: WorkspaceThreadSelectionOptions) {
  const location = useLocation();
  const navigate = useNavigate();
  const {
    sessionId: routeSessionId,
    threadId: routeThreadId,
  } = useParams<{ sessionId?: string; threadId?: string }>();
  const [threadState, setThreadState] = useState(initialThreads);
  const threads = useMemo(
    () => mergeRuntimeSessionsIntoThreads(threadState, runtimeSessions),
    [runtimeSessions, threadState],
  );
  const setThreads = useCallback<Dispatch<SetStateAction<Thread[]>>>(
    (nextState) => {
      setThreadState((current) => {
        const mergedCurrent = mergeRuntimeSessionsIntoThreads(current, runtimeSessions);
        return typeof nextState === "function"
          ? nextState(mergedCurrent)
          : nextState;
      });
    },
    [runtimeSessions],
  );

  const selectedThread = useMemo(
    () =>
      resolveSelectedThread(threads, {
        routeSessionId,
        routeThreadId,
      }),
    [routeSessionId, routeThreadId, threads],
  );
  const [selectedArtifactId, setSelectedArtifactId] = useState<string | null>(
    getFirstArtifactId(selectedThread),
  );

  useEffect(() => {
    if (!selectedThread) {
      return;
    }

    const canonicalPath = buildWorkspaceThreadPath(selectedThread);
    if (location.pathname !== canonicalPath) {
      navigate(canonicalPath, { replace: true });
    }
  }, [location.pathname, navigate, selectedThread]);

  function handleSelectThread(threadId: string) {
    if (threadId === NEW_THREAD_ID) {
      startTransition(() => {
        setSelectedArtifactId(null);
        navigate("/workspace/chats/new");
      });
      return;
    }

    const nextThread = threads.find((thread) => thread.id === threadId);
    if (!nextThread) {
      return;
    }

    startTransition(() => {
      setSelectedArtifactId(getFirstArtifactId(nextThread));
      navigate(buildWorkspaceThreadPath(nextThread));
    });
  }

  function handleSelectArtifact(artifactId: string) {
    setSelectedArtifactId(artifactId);
  }

  const { resolvedSelectedArtifactId, selectedArtifact } = resolveArtifactSelection(
    selectedThread,
    selectedArtifactId,
  );

  return {
    onSelectArtifact: handleSelectArtifact,
    onSelectThread: handleSelectThread,
    selectedArtifact,
    selectedArtifactId: resolvedSelectedArtifactId,
    selectedThread,
    setSelectedArtifactId,
    setThreads,
    threads,
  };
}
