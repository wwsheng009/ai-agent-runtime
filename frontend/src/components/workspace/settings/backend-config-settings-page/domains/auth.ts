// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { type RuntimeAuthConfigSummary } from "../../runtime-config-domain-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createAuthDomain(core: ConfigEditorCore) {
  const {
    setDraftParsed,
  } = core;

  function handleAuthChange(nextAuthConfig: RuntimeAuthConfigSummary) {
    setDraftParsed((current: unknown) => {
      const currentAuthValue = getConfigValueAtPath(current, ["auth"]);
      const currentAuth = isConfigRecord(currentAuthValue)
        ? currentAuthValue
        : {};
      const currentAccessAuth = isConfigRecord(currentAuth.access_auth)
        ? currentAuth.access_auth
        : {};

      return setConfigValueAtPath(current, ["auth"], {
        ...currentAuth,
        jwt_secret: nextAuthConfig.jwtSecret,
        access_key_secret: nextAuthConfig.accessKeySecret,
        jwt_expire: nextAuthConfig.jwtExpire,
        session_timeout: nextAuthConfig.sessionTimeout,
        max_api_create_times: parseLooseScalar(
          nextAuthConfig.maxApiCreateTimes,
        ),
        admin_auth_enabled: nextAuthConfig.adminAuthEnabled,
        admin_token: nextAuthConfig.adminToken,
        access_auth: {
          ...currentAccessAuth,
          enabled: nextAuthConfig.accessAuthEnabled,
          allow_anonymous: nextAuthConfig.accessAuthAllowAnonymous,
        },
      });
    });
  }


  return {
    handleAuthChange,
  };
}

export type AuthDomain = ReturnType<typeof createAuthDomain>;
