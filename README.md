<div align="center">

# Quota Pacer

[English](./README.md) | [中文](./README.zh.md)

</div>

Quota Pacer (formerly credential-priority) is a CLIProxyAPI (CPA) plugin that automatically paces and balances credential priority across all AI providers based on fresh quota evidence and consumption rate (PacingScore). The plugin ID, dynamic library basename, and CPA configuration key are all `quota-pacer`.

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
- Generates priority changes only from fresh and ready evidence collected in the current probe run.
- Currently supports Antigravity, Codex, Claude, and xAI credentials on a unified global priority scale.
- **Pure PacingScore sorting**: no complex provider-specific rules or manual depletion branches—accounts with positive quota are dynamically ordered by PacingScore; depleted accounts (`Remaining <= 0`) naturally receive a score of 0 and Priority `0`; invalid OAuth credentials (401) are disabled.
- Status pages, diagnostics, snapshots, and logs expose only redacted credential information.
- **Configuration** is managed via CPA **Plugin Manager visual ConfigFields** (recommended), or host `config.yaml` / `plugins.configs.quota-pacer`.
- **Plugin management page** supports Management Key verification, overview (read-only effective config), run history (last 5), help, and manual sorting triggers.

## Workflow

```text
Load plugin
  -> Read plugins.configs.quota-pacer config
  -> Fetch CPA credential list through host.auth.list
  -> Filter supported providers by provider_scope (all or antigravity|codex|claude|xai)
       - Antigravity: probe remaining quota for the selected model group
       - Codex: probe availability and remaining quota
       - Claude: probe availability and remaining quota by session / 5-hour reset window
       - xAI: probe quota and reset window via business usage and OAuth status
  -> Calculate PacingScore for each credential based on probed quota and remaining time
  -> Build a sorting plan only from fresh and ready evidence in this run:
       - Positive remaining quota: sorted descending by PacingScore from MaxPriority (e.g. 100) down
       - Depleted quota (Remaining <= 0): Priority = 0, Reason = "fresh remaining depleted"
       - Auth invalid (401): Priority = -1, Disabled = true, Reason = "xai auth invalid"
  -> Decide whether to write back by run mode:
       - apply: write priority and enabled state through host.auth.save
       - preview / dry_run: update status, diagnostics, snapshot, and logs only
  -> Show redacted statistics, audit summary, and sorting result on the management page
```

## PacingScore Algorithm

Sorting does not rely on fixed thresholds or provider-specific heuristics. All credentials compete on one unified, dimensionless pacing health score:

```
PacingScore = Remaining Quota % ÷ Remaining Time %
```

- **Remaining Quota %**: the `Remaining` value (0-100) probed in this run. A failed probe or zero remaining quota yields a score of `0`, sinking the account to the bottom (`Priority = 0`).
- **Remaining Time %**: time left until quota reset ÷ the inferred window length, clamped to `[0.001, 1.0]`. The window length is inferred as follows:
  - If a long-window reset time is probed (e.g., OAuth weekly window `LongWindowResetAt`), the window is fixed at 7 days (168h).
  - Otherwise it falls back to the short-window reset time (`ResetAt`) and infers the window from the remaining duration: `> 48h` → 7-day window, `6-48h` → 24-hour window, `< 6h` → 5-hour window (matching Claude / Codex session windows).
  - With no reset time available, it falls back to comparing remaining quota % directly.
- **Full-quota override**: if `Remaining >= 100` in the current run, the account scores at the maximum ceiling (`Remaining / 0.001`), activating freshly reset cycles immediately.

A higher score indicates that an account's quota consumption is lagging behind time elapsed (used slower than expected), granting it higher priority to consume before the window resets.

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
      antigravity_model_group: "gemini" # or "claude_gpt"
      interval: "15m"
      immediate_probe_limit: 30
      max_concurrency: 6
      active_group_size: 10
```

| Field | Description | Default |
| :--- | :--- | :--- |
| `enabled` | Plugin switch. Requires global `plugins.enabled: true`. | `true` |
| `priority` | CPA plugin loading and execution order. Higher values run earlier. | `10` |
| `auto_apply` | Enables scheduled automatic priority sorting and write-back. | `false` |
| `provider_scope` | Providers to sort: `all`, or pipe-separated values like `antigravity\|codex\|claude\|xai`. | `all` |
| `antigravity_model_group` | Antigravity quota model group: `gemini` or `claude_gpt`. | `gemini` |
| `interval` | Auto sort interval (e.g. `15m`). | `15m` |
| `immediate_probe_limit` | Maximum credentials probed immediately per run. | `30` |
| `max_concurrency` | Maximum concurrent probing requests. | `6` |
| `active_group_size` | Batch size when probing credentials in batches. | `10` |

## Management Page and API

The plugin registers **resources** (static web UI) and **routes** (dynamic APIs) via `management.register`.

### Product Boundary

| Capability | Entry | Notes |
| :--- | :--- | :--- |
| Automatic priority config | CPA Plugin Manager visual fields (recommended) or `config.yaml` | `auto_apply`, `provider_scope`, `interval`, etc. |
| Resource page | `/v0/resource/plugins/quota-pacer/status` | Static HTML: key verify + overview / run history / help + manual sort |
| Manual apply | `/v0/management/plugins/quota-pacer/run` | Requires Management Key |
| Read-only config | Host `GET /v0/management/plugins/quota-pacer/config` | Display only; no plugin-page PATCH |

### Resource Page (Static)

- `GET /v0/resource/plugins/quota-pacer/status`
  Returns a static HTML shell. The browser uses the Management Key for read-only data, run history, and management-path manual runs.

### Management API (Dynamic, Key Required)

- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider_scope=all&antigravity_model_group=gemini`
  Manual probe, plan, and write-back of credential priorities.
- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider=antigravity&antigravity_model_group=claude_gpt`
  Handles only Antigravity credentials with the Claude/GPT model group.
- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider=codex`
  Handles only Codex credentials.
- `GET /v0/management/plugins/quota-pacer/diagnostics`
  Exports redacted diagnostics and recent run history.
- `GET /v0/management/plugins/quota-pacer/snapshot/latest`
  Returns the latest redacted decision snapshot with PacingScore details.

## Acknowledgments

- This project was originally forked from [Cody292/credential-priority](https://github.com/Cody292/credential-priority) — thanks to the original author for the plugin scaffold and provider-probing logic.
- Thanks to [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) for the host plugin platform (`host.auth.*` callbacks, Management Key verification, hot-reload, and more), which let this plugin focus purely on the pacing algorithm.

## License

This project is licensed under the MIT License. See [LICENSE](./LICENSE).
