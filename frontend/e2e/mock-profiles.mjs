// e2e mock：profiles 域（Batch 13 slice 9 / E2E-1/3/4/5 前端半程）。
//
// 契约来源：frontend/src/api/runtime/profiles/*（URL 见 url.ts、归一化纪律见
// normalize.ts / session-switch.ts 顶部注释）。三条纪律：
//
// 1. **形状对齐真后端**：前端归一化器把 `ref`（列表/视图）与 `switch_report`
//    （会话切换）视为必填句柄——mock 少给一个字段，E2E 就会把契约回归伪装成绿。
//    这里按 Go handler 的字段名（snake_case）返回；少数同时存在 camel 别名的展示
//    字段显式双写，避免列表徽标因字段名分歧而静默为空。
// 2. **状态按用例隔离**：初始状态一律由 `POST /api/_test/profiles` 注入，
//    `POST /api/_test/reset` 清空（与 mock-server 的会话/事件夹具同口径）。
// 3. **域内自洽**：create / rename / move / delete / set_default 之后，列表、视图与
//    引用检查必须同步变化——写死单条数据的 mock 只能证明「首屏能渲染」。
//
// 本模块只依赖注入的 readBody / writeJson 两个 helper，不读 mock-server 内部状态。

const PROFILES_PREFIX = "/api/runtime/profiles";
const IMPORT_PATH = `${PROFILES_PREFIX}/import`;
const SESSIONS_PREFIX = "/api/runtime/sessions/";

/** 最小合法 zip（22 字节 EOCD）：导出体要能被浏览器下载，导入体要能被 setInputFiles 选中。 */
const EMPTY_ZIP = Buffer.concat([Buffer.from([0x50, 0x4b, 0x05, 0x06]), Buffer.alloc(18)]);

const DEFAULT_MTIME = "2026-09-24T10:00:00Z";

/** 模板 → profile.yaml 草稿（字段名对齐 §10.5 的 spec 形态）。 */
function templateSpec(template, name, description) {
  const base = {
    name,
    ...(description ? { description } : {}),
  };
  if (template === "review") {
    return {
      ...base,
      tools: { allow: ["read_file"], deny: ["write_file", "shell"] },
      skills: { allow: ["code-review"] },
      mcp: { use: ["github"] },
      prompts: { system: "prompts/system.md", mode: "append" },
      agents: { default: "reviewer", enabled: ["reviewer"] },
      preferences: { permission_mode: "read_only" },
    };
  }
  if (template === "coding") {
    return {
      ...base,
      tools: { allow: ["read_file", "write_file", "shell"] },
      agents: { default: "coder" },
    };
  }
  return base;
}

/** 引用检查报告（`normalizeReferences` 的必填面）。 */
function referencesReport(entry, defaultProfile, sessionProfiles) {
  const blocking = [];
  if (defaultProfile === entry.name || defaultProfile === entry.ref) {
    blocking.push({
      kind: "config",
      label: "default_profile",
      detail: `config.yaml default_profile points at ${entry.ref}`,
    });
  }
  for (const [sessionId, ref] of sessionProfiles) {
    if (ref === entry.ref || ref === entry.name) {
      blocking.push({
        kind: "session",
        label: sessionId,
        detail: `session ${sessionId} binds ${entry.ref}`,
      });
    }
  }
  return {
    ref: entry.ref,
    root: entry.root,
    default_profile: defaultProfile,
    is_default: defaultProfile === entry.name || defaultProfile === entry.ref,
    blocking,
    warnings: [],
    config_items: [],
    session_scan: "ok",
    agent_references: [],
    agent_reference_note: "",
    files: [],
    file_count: 0,
  };
}

