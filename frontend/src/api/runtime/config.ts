import type {
  RuntimeAgentRoutePreviewRequest,
  RuntimeAgentRoutePreviewResponse,
  RuntimeAgentMaxStepsResponse,
  RuntimeAgentMaxStepsSaveRequest,
  RuntimeAgentMaxStepsSaveResponse,
  RuntimeConfigDocumentResponse,
  RuntimeConfigDocumentSaveRequest,
  RuntimeConfigDocumentSaveResponse,
  RuntimeServiceRestartResponse,
  RuntimeServiceStatusResponse,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

const runtimeConfigDocumentUrl = buildRuntimeUrl("/api/runtime/config/document");
const runtimeConfigPreviewUrl = buildRuntimeUrl("/api/runtime/config/document/preview");
const runtimeAgentRoutePreviewUrl = buildRuntimeUrl(
  "/api/runtime/config/document/agent-route-preview",
);
const runtimeSkillsConfigWriteUrl = buildRuntimeUrl("/api/runtime/skills/config/write");
const runtimeAgentMaxStepsUrl = buildRuntimeUrl(
  "/api/runtime/config/agent/max-steps",
);
const runtimeServiceUrl = buildRuntimeUrl("/api/runtime/service");
const runtimeServiceRestartUrl = buildRuntimeUrl("/api/runtime/service/restart");

export async function getRuntimeConfigDocument() {
  const response = await fetchRuntimeJson<RuntimeConfigDocumentResponse>(
    runtimeConfigDocumentUrl,
  );
  return response.document;
}

export async function saveRuntimeConfigDocument(
  request: RuntimeConfigDocumentSaveRequest,
) {
  const response = await fetchRuntimeJson<RuntimeConfigDocumentSaveResponse>(
    runtimeConfigDocumentUrl,
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
  return response.document;
}

export async function writeRuntimeConfigDocument(
  request: RuntimeConfigDocumentSaveRequest,
) {
  const response = await fetchRuntimeJson<RuntimeConfigDocumentSaveResponse>(
    runtimeSkillsConfigWriteUrl,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
  return response.document;
}

export async function previewRuntimeConfigDocument(
  request: RuntimeConfigDocumentSaveRequest,
) {
  const response = await fetchRuntimeJson<RuntimeConfigDocumentResponse>(
    runtimeConfigPreviewUrl,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
  return response.document;
}

/**
 * 保存「最大步骤数」到后端：runtime 内存快照与 runtime 配置文件一起更新。
 *
 * 前端本地设置仍会随每轮 chat 请求带上 max_steps（对下一轮立即生效），
 * 这里负责让后端缺省值（请求未指定 max_steps 时）与配置文件保持一致。
 */
export async function saveRuntimeAgentMaxSteps(
  request: RuntimeAgentMaxStepsSaveRequest,
) {
  return fetchRuntimeJson<RuntimeAgentMaxStepsSaveResponse>(
    runtimeAgentMaxStepsUrl,
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
}

/**
 * 读取后端缺省的最大步骤数（runtime 内存快照里的 agent.maxSteps）与来源配置文件路径。
 *
 * 该值只影响「请求未携带 max_steps」时；工作区设置会随每轮请求携带自己的值，
 * 因此这里读到的数字用于显示服务端缺省，而不是本轮生效值。
 */
export async function getRuntimeAgentMaxSteps() {
  return fetchRuntimeJson<RuntimeAgentMaxStepsResponse>(runtimeAgentMaxStepsUrl);
}

export async function previewRuntimeAgentRoute(
  request: RuntimeAgentRoutePreviewRequest,
) {
  const response = await fetchRuntimeJson<RuntimeAgentRoutePreviewResponse>(
    runtimeAgentRoutePreviewUrl,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    },
  );
  return response.route;
}

export async function getRuntimeServiceStatus() {
  const response =
    await fetchRuntimeJson<RuntimeServiceStatusResponse>(runtimeServiceUrl);
  return response.service;
}

export async function restartRuntimeService() {
  const response = await fetchRuntimeJson<RuntimeServiceRestartResponse>(
    runtimeServiceRestartUrl,
    {
      method: "POST",
    },
  );
  return response.restart;
}
