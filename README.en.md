<div align="center">

# Quota Pacer

[中文](./README.md) | [English](./README.en.md)

</div>

Quota Pacer (formerly credential-priority) is a CLIProxyAPI (CPA) plugin that automatically paces credential priority by fresh quota evidence. The plugin ID, dynamic library basename, and CPA configuration key are all `quota-pacer`.

## Navigation

- [Overview](#overview)
- [Workflow](#workflow)
- [PacingScore Algorithm](#pacingscore-algorithm)
- [Build and Installation](#build-and-installation)
- [Plugin Store Source](#plugin-store-source)
- [Configuration](#configuration)
- [Management Page and API](#management-page-and-api)
- [Acknowledgments](#acknowledgments)
- [License](#license)

## Overview

- Reuses CPA credential, proxy, and write-back flows through `host.auth.list`, `host.auth.get`, `host.auth.get_runtime`, and `host.auth.save`.
- Generates priority changes only from fresh and ready evidence collected in the current run.
- Currently supports Antigravity, Codex, and xAI credentials; additional providers may be added later.
- Provider rules are independent: Antigravity, Codex, and xAI do not share depletion behavior.
- Status pages, diagnostics, snapshots, and logs expose only redacted credential information.
- **Automatic priority and rules** are edited via CPA **Plugin Manager visual ConfigFields** (recommended), or host `config.yaml` / `plugins.configs.quota-pacer`.
- **Plugin page** supports Management Key verification, overview (read-only effective config), run history (last 5), help, and manual apply (management routes). **It does not save config on the plugin page.**

## Workflow

```text
Load plugin
  -> Read plugins.configs.quota-pacer config
  -> Fetch CPA credential list through host.auth.list
  -> Filter currently supported providers by provider_scope (all or antigravity|codex|claude|xai)
       - Antigravity: probe remaining quota for the selected model group
       - Codex: probe availability by account plan and quota state
       - Claude: probe availability and remaining quota by session / 5-hour reset window
        - xAI: FetchPlan (settings / billing / JWT) classifies free/paid; auto no longer multi-model chat probes
             Business quota via usage.handle; 2×429 within 30 minutes → free soft-disable; 401 AuthInvalid → hard-disable
             Free does not join priority sorting by default (`free_participates_priority` default false)
  -> Build a sorting plan only from fresh and ready evidence in this run
  -> Decide whether to write back by run mode
       - apply: write priority and enabled state through host.auth.save
       - preview: update status, diagnostics, snapshot, and logs only
  -> Show redacted statistics, audit summary, and sorting result on the management page
```

## PacingScore Algorithm

Sorting no longer relies on fixed thresholds or boost rules. The core is a dimensionless pacing-health score used to compare every account, across every provider, on one global scale:

```
PacingScore = Remaining Quota % ÷ Remaining Time %
```

- **Remaining Quota %**: the `Remaining` value (0-100) probed in this run. A failed probe or zero remaining quota scores 0 directly, so the account automatically sinks to the bottom — no separate "depleted" branch needed.
- **Remaining Time %**: time left until quota reset ÷ the inferred window length, clamped to `[0.001, 1.0]` (a reset time that has already passed but hasn't refreshed yet also hits the floor, amplifying the score). The window length is inferred as follows:
  - If a long-window reset time is probed (e.g. an OAuth weekly window, `LongWindowResetAt`), the window is fixed at 7 days.
  - Otherwise it falls back to the short-window reset time (`ResetAt`) and infers the window from how much time remains: > 48h → 7-day window, 6-48h → 24-hour window, < 6h → 5-hour window (matching the session-level windows common to Claude/Codex).
  - With no reset-time evidence at all, it falls back to sorting by remaining quota % alone.

A higher score means the account's quota consumption is lagging behind time elapsed (it's being used more slowly than expected), so it should get more traffic; a score below 1 means it's burning too fast and should be throttled back. Because it's a ratio of two percentages, accounts can be compared on one global priority queue even when quota semantics differ completely across providers (Antigravity's model-group quota, Codex/Claude's plan windows, xAI's weekly/monthly caps) — no per-provider scoring rule required.

## Build and Installation

The plugin runs as a CGO dynamic library. CPA derives the plugin ID from the dynamic library filename, so the filename must stay `quota-pacer.<ext>`.

```bash
go build -buildmode=c-shared -o quota-pacer.so .
```

Place the artifact in one of the CPA plugin discovery directories:

- `plugins/<GOOS>/<GOARCH>/quota-pacer.<ext>`
- `plugins/<GOOS>/<GOARCH>-<variant>/quota-pacer.<ext>`
- `plugins/quota-pacer.<ext>`

Extensions: `.so` on Linux and FreeBSD, `.dylib` on macOS, and `.dll` on Windows.

## Plugin Store Source

To install this plugin through the CPA plugin store, third-party sources must point to the raw JSON text of `registry.json`:

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/xg1990/quota-pacer/main/registry.json"
```

Do not use `https://github.com/xg1990/quota-pacer/blob/main/registry.json`. That URL returns a GitHub HTML page, which CPA cannot parse as a plugin store registry. After changing `store-sources`, restart CPA or reload configuration through the management UI, then refresh the plugin store list.

## Configuration

Enable the CPA plugin system and keep plugin-owned fields under `plugins.configs.quota-pacer`:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    quota-pacer:
      enabled: true
      priority: 10
      auto_apply: false
      provider_scope: "all"   # or "antigravity|codex|claude|xai"
      antigravity_model_group: "gemini"
      priority_rules:
        enabled: false
        antigravity: {}
        codex:
          free_depleted_priority: -1
          free_depleted_disabled: true
          paid_depleted_disabled: false
        claude:
          free_depleted_priority: -1
          free_depleted_disabled: true
          paid_depleted_disabled: false
        xai:
          free_depleted_priority: -1
          free_depleted_disabled: false
          weekly_depleted_priority: -1
          monthly_and_weekly_depleted_priority: -1
          monthly_and_weekly_depleted_disabled: true
```

| Field | Description |
| :--- | :--- |
| `enabled` | Per-plugin switch. Global `plugins.enabled: true` and successful dynamic library registration are also required. Independent of `priority_rules.enabled`. |
| `priority` | CPA plugin loading and execution order. Higher values run earlier. |
| `auto_apply` | Enables scheduled execution and write-back. Default: `false`. |
| `provider_scope` | `all` handles every currently supported provider; or list one or more providers separated by `\|`, e.g. `antigravity\|codex\|claude\|xai`. Legacy `selected` + `selected_providers` remains supported. |
| `antigravity_model_group` | Antigravity quota group: `gemini` or `claude_gpt`. |
| `priority_rules.enabled` | Enables custom priority rules. When disabled, built-in sorting is used. Independent of top-level `enabled`. |
| `interval` | Auto sort / probe batch step (default 15m). Disabled credentials also batch with this interval (no fixed 1h freeze). |
| `immediate_probe_limit` / `active_group_size` | Immediate probe count and active batch size; disabled batches share `active_group_size` with active. |

> **Configuration notes** (user-facing): Flat `priority_rules.*` keys are supported. Priority is determined entirely by the global, cross-provider pacing algorithm (PacingScore); there is no per-provider start-priority override. `priority_rules.enabled` controls whether depletion-related policy fields take effect. Disabled/depleted accounts no longer wait a fixed 1 hour—pacing follows `interval` and batch settings. Within ~24h of a quota reset, accounts with remaining quota are preferred (Antigravity/Codex/Claude and OAuth paid xAI weekly long-windows included; xAI free excluded).

### Provider-Independent Rules

Antigravity rules

- Only credentials with fresh quota evidence for the selected Antigravity model group are sorted.
- Failed quota fetches and unavailable remaining quota keep the current priority and enabled state.

Codex rules

- `priority_rules.codex.free_depleted_priority`: priority for depleted Free credentials. Default: `-1`.
- `priority_rules.codex.free_depleted_disabled`: disables depleted Free credentials. Default: `true`.
- `priority_rules.codex.paid_depleted_disabled`: disable Plus/Pro/Team when depleted; `true`=disable, `false`=keep enabled. Default: `false`. Legacy `paid_depleted_keeps_enabled` is still accepted (inverted).

Claude rules

- `priority_rules.claude.free_depleted_priority`: priority for depleted Free credentials. Default: `-1`.
- `priority_rules.claude.free_depleted_disabled`: disables depleted Free credentials. Default: `true`.
- `priority_rules.claude.paid_depleted_disabled`: disable Pro/Team when depleted; `true`=disable, `false`=keep enabled. Default: `false`.

xAI rules

**Plan classification (FetchPlan)**

- Classifies the plan as `free` or `paid` via settings / billing / JWT tier — **auto no longer** multi-model chat probes for quota.
- Unfetchable results (network failure, 404, missing plan fields, etc.) default to **`free`**; only explicit paid product/tier signals mark **`paid`**.
- **Free sorting is off by default**: `free_participates_priority` defaults to `false` — Free does not join positive priority promotion / free-first / uniqueness reordering.
- Only with explicit `priority_rules.xai.free_participates_priority: true` do eligible free credentials sort above paid (consume free first).

**Free quota and 24h anchor (independent of the sorting switch)**

- Business quota is accumulated from host `usage.handle`, not from chat probes.
- **2×429 within 30 minutes** → `priority=-1` **soft-disable** (lower priority only); independent of whether Free joins sorting; still applies by default.
- `free_depleted_disabled` defaults to `false` (soft-disable, no hard `disabled`); set `true` only for hard disable.
- Cooldown anchor: `first_success_at + 24h` (or the depleting failure time + 24h if no success yet); after that, recovery can resume.

**401 AuthInvalid (hard disable)**

- Still 401 / credential-invalid text after OAuth force refresh → `priority=-1` and `disabled=true` hard disable.
- Requires user **re-login** to recover; does not count toward free quota failure streak.

**Config fields**

- `priority_rules.xai.free_depleted_priority`: priority when free usage is depleted (soft-disable). Default: `-1`.
- `priority_rules.xai.free_depleted_disabled`: hard-disables free usage depleted credentials. Default: `false` (soft-disable: lower priority only).
- `priority_rules.xai.free_participates_priority`: whether Free joins positive priority sorting / free-first. Default: `false`; set `true` to opt in. When false, 429 depletion, cooldown, and 401 are unchanged.
- `priority_rules.xai.weekly_depleted_priority`: priority when only weekly limit is depleted. Default: `-1` (not disabled).
- `priority_rules.xai.monthly_and_weekly_depleted_priority`: priority when monthly and weekly are depleted. Default: `-1`.
- `priority_rules.xai.monthly_and_weekly_depleted_disabled`: disables when monthly and weekly are depleted. Default: `true`.

## Management Page and API

The plugin registers **resources** (static shell) and **routes** (dynamic APIs) via `management.register`.

### Product boundary

| Capability | Entry | Notes |
| :--- | :--- | :--- |
| Automatic priority and rules | CPA Plugin Manager visual fields (recommended) or `config.yaml` | `auto_apply`, `provider_scope` (all or a\|b\|c), `interval`, `priority_rules.*`, etc. |
| Resource page | `/v0/resource/plugins/quota-pacer/status` | Static HTML: key verify + overview / run history / help + manual sort |
| Manual apply | `/v0/management/plugins/quota-pacer/run` | Requires Management Key |
| Read-only config | Host `GET /v0/management/plugins/quota-pacer/config` | Display only; no plugin-page PATCH |

### Resource page (static)

- `GET /v0/resource/plugins/quota-pacer/status`
  Returns a static HTML shell. The browser uses the Management Key for read-only data, run history, and management-path manual runs. **No in-page config save controls.**

### Management API (dynamic, key required)

- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider_scope=all&antigravity_model_group=gemini`
  Manual probe, plan, and write-back of credential priorities.
- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider=antigravity&antigravity_model_group=claude_gpt`
  Handles only Antigravity credentials with the Claude/GPT model group.
- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider=codex`
  Handles only Codex credentials.
- `GET /v0/management/plugins/quota-pacer/diagnostics`
  Exports redacted diagnostics.
- `GET /v0/management/plugins/quota-pacer/snapshot/latest`
  Returns the latest redacted decision snapshot.

## Acknowledgments

- This project was originally forked from [Cody292/credential-priority](https://github.com/Cody292/credential-priority) — thanks to the original author for the plugin scaffold and provider-probing logic.
- Thanks to [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) for the host plugin platform (`host.auth.*` callbacks, Management Key verification, hot-reload, and more), which let this plugin focus purely on the pacing algorithm.

## License

This project is licensed under the MIT License. See [LICENSE](./LICENSE).
