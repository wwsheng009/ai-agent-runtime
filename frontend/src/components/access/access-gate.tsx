import { type FormEvent, type ReactNode, useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { getRuntimeAccessStatus, loginRuntimeAccess } from "@/api/runtime/access";
import { getRuntimeRouterBasename, RuntimeApiError } from "@/api/runtime/shared";
import { Button } from "@/components/ui/button";
import type { RuntimeAccessStatus } from "@/types/runtime";

const accessUnauthorizedEvent = "runtime:access-unauthorized";

export function AccessGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation("common");
  const [status, setStatus] = useState<RuntimeAccessStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [token, setToken] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState("");

  const refresh = useCallback(async () => {
    setLoading(true);
    setLoadError("");
    try {
      setStatus(await getRuntimeAccessStatus());
    } catch (error) {
      if (error instanceof RuntimeApiError && error.status === 401) {
        const loginPath = gatewayLoginRedirectPath();
        if (loginPath) {
          window.location.assign(loginPath);
          return;
        }
      }
      setStatus(null);
      setLoadError(error instanceof Error ? error.message : String(error));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    const onUnauthorized = () => {
      setStatus((current) =>
        current ? { ...current, authenticated: false, required: true } : current,
      );
      void refresh();
    };
    window.addEventListener(accessUnauthorizedEvent, onUnauthorized);
    return () => window.removeEventListener(accessUnauthorizedEvent, onUnauthorized);
  }, [refresh]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const value = token.trim();
    if (!value) {
      setFormError(t("access.required"));
      return;
    }
    setSubmitting(true);
    setFormError("");
    try {
      await loginRuntimeAccess(value);
      setStatus((current) =>
        current
          ? { ...current, authenticated: true, required: true }
          : { required: true, authenticated: true, local: false, configured: true },
      );
      setToken("");
    } catch (error) {
      if (error instanceof RuntimeApiError && error.status === 401) {
        setFormError(t("access.invalid"));
      } else if (error instanceof RuntimeApiError && error.status === 503) {
        setLoadError(t("access.notConfigured"));
      } else {
        setFormError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      setSubmitting(false);
    }
  }

  if (loading) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-[var(--workspace-shell-bg)] px-4 text-sm text-muted-foreground">
        {t("access.loading")}
      </main>
    );
  }

  if (loadError) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-[var(--workspace-shell-bg)] px-4">
        <section className="w-full max-w-md rounded-panel-lg border border-border bg-surface-solid p-5 shadow-[var(--elevation-panel)]">
          <h1 className="text-lg font-semibold text-foreground">{t("access.title")}</h1>
          <p role="alert" className="mt-2 break-all text-sm text-analytics-danger">
            {t("access.loadFailed")}: {loadError}
          </p>
          <Button className="mt-4 w-full" onClick={() => void refresh()}>
            {t("access.retry")}
          </Button>
        </section>
      </main>
    );
  }

  if (!status || !status.required || status.authenticated) {
    return <>{children}</>;
  }

  if (!status.configured) {
    return (
      <main className="flex min-h-screen items-center justify-center bg-[var(--workspace-shell-bg)] px-4">
        <section className="w-full max-w-md rounded-panel-lg border border-border bg-surface-solid p-5 shadow-[var(--elevation-panel)]">
          <h1 className="text-lg font-semibold text-foreground">{t("access.title")}</h1>
          <p role="alert" className="mt-2 text-sm text-analytics-warning">
            {t("access.notConfigured")}
          </p>
        </section>
      </main>
    );
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-[var(--workspace-shell-bg)] px-4">
      <form
        onSubmit={submit}
        className="w-full max-w-md rounded-panel-lg border border-border bg-surface-solid p-5 shadow-[var(--elevation-panel)]"
      >
        <h1 className="text-lg font-semibold text-foreground">{t("access.title")}</h1>
        <p className="mt-1 text-sm leading-6 text-muted-foreground">
          {t("access.description")}
        </p>
        <label className="mt-4 block text-xs font-medium text-foreground" htmlFor="runtime-access-token">
          {t("access.tokenLabel")}
        </label>
        <input
          id="runtime-access-token"
          type="password"
          autoComplete="off"
          autoFocus
          value={token}
          onChange={(event) => setToken(event.target.value)}
          placeholder={t("access.tokenPlaceholder")}
          aria-invalid={Boolean(formError)}
          aria-describedby={formError ? "runtime-access-token-error" : undefined}
          className="mt-1.5 w-full rounded-field border border-border bg-surface-softer px-3 py-2 text-sm text-foreground outline-none focus:border-accent-primary-border focus:ring-1 focus:ring-ring"
        />
        {formError ? (
          <p id="runtime-access-token-error" role="alert" className="mt-2 text-xs text-analytics-danger">
            {formError}
          </p>
        ) : null}
        <Button type="submit" className="mt-4 w-full" disabled={submitting}>
          {submitting ? t("access.submitting") : t("access.submit")}
        </Button>
        <p className="mt-3 text-center text-xs text-muted-foreground">{t("access.localHint")}</p>
      </form>
    </main>
  );
}

// When the SPA is mounted under /n/{node}/, a 401 comes from the gateway owner
// gate (not the runtime access guard). Send the browser to the gateway login
// page with a return path so the user only logs in once and comes back without
// any runtime token prompt.
function gatewayLoginRedirectPath() {
  const basename = getRuntimeRouterBasename();
  const marker = basename.indexOf("/n/");
  if (marker < 0) {
    return "";
  }
  const root = basename.slice(0, marker) || "/";
  const next = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  return `${root}?next=${encodeURIComponent(next)}`;
}
