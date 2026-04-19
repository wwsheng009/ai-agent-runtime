import type { RuntimeModelsResponse } from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

export async function listRuntimeModels(): Promise<RuntimeModelsResponse> {
  return fetchRuntimeJson<RuntimeModelsResponse>(buildRuntimeUrl("/api/runtime/models"));
}
