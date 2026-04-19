import {
  startTransition,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import { useNavigate, useParams } from "react-router-dom";

import { type Artifact, type Thread } from "@/data/mock";
import {
  getFirstArtifactId,
  mergeRuntimeSessionsIntoThreads,
} from "@/lib/workspace-thread-state";
import type { RuntimeSessionRecord } from "@/types/runtime";

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

export function resolveSelectedThread(
  threads: Thread[],
  { routeSessionId, routeThreadId }: WorkspaceRouteSelection,
) {
  if (routeThreadId === NEW_THREAD_ID) {
    return createDraftThread();
  }

  const directThreadMatch = routeThreadId
    ? threads.find(
        (thread) =>
          thread.id === routeThreadId || thread.sessionId === routeThreadId,
      )
    : undefined;

  if (directThreadMatch) {
    return directThreadMatch;
  }

  const directSessionMatch = routeSessionId
    ? threads.find(
        (thread) =>
          thread.sessionId === routeSessionId || thread.id === routeSessionId,
      )
    : undefined;

  return directSessionMatch ?? threads[0];
}

export function buildWorkspaceThreadPath(thread: Thread | undefined) {
  if (!thread) {
    return "/workspace/chats/new";
  }

  if (thread.id === NEW_THREAD_ID) {
    return "/workspace/chats/new";
  }

  if (thread.sessionId) {
    return `/workspace/sessions/${thread.sessionId}`;
  }

  return `/workspace/chats/${thread.id}`;
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

  const selectedThread = resolveSelectedThread(threads, {
    routeSessionId,
    routeThreadId,
  });
  const [selectedArtifactId, setSelectedArtifactId] = useState<string | null>(
    getFirstArtifactId(selectedThread),
  );

  useEffect(() => {
    if (!selectedThread) {
      return;
    }

    const canonicalPath = buildWorkspaceThreadPath(selectedThread);
    const currentPath = routeSessionId
      ? `/workspace/sessions/${routeSessionId}`
      : routeThreadId
        ? `/workspace/chats/${routeThreadId}`
        : "/workspace";

    if (currentPath !== canonicalPath) {
      navigate(canonicalPath, { replace: true });
    }
  }, [navigate, routeSessionId, routeThreadId, selectedThread]);

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
