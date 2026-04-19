import type {
  RuntimeConfigDocumentResponse,
  RuntimeConfigDocumentSaveRequest,
  RuntimeConfigDocumentSaveResponse,
  RuntimeServiceRestartResponse,
  RuntimeServiceStatusResponse,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

const runtimeConfigDocumentUrl = buildRuntimeUrl("/api/runtime/config/document");
const runtimeConfigPreviewUrl = buildRuntimeUrl("/api/runtime/config/document/preview");
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
