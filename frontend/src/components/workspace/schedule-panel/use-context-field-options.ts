// 「执行上下文」分区的远端选项加载与派生。把 sessions / workspaces /
// profiles / models 的拉取及下拉选项构造从渲染中拆出，组件只负责布局。

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { listRuntimeModels } from "@/api/runtime/models";
import { listRuntimeProfiles } from "@/api/runtime/profiles";
import { listRuntimeSessions } from "@/api/runtime/sessions";
import { listWorkspaceDirectories } from "@/api/runtime/workspace-directories";
import type {
  RuntimeModelsResponse,
  RuntimeProfileListEntry,
  RuntimeSessionRecord,
  RuntimeWorkspaceDirectory,
} from "@/types/runtime";

import type { CreateFormState } from "./form-state-types";

export function useScheduleContextOptions(createForm: CreateFormState) {
  const { t } = useTranslation("workspace");
  const [sessions, setSessions] = useState<RuntimeSessionRecord[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(false);
  const [sessionsLoadFailed, setSessionsLoadFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setSessionsLoading(true);
    setSessionsLoadFailed(false);
    void listRuntimeSessions()
      .then((response) => {
        if (!cancelled) {
          setSessions(response.sessions);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSessionsLoadFailed(true);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setSessionsLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // 工作区目录：给「目标工作区」选择器供数。列表不可用时字段退回手输。
  const [workspaces, setWorkspaces] = useState<RuntimeWorkspaceDirectory[]>([]);
  const [workspacesLoading, setWorkspacesLoading] = useState(false);
  const [workspacesLoadFailed, setWorkspacesLoadFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setWorkspacesLoading(true);
    setWorkspacesLoadFailed(false);
    void listWorkspaceDirectories()
      .then((response) => {
        if (!cancelled) {
          setWorkspaces(response.directories);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setWorkspacesLoadFailed(true);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setWorkspacesLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Profile 清单：只在「无人值守 / 工作区」用到，仍统一加载一次，
  // 切换执行上下文时无需再等待。
  const [profiles, setProfiles] = useState<RuntimeProfileListEntry[]>([]);
  const [profilesLoading, setProfilesLoading] = useState(false);
  const [profilesLoadFailed, setProfilesLoadFailed] = useState(false);
  const [runtimeModels, setRuntimeModels] = useState<RuntimeModelsResponse | null>(null);
  const [modelsLoading, setModelsLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setProfilesLoading(true);
    setProfilesLoadFailed(false);
    void listRuntimeProfiles()
      .then((response) => {
        if (!cancelled) {
          setProfiles(response.profiles);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setProfilesLoadFailed(true);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setProfilesLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    setModelsLoading(true);
    void listRuntimeModels()
      .then((response) => {
        if (!cancelled) setRuntimeModels(response);
      })
      .catch(() => {
        if (!cancelled) setRuntimeModels(null);
      })
      .finally(() => {
        if (!cancelled) setModelsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const workspaceListUnavailable =
    workspacesLoadFailed || (!workspacesLoading && workspaces.length === 0);
  const profileListUnavailable =
    profilesLoadFailed || (!profilesLoading && profiles.length === 0);
  const providerOptions = (runtimeModels?.providers ?? []).map((provider) => ({
    value: provider.name,
    label: provider.name,
  }));
  const selectedProvider = runtimeModels?.providers.find(
    (provider) => provider.name === createForm.provider,
  );
  const modelOptions = (selectedProvider?.models ?? []).map((model) => ({
    value: model,
    label: model,
  }));
  const reasoningOptions =
    selectedProvider?.model_capabilities?.[createForm.model]?.reasoning_efforts ?? [];

  // 值用目录路径（后端按路径识别工作区），标签优先显示目录名。
  const workspaceOptions = [
    ...(workspacesLoading
      ? [
          {
            value: "",
            label: t("panels.schedule.workspaceLoading"),
            disabled: true,
          },
        ]
      : []),
    ...workspaces.map((directory) => ({
      value: directory.path,
      label: directory.name?.trim()
        ? `${directory.name.trim()} · ${directory.path}`
        : directory.path,
    })),
  ].filter(
    (option) =>
      option.value !== "" ||
      createForm.targetWorkspace === "" ||
      workspacesLoading,
  );

  // 已填但不在清单里的值也要能显示出来，否则选中的工作区会显示成空。
  const hasSelectedWorkspace =
    createForm.targetWorkspace.length > 0 &&
    !workspaces.some((directory) => directory.path === createForm.targetWorkspace);
  if (hasSelectedWorkspace) {
    workspaceOptions.unshift({
      value: createForm.targetWorkspace,
      label: createForm.targetWorkspace,
    });
  }

  const profileOptions = [
    ...profiles.map((profile) => ({
      value: profile.ref,
      label: profile.name?.trim()
        ? `${profile.name.trim()} · ${profile.ref}`
        : profile.ref,
    })),
  ];
  const hasSelectedProfile =
    createForm.targetProfile.length > 0 &&
    !profiles.some((profile) => profile.ref === createForm.targetProfile);
  if (hasSelectedProfile) {
    profileOptions.unshift({
      value: createForm.targetProfile,
      label: createForm.targetProfile,
    });
  }

  const [manualSession, setManualSession] = useState(false);
  const sessionListUnavailable =
    sessionsLoadFailed || (!sessionsLoading && sessions.length === 0);
  const showSessionInput = manualSession || sessionListUnavailable;

  const hasSelectedSession =
    createForm.sessionId.length > 0 &&
    !sessions.some((session) => session.id === createForm.sessionId);
  const sessionOptions = [
    {
      value: "",
      label: sessionsLoading
        ? t("panels.schedule.sessionLoading")
        : t("panels.schedule.sessionSelectPlaceholder"),
    },
    ...(hasSelectedSession
      ? [{ value: createForm.sessionId, label: createForm.sessionId }]
      : []),
    ...sessions.map((session) => ({
      value: session.id,
      label: session.metadata?.title?.trim()
        ? `${session.metadata.title.trim()} · ${session.id}`
        : session.id,
    })),
  ];

  return {
    modelOptions,
    modelsLoading,
    profileListUnavailable,
    profileOptions,
    profilesLoading,
    providerOptions,
    reasoningOptions,
    runtimeModels,
    selectedProvider,
    sessionListUnavailable,
    sessionOptions,
    sessionsLoadFailed,
    sessionsLoading,
    setManualSession,
    showSessionInput,
    workspaceListUnavailable,
    workspaceOptions,
    workspacesLoadFailed,
    workspacesLoading,
  };
}