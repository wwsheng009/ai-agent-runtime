const runtimeModelCatalogInvalidateEvent =
  "workspace:runtime-model-catalog-invalidated";
const runtimeModelCatalogInvalidateStorageKey =
  "workspace.runtime.modelCatalogInvalidatedAt";

function getBrowserStorage() {
  if (typeof window === "undefined") {
    return null;
  }
  return window.localStorage;
}

export function notifyRuntimeModelCatalogChanged() {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new Event(runtimeModelCatalogInvalidateEvent));
  }

  const storage = getBrowserStorage();
  if (!storage) {
    return;
  }

  storage.setItem(
    runtimeModelCatalogInvalidateStorageKey,
    `${Date.now()}:${Math.random().toString(36).slice(2)}`,
  );
}

export function subscribeRuntimeModelCatalogInvalidation(
  onInvalidate: () => void,
) {
  if (typeof window === "undefined") {
    return () => {};
  }

  const handleInvalidate = () => {
    onInvalidate();
  };
  const handleStorage = (event: StorageEvent) => {
    if (event.key !== runtimeModelCatalogInvalidateStorageKey) {
      return;
    }
    onInvalidate();
  };

  window.addEventListener(runtimeModelCatalogInvalidateEvent, handleInvalidate);
  window.addEventListener("storage", handleStorage);

  return () => {
    window.removeEventListener(
      runtimeModelCatalogInvalidateEvent,
      handleInvalidate,
    );
    window.removeEventListener("storage", handleStorage);
  };
}