export function createProfilesMock() {
  /** ref -> entry{ref,name,layer,root,path,description,spec,valid,writable,mtime} */
  const entries = new Map();
  /** sessionId -> ref（会话身份；切换报告 `from` 的事实来源） */
  const sessionProfiles = new Map();
  let defaultProfile = "";

  function reset() {
    entries.clear();
    sessionProfiles.clear();
    defaultProfile = "";
  }

  function refFor(layer, name) {
    return `${layer}:${name}`;
  }

  function rootFor(layer, name) {
    return layer === "project"
      ? `.aicli/profiles/${name}`
      : `.aicli/user-profiles/${name}`;
  }

  function putEntry({ name, layer, description, spec, valid, writable, ref }) {
    const resolvedRef = ref ?? refFor(layer, name);
    const root = rootFor(layer, name);
    const entry = {
      ref: resolvedRef,
      name,
      layer,
      root,
      path: `${root}/profile.yaml`,
      description: description ?? "",
      spec: spec ?? templateSpec("minimal", name, description),
      valid: valid !== false,
      writable: writable !== false,
      mtime: DEFAULT_MTIME,
    };
    entries.set(resolvedRef, entry);
    return entry;
  }

  /** 列表条目：`is_default` 双写（徽标读 entry.isDefault），计数双写（行上标签读 snake）。 */
  function toListEntry(entry) {
    const isDefault = defaultProfile === entry.name || defaultProfile === entry.ref;
    const tools = Array.isArray(entry.spec?.tools?.allow) ? entry.spec.tools.allow : [];
    const referenceCount = referencesReport(entry, defaultProfile, sessionProfiles).blocking.length;
    return {
      ref: entry.ref,
      name: entry.name,
      layer: entry.layer,
      root: entry.root,
      path: entry.path,
      description: entry.description,
      valid: entry.valid,
      writable: entry.writable,
      is_default: isDefault,
      isDefault,
      tool_count: tools.length,
      toolCount: tools.length,
      reference_count: referenceCount,
      referenceCount,
      total_tokens: 120 + tools.length * 30,
      totalTokens: 120 + tools.length * 30,
    };
  }

  function estimateFor(spec) {
    const tools = Array.isArray(spec?.tools?.allow) ? spec.tools.allow : [];
    const promptTokens = spec?.prompts ? 80 : 0;
    const toolTokens = tools.length * 30;
    return {
      tool_tokens: toolTokens,
      toolTokens,
      prompt_tokens: promptTokens,
      promptTokens,
      total_tokens: toolTokens + promptTokens,
      totalTokens: toolTokens + promptTokens,
      tool_count: tools.length,
      toolCount: tools.length,
    };
  }

  /** 解析后视图：`ref` 必填（normalizeProfileView 的第一句就是它）。 */
  function toView(entry, options = {}) {
    const spec = options.spec ?? entry.spec;
    const allow = Array.isArray(spec?.tools?.allow) ? spec.tools.allow : [];
    const deny = Array.isArray(spec?.tools?.deny) ? spec.tools.deny : [];
    const changedPaths = options.changedPaths ?? [];
    return {
      ref: entry.ref,
      name: entry.name,
      layer: entry.layer,
      root: entry.root,
      path: entry.path,
      description: entry.description,
      valid: entry.valid,
      writable: entry.writable,
      mtime: entry.mtime,
      preview: options.preview === true,
      spec,
      tools: {
        allow,
        deny,
        effective: allow.filter((tool) => !deny.includes(tool)),
        excluded: deny,
        origins: {},
      },
      skills: {
        dirs: Array.isArray(spec?.skills?.dirs) ? spec.skills.dirs : [],
        allow: Array.isArray(spec?.skills?.allow) ? spec.skills.allow : [],
        deny: Array.isArray(spec?.skills?.deny) ? spec.skills.deny : [],
        exposure_mode: spec?.skills?.exposure_mode ?? "",
        top_k: spec?.skills?.top_k ?? "",
      },
      mcp: {
        use: Array.isArray(spec?.mcp?.use) ? spec.mcp.use : [],
        exclude: Array.isArray(spec?.mcp?.exclude) ? spec.mcp.exclude : [],
        connected: [],
        tool_count: 0,
      },
      prompts: {
        system: spec?.prompts?.system ?? "",
        role: spec?.prompts?.role ?? "",
        tools: spec?.prompts?.tools ?? "",
        mode: spec?.prompts?.mode ?? "",
        preview: "",
      },
      agents: {
        default: spec?.agents?.default ?? "",
        enabled: Array.isArray(spec?.agents?.enabled) ? spec.agents.enabled : [],
        disabled: Array.isArray(spec?.agents?.disabled) ? spec.agents.disabled : [],
        available: [],
      },
      overrides: { entries: [], allowed_keys: [], allowedKeys: [] },
      preferences: {
        permission_mode: spec?.preferences?.permission_mode ?? "",
        reasoning_effort: spec?.preferences?.reasoning_effort ?? "",
        provider: "e2e-mock-provider",
        model: "e2e-mock-model",
      },
      estimate: estimateFor(spec),
      changed_paths: changedPaths,
      changedPaths,
      diff: options.diff ?? "",
    };
  }

  function viewNotFound(res, ref) {
    writeJson(res, 404, { error: `profile not found: ${ref}`, code: "not_found" });
  }

  /** 草稿校验：只做「同一个 validate」里最容易被 mock 忽略的两条硬规则。 */
  function validateSpec(spec, name) {
    const issues = [];
    const allow = Array.isArray(spec?.tools?.allow) ? spec.tools.allow : [];
    const deny = Array.isArray(spec?.tools?.deny) ? spec.tools.deny : [];
    const conflicts = allow.filter((tool) => deny.includes(tool));
    if (conflicts.length > 0) {
      issues.push({
        severity: "error",
        path: "tools.deny",
        message: `tool appears in allow and deny: ${conflicts.join(", ")}`,
      });
    }
    if (typeof name !== "string" || name.length === 0) {
      issues.push({ severity: "error", path: "name", message: "name is required" });
    }
    return issues;
  }

  /** 差分：只报「草稿相对当前落盘 spec 变了哪几个顶层分组」，够 UI 显示 changed paths。 */
  function diffPaths(before, after) {
    const paths = [];
    for (const group of ["tools", "skills", "mcp", "prompts", "agents", "preferences"]) {
      const left = JSON.stringify(before?.[group] ?? null);
      const right = JSON.stringify(after?.[group] ?? null);
      if (left !== right) {
        paths.push(group);
      }
    }
    return paths;
  }

  async function handle(req, res, { path, url, readBody, writeJson }) {
    // ---- 测试注入：POST /api/_test/profiles ----
    if (path === "/api/_test/profiles" && req.method === "POST") {
      const body = await readBody(req);
      const seeded = Array.isArray(body?.profiles) ? body.profiles : [];
      for (const item of seeded) {
        const layer = typeof item.layer === "string" && item.layer ? item.layer : "project";
        const name = typeof item.name === "string" ? item.name : "profile";
        putEntry({
          name,
          layer,
          description: item.description,
          spec: item.spec,
          valid: item.valid,
          writable: item.writable,
          ref: item.ref,
        });
      }
      if (typeof body?.default_profile === "string") {
        defaultProfile = body.default_profile;
      }
      if (body?.session_profiles && typeof body.session_profiles === "object") {
        for (const [sessionId, ref] of Object.entries(body.session_profiles)) {
          if (typeof ref !== "string") {
            continue;
          }
          // 会话身份统一落成 ref：切换报告的 `from` 与引用检查都按 ref 比对。
          const bound = [...entries.values()].find(
            (item) => item.name === ref || item.ref === ref,
          );
          sessionProfiles.set(sessionId, bound?.ref ?? ref);
        }
      }
      writeJson(res, 200, { ok: true, count: entries.size, default_profile: defaultProfile });
      return true;
    }

    // ---- 列表：GET /api/runtime/profiles ----
    if (path === PROFILES_PREFIX && req.method === "GET") {
      const profiles = [...entries.values()].map(toListEntry);
      const defaultEntry = [...entries.values()].find(
        (entry) => entry.name === defaultProfile || entry.ref === defaultProfile,
      );
      writeJson(res, 200, {
        count: profiles.length,
        default_profile: defaultProfile,
        default_root: defaultEntry?.root ?? "",
        profiles,
        session_switch: true,
      });
      return true;
    }

    // ---- 创建：POST /api/runtime/profiles（模板/复制/固化三模式共用入口）----
    if (path === PROFILES_PREFIX && req.method === "POST") {
      const body = await readBody(req);
      const name = typeof body?.name === "string" ? body.name.trim() : "";
      if (!name) {
        writeJson(res, 400, { error: "name is required", code: "invalid_request" });
        return true;
      }
      const layer = typeof body?.layer === "string" && body.layer ? body.layer : "user";
      const ref = refFor(layer, name);
      if (entries.has(ref)) {
        writeJson(res, 409, { error: `profile already exists: ${ref}`, code: "already_exists" });
        return true;
      }
      const source =
        typeof body?.from_ref === "string" && body.from_ref ? entries.get(body.from_ref) : null;
      const spec = source
        ? { ...source.spec, name }
        : templateSpec(typeof body?.template === "string" ? body.template : "minimal", name, body?.description);
      const entry = putEntry({ name, layer, spec, description: body?.description });
      const setDefault = body?.set_default === true;
      if (setDefault) {
        defaultProfile = name;
      }
      writeJson(res, 200, {
        created: true,
        name,
        root: entry.root,
        layer,
        files: ["profile.yaml"],
        profile: { ref: entry.ref, name },
        config_path: ".aicli/config.yaml",
        registered: true,
        default_profile_set: setDefault,
        affects: "new sessions",
      });
      return true;
    }

    // ---- 导入：POST /api/runtime/profiles/import?name=&layer=&dry_run= ----
    if (path === IMPORT_PATH && req.method === "POST") {
      // 请求体是 zip 本身（非 JSON）：必须消费掉，否则连接会挂在半读状态。
      req.resume();
      const dryRun = url.searchParams.get("dry_run") === "true";
      const layer = url.searchParams.get("layer") || "user";
      const name = url.searchParams.get("name") || "imported-profile";
      const ref = refFor(layer, name);
      if (!dryRun && entries.has(ref)) {
        writeJson(res, 409, { error: `profile already exists: ${ref}`, code: "already_exists" });
        return true;
      }
      const entry = dryRun
        ? { ref, name, layer, root: rootFor(layer, name), path: `${rootFor(layer, name)}/profile.yaml` }
        : putEntry({ name, layer, spec: templateSpec("review", name, "imported bundle") });
      writeJson(res, 200, {
        ok: true,
        valid: true,
        imported: !dryRun,
        // 导入绝不自动激活（D28）：激活是 default / apply 两个独立动作。
        activated: false,
        dry_run: dryRun,
        layer,
        name,
        root: entry.root,
        paths: ["profile.yaml", "prompts/system.md"],
        file_count: 2,
        fileCount: 2,
        error: "",
        error_count: 0,
        errorCount: 0,
        warning_count: 0,
        warningCount: 0,
        issues: [],
        hint: dryRun ? "dry run only; nothing was written" : "imported but not activated",
      });
      return true;
    }

    // ---- 会话内切换：POST /api/runtime/sessions/{id}/runtime/commands ----
    if (
      path.startsWith(SESSIONS_PREFIX) &&
      path.endsWith("/runtime/commands") &&
      req.method === "POST"
    ) {
      const sessionId = decodeURIComponent(
        path.slice(SESSIONS_PREFIX.length, path.length - "/runtime/commands".length),
      );
      const body = await readBody(req);
      if (body?.type !== "set_profile") {
        writeJson(res, 400, {
          error: `unsupported mock command: ${String(body?.type)}`,
          code: "unsupported_command",
        });
        return true;
      }
      const target = typeof body?.profile === "string" ? body.profile.trim() : "";
      const entry = [...entries.values()].find(
        (item) => item.name === target || item.ref === target,
      );
      if (!target || !entry) {
        writeJson(res, 400, { error: `unknown profile: ${target}`, code: "unknown_profile" });
        return true;
      }
      const from = sessionProfiles.get(sessionId) ?? "";
      sessionProfiles.set(sessionId, entry.ref);
      const allow = Array.isArray(entry.spec?.tools?.allow) ? entry.spec.tools.allow : [];
      const deny = Array.isArray(entry.spec?.tools?.deny) ? entry.spec.tools.deny : [];
      writeJson(res, 200, {
        ok: true,
        switch_report: {
          from,
          to: entry.ref,
          changed: {
            tools_added: allow,
            tools_removed: [],
            skills_added: Array.isArray(entry.spec?.skills?.allow) ? entry.spec.skills.allow : [],
            skills_removed: [],
            mcp_added: Array.isArray(entry.spec?.mcp?.use) ? entry.spec.mcp.use : [],
            mcp_removed: [],
            prompt_changed: Boolean(entry.spec?.prompts),
            provider_changed: false,
            model_changed: false,
            permission_mode_changed: Boolean(entry.spec?.preferences?.permission_mode),
          },
          effective_at: "next_turn",
          cache_notice: "prompt cache prefix invalidated",
          warnings: [],
          in_flight_turn: false,
          anchor_cleared: true,
          tool_surface_invalidated: true,
          tool_scope: "actor",
          actor_evicted: true,
          context_token_count_reset: false,
        },
      });
      return true;
    }

    if (!path.startsWith(`${PROFILES_PREFIX}/`)) {
      return false;
    }

    // ---- 单资源：/api/runtime/profiles/{ref}[/{action}] ----
    const rest = path.slice(PROFILES_PREFIX.length + 1);
    const [encodedRef, action] = rest.split("/");
    const ref = decodeURIComponent(encodedRef ?? "");
    const entry = entries.get(ref);
    if (!entry && action !== "references") {
      viewNotFound(res, ref);
      return true;
    }

    if (!action && req.method === "GET") {
      writeJson(res, 200, toView(entry));
      return true;
    }

    if (!action && req.method === "PUT") {
      const body = await readBody(req);
      const expected = body?.expected_mtime ?? body?.expectedMtime;
      if (typeof expected === "string" && expected && expected !== entry.mtime) {
        writeJson(res, 409, { error: "profile was modified by another process", code: "conflict" });
        return true;
      }
      entry.spec = body?.spec ?? entry.spec;
      entry.name = entry.spec?.name ?? entry.name;
      entry.mtime = "2026-09-24T10:05:00Z";
      const issues = validateSpec(entry.spec, entry.name);
      entry.valid = issues.length === 0;
      writeJson(res, 200, {
        profile: toView(entry),
        mtime: entry.mtime,
        warning_count: 0,
        warningCount: 0,
        issues,
      });
      return true;
    }

    if (!action && req.method === "DELETE") {
      const force = url.searchParams.get("force") === "true";
      const references = referencesReport(entry, defaultProfile, sessionProfiles);
      const isDefault = references.is_default;
      if (!force && (isDefault || references.blocking.length > 0)) {
        // 409 + references：前端据此把「强制删除」复选框与阻断数呈现出来（G2）。
        writeJson(res, 409, {
          deleted: false,
          error: `profile is still referenced: ${entry.ref}`,
          references,
        });
        return true;
      }
      entries.delete(entry.ref);
      if (isDefault) {
        defaultProfile = "";
      }
      for (const [sessionId, bound] of [...sessionProfiles]) {
        if (bound === entry.ref) {
          sessionProfiles.delete(sessionId);
        }
      }
      writeJson(res, 200, {
        deleted: true,
        ref: entry.ref,
        root: entry.root,
        removed_files: ["profile.yaml"],
        removed_file_count: 1,
        config_items_removed: [],
        default_cleared: isDefault,
        references: referencesReport(entry, defaultProfile, sessionProfiles),
        recoverable: false,
      });
      return true;
    }

    if (action === "references" && req.method === "GET") {
      if (!entry) {
        writeJson(res, 200, {
          ref,
          root: "",
          default_profile: defaultProfile,
          is_default: false,
          blocking: [],
          warnings: [],
          config_items: [],
          session_scan: "missing",
          agent_references: [],
          agent_reference_note: "",
          files: [],
          file_count: 0,
        });
        return true;
      }
      writeJson(res, 200, referencesReport(entry, defaultProfile, sessionProfiles));
      return true;
    }

    if (action === "validate" && req.method === "POST") {
      const body = await readBody(req);
      const spec = body?.spec ?? entry.spec;
      const issues = validateSpec(spec, entry.name);
      writeJson(res, 200, {
        ref: entry.ref,
        name: entry.name,
        path: entry.path,
        root: entry.root,
        agent: typeof body?.agent === "string" ? body.agent : "",
        valid: issues.length === 0,
        error_count: issues.filter((issue) => issue.severity === "error").length,
        warning_count: 0,
        issues,
      });
      return true;
    }

    if (action === "preview" && req.method === "POST") {
      const body = await readBody(req);
      const spec = body?.spec ?? entry.spec;
      const changedPaths = diffPaths(entry.spec, spec);
      writeJson(
        res,
        200,
        toView(entry, {
          spec,
          preview: true,
          changedPaths,
          diff: changedPaths.map((group) => `~ ${group}`).join("\n"),
        }),
      );
      return true;
    }

    if (action === "default" && req.method === "POST") {
      const previous = defaultProfile;
      defaultProfile = entry.name;
      writeJson(res, 200, {
        default_profile: entry.name,
        defaultProfile: entry.name,
        previous_default: previous,
        previousDefault: previous,
        config_path: ".aicli/config.yaml",
        registered: true,
        affects: "new sessions",
        current_session_note: "the current session is unchanged; switch it with /profile",
      });
      return true;
    }

    if (action === "apply" && req.method === "POST") {
      const body = await readBody(req);
      const sessionId = typeof body?.session_id === "string" ? body.session_id : "";
      if (!sessionId) {
        writeJson(res, 400, {
          error: "session_id is required",
          code: "session_required",
          hint: "apply targets exactly one session; switch inside a session with /profile",
        });
        return true;
      }
      sessionProfiles.set(sessionId, entry.ref);
      writeJson(res, 200, {
        ok: true,
        changed: { tools_added: entry.spec?.tools?.allow ?? [], tools_removed: [] },
        warnings: [],
      });
      return true;
    }

    if (action === "rename" && req.method === "POST") {
      const body = await readBody(req);
      const name = typeof body?.name === "string" ? body.name.trim() : "";
      if (!name) {
        writeJson(res, 400, { error: "name is required", code: "invalid_request" });
        return true;
      }
      const nextRef = refFor(entry.layer, name);
      if (entries.has(nextRef)) {
        writeJson(res, 409, { error: `profile already exists: ${nextRef}`, code: "already_exists" });
        return true;
      }
      entries.delete(entry.ref);
      const wasDefault = defaultProfile === entry.name || defaultProfile === entry.ref;
      entry.name = name;
      entry.ref = nextRef;
      entry.root = rootFor(entry.layer, name);
      entry.path = `${entry.root}/profile.yaml`;
      entry.spec = { ...entry.spec, name };
      entries.set(nextRef, entry);
      if (wasDefault) {
        defaultProfile = name;
      }
      writeJson(res, 200, {
        renamed: true,
        old_ref: ref,
        oldRef: ref,
        new_ref: nextRef,
        newRef: nextRef,
        root: entry.root,
        layer: entry.layer,
        config_updated: wasDefault,
        configUpdated: wasDefault,
        session_note: "existing session bindings keep the old ref until they are switched",
        sessionNote: "existing session bindings keep the old ref until they are switched",
        references: referencesReport(entry, defaultProfile, sessionProfiles),
      });
      return true;
    }

    if (action === "move" && req.method === "POST") {
      const body = await readBody(req);
      const layer = typeof body?.layer === "string" && body.layer ? body.layer : entry.layer;
      const previousRoot = entry.root;
      const nextRef = refFor(layer, entry.name);
      entries.delete(entry.ref);
      entry.layer = layer;
      entry.ref = nextRef;
      entry.root = rootFor(layer, entry.name);
      entry.path = `${entry.root}/profile.yaml`;
      entries.set(nextRef, entry);
      writeJson(res, 200, {
        moved: true,
        ref: nextRef,
        from: previousRoot,
        to: entry.root,
        config_updated: false,
        configUpdated: false,
        references: referencesReport(entry, defaultProfile, sessionProfiles),
      });
      return true;
    }

    if (action === "duplicate" && req.method === "POST") {
      const body = await readBody(req);
      const name = typeof body?.name === "string" ? body.name.trim() : "";
      if (!name) {
        writeJson(res, 400, { error: "name is required", code: "invalid_request" });
        return true;
      }
      const layer = typeof body?.layer === "string" && body.layer ? body.layer : entry.layer;
      const nextRef = refFor(layer, name);
      if (entries.has(nextRef)) {
        writeJson(res, 409, { error: `profile already exists: ${nextRef}`, code: "already_exists" });
        return true;
      }
      const created = putEntry({ name, layer, spec: { ...entry.spec, name } });
      writeJson(res, 200, {
        created: true,
        name,
        root: created.root,
        layer,
        files: ["profile.yaml"],
        profile: { ref: created.ref, name },
        config_path: ".aicli/config.yaml",
        registered: true,
        default_profile_set: false,
        affects: "new sessions",
      });
      return true;
    }

    if (action === "export" && req.method === "POST") {
      if (!entry.writable) {
        writeJson(res, 400, { error: `profile is read-only: ${entry.ref}`, code: "read_only" });
        return true;
      }
      res.writeHead(200, {
        "Content-Type": "application/zip",
        "Content-Disposition": `attachment; filename="${entry.name}.zip"`,
        "X-Aicli-Profile-Ref": entry.ref,
        "X-Aicli-Profile-File-Count": "2",
        "Content-Length": String(EMPTY_ZIP.length),
      });
      res.end(EMPTY_ZIP);
      return true;
    }

    writeJson(res, 404, { error: `unsupported profiles route: ${path}`, code: "not_found" });
    return true;
  }

  return { handle, reset };
}
