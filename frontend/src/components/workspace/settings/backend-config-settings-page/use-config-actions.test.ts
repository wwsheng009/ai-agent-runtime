import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  getRuntimeConfigDocument,
  saveRuntimeConfigDocument,
} from "@/lib/runtime-api";
import { saveRuntimeShellSettings } from "@/api/runtime/config";

import { createConfigEditorActions } from "./use-config-actions";
import { type ConfigEditorState } from "./use-config-state";

vi.mock("@/lib/runtime-api", () => ({
  getRuntimeConfigDocument: vi.fn(),
  getRuntimeServiceStatus: vi.fn(),
  previewRuntimeConfigDocument: vi.fn(),
  restartRuntimeService: vi.fn(),
  saveRuntimeConfigDocument: vi.fn(),
}));
vi.mock("@/api/runtime/config", () => ({
  saveRuntimeShellSettings: vi.fn(),
}));
vi.mock("@/lib/runtime-model-catalog-sync", () => ({
  notifyRuntimeModelCatalogChanged: vi.fn(),
}));

const getRuntimeConfigDocumentMock = vi.mocked(getRuntimeConfigDocument);
const saveRuntimeConfigDocumentMock = vi.mocked(saveRuntimeConfigDocument);
const saveRuntimeShellSettingsMock = vi.mocked(saveRuntimeShellSettings);

function makeDocument(parsed: unknown) {
  return {
    raw: "parsed-document",
    parsed,
    restart_required: false,
  } as never;
}

function makeState(document: unknown, draftParsed: unknown): ConfigEditorState {
  return {
    document,
    draftErrorCount: 0,
    draftParsed,
    draftRaw: "parsed-document",
    hasDraftErrors: false,
    hasDomainChanges: true,
    hasSourceChanges: false,
    hasUnsavedChanges: true,
    mode: "shell",
    setDocument: vi.fn(),
    setDraftParsed: vi.fn(),
    setDraftRaw: vi.fn(),
    setError: vi.fn(),
    setIsLoading: vi.fn(),
    setIsModeSwitching: vi.fn(),
    setIsPreviewLoading: vi.fn(),
    setIsRestarting: vi.fn(),
    setIsSaving: vi.fn(),
    setMode: vi.fn(),
    setPreviewDocument: vi.fn(),
    setServiceError: vi.fn(),
    setServiceStatus: vi.fn(),
    setStatusMessage: vi.fn(),
    t: ((key: string) => key) as ConfigEditorState["t"],
  } as unknown as ConfigEditorState;
}

describe("saveDocument shell routing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getRuntimeConfigDocumentMock.mockResolvedValue(makeDocument({}));
    saveRuntimeConfigDocumentMock.mockImplementation(async () =>
      makeDocument({}),
    );
    saveRuntimeShellSettingsMock.mockResolvedValue({
      auto_detect: true,
      prefer: ["git-bash"],
      path: "",
      probe_timeout: "3s",
      strict_probe: true,
      script_delivery: "stdin",
    });
  });

  it("persists a shell-only edit through the dedicated API and skips the generic document writer", async () => {
    const current = {
      providers: { default_provider: "p" },
      shell: { prefer: ["powershell"], strictProbe: true },
    };
    const draft = {
      providers: { default_provider: "p" },
      shell: { prefer: ["git-bash"], strictProbe: true },
    };
    const refreshed = makeDocument(draft);
    const calls: string[] = [];
    getRuntimeConfigDocumentMock.mockImplementation(async () => {
      calls.push("reload");
      return refreshed;
    });
    saveRuntimeShellSettingsMock.mockImplementation(async () => {
      calls.push("shell");
      return {
        auto_detect: true,
        prefer: ["git-bash"],
        path: "",
        probe_timeout: "3s",
        strict_probe: true,
        script_delivery: "stdin",
      };
    });

    const actions = createConfigEditorActions(makeState(makeDocument(current), draft));
    await actions.saveDocument();

    expect(calls).toEqual(["shell", "reload"]);
    expect(saveRuntimeShellSettingsMock).toHaveBeenCalledWith(
      expect.objectContaining({ prefer: ["git-bash"] }),
    );
    expect(saveRuntimeConfigDocumentMock).not.toHaveBeenCalled();
  });

  it("strips shell edits from a mixed generic document save", async () => {
    const current = {
      providers: { default_provider: "old" },
      shell: { prefer: ["powershell"], strictProbe: true },
    };
    const draft = {
      providers: { default_provider: "new" },
      shell: { prefer: ["git-bash"], strictProbe: true },
    };
    getRuntimeConfigDocumentMock.mockResolvedValue(makeDocument(draft));

    const actions = createConfigEditorActions(makeState(makeDocument(current), draft));
    await actions.saveDocument();

    expect(saveRuntimeShellSettingsMock).toHaveBeenCalledOnce();
    expect(saveRuntimeConfigDocumentMock).toHaveBeenCalledWith(
      expect.objectContaining({
        mode: "structured",
        parsed: {
          providers: { default_provider: "new" },
          shell: current.shell,
        },
      }),
    );
  });

  it("routes raw shell-node changes even when the effective defaults are unchanged", async () => {
    const current = {
      providers: { default_provider: "p" },
    };
    const draft = {
      providers: { default_provider: "p" },
      shell: {
        autoDetect: true,
        strictProbe: true,
        scriptDelivery: "stdin",
      },
    };
    getRuntimeConfigDocumentMock.mockResolvedValue(makeDocument(draft));

    const actions = createConfigEditorActions(
      makeState(makeDocument(current), draft),
    );
    await actions.saveDocument();

    expect(saveRuntimeShellSettingsMock).toHaveBeenCalledOnce();
    expect(saveRuntimeConfigDocumentMock).not.toHaveBeenCalled();
  });

  it("saves generic changes before the dedicated shell writer", async () => {
    const current = {
      providers: { default_provider: "old" },
      shell: { prefer: ["powershell"], strictProbe: true },
    };
    const draft = {
      providers: { default_provider: "new" },
      shell: { prefer: ["git-bash"], strictProbe: true },
    };
    const calls: string[] = [];
    saveRuntimeConfigDocumentMock.mockImplementation(async () => {
      calls.push("document");
      return makeDocument({
        providers: { default_provider: "new" },
        shell: current.shell,
      });
    });
    saveRuntimeShellSettingsMock.mockImplementation(async () => {
      calls.push("shell");
      return {
        auto_detect: true,
        prefer: ["git-bash"],
        path: "",
        probe_timeout: "3s",
        strict_probe: true,
        script_delivery: "stdin",
      };
    });
    getRuntimeConfigDocumentMock.mockImplementation(async () => {
      calls.push("reload");
      return makeDocument(draft);
    });

    const actions = createConfigEditorActions(
      makeState(makeDocument(current), draft),
    );
    await actions.saveDocument();

    // The generic writer must run while the file still contains the old shell
    // value. Running the dedicated writer last keeps its result authoritative.
    expect(calls).toEqual(["document", "shell", "reload"]);
  });
});
