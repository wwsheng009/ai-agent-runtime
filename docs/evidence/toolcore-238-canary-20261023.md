# Toolcore 238 canary evidence (2026-10-23)

- Target: `238` (`172.18.3.238:18765/mcp`)
- Workspace: `/home/n9e/itsm/repo`
- Source commit: `d1fc884a` (`docs(tool-layer): record p1 regression and flake dispositions`)
- Canary client: `scripts/canary-remote-tools.ps1`
- Remote binary: Linux amd64, built from `backend/cmd/aicli-remote-tools`
- Binary SHA-256: `1a7eabe2ef6e6abf35b0082b07254fa978d147c1355dfab9b81fbbcb65a5fa6a`
- Previous binary SHA-256: `48ab4f08af81564653dbe8df0d32f397c4327bc59e668ae40d69008b525e3cec`

## 1. Why this canary was needed

The first 238 probe showed an older remote-tools schema: `artifact_read` only
accepted `artifact_id` and rejected `artifact_ref` with JSON-RPC `-32602`.
The previous 238 binary was therefore not a valid canary for the current
Phase 6b implementation.

The current Linux binary was built and installed with a preserved backup. The
old binary was kept at:

```text
/home/n9e/bin/aicli-remote-tools.bak-20261023-082045
```

## 2. Six-tool smoke

Command:

```powershell
pwsh -File .\scripts\canary-remote-tools.ps1 `
  -EnvFile 'E:\projects\itsm\itsm\.env' -Scenario smoke
```

Result: PASS.

| Tool | Result |
|------|--------|
| `remote_238__view` | PASS, 1422 bytes |
| `remote_238__ls` | PASS, 8079 bytes |
| `remote_238__glob` | PASS, 16384 bytes |
| `remote_238__grep` | PASS, 32766 bytes |
| `remote_238__shell` (`git status`) | PASS, 3087 bytes |
| `remote_238__shell` (`git log`) | PASS, folded to 32497 bytes; artifact `art_2d53cc3ae371a8606a154b282b7612d4` |
| `remote_238__artifact_read` | PASS, 4096 bytes from the folded artifact |
| cross-target `artifact_ref` | PASS: `ARTIFACT_SCOPE_DENIED` |

The cross-target probe used `remote:999:art_...`; the target rejected the
namespace with `ARTIFACT_SCOPE_DENIED`, as required by the target-internal
artifact contract.

## 3. 100-call canary window

Command:

```powershell
pwsh -File .\scripts\canary-remote-tools.ps1 `
  -EnvFile 'E:\projects\itsm\itsm\.env' -Scenario soak -Count 100
```

Result: PASS.

```json
{
  "count": 100,
  "ok": 100,
  "failed": 0,
  "error_rate": 0.0,
  "p50_ms": 228,
  "p95_ms": 690
}
```

The 100 calls were evenly distributed across view/ls/glob/grep/shell/
artifact_read. No unexpected 429 or artifact scope denial occurred; the only
scope denial was the explicit cross-target probe in the smoke run.

## 4. Rollback drill

The old binary was restored, the service was started, and read-only shell
health was verified. Then the current binary was installed again and the full
smoke/soak checks were rerun.

| Step | Result | Elapsed |
|------|--------|---------|
| rollback to previous binary | SHA-256 `48ab4f...`; process listening on 18765 | 5.6s |
| health after rollback | `itsm-deploy-238 status`: front HTTP 200, three containers healthy | n/a |
| roll forward to current binary | SHA-256 `1a7e...`; process listening on 18765 | 5.5s |
| post-roll-forward smoke + 100 calls | PASS, 0 failures | 41.0s |

Both directions are well below the 10-minute rollback target.

## 5. Operational caveat

The 238 `aicli-remote-238.service` unit is systemd-managed, but the canary
account `n9e` cannot run `systemctl restart` without a sudo password; the
NOPASSWD rule only covers `/usr/local/bin/itsm-deploy-238`. The canary therefore
ran the installed binary through the documented `start-remote-238.sh` fallback
(no competing listener was present). The root-side install script remains the
production path and must own the final restart. This does not affect the tool
contract results, but release operations must start the service through the
root installer or an equivalent privileged deployment step.

## 6. No-go checks

- no cross-target artifact read;
- no malicious metadata escape in the canary path;
- no 429 storms;
- no data corruption observed;
- rollback and roll-forward both completed;
- binary changed by SHA-256 in both directions, so the drill was real rather
  than a no-op restart.

The independent security review and the remaining full-repo test item are
tracked separately in `tool-layer-abstraction-design-20261022-review.md`.
