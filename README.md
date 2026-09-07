<div align="center">

# Quota Pacer

[English](./README.md) | [中文](./README.zh.md)

</div>

Quota Pacer (formerly credential-priority) is a CLIProxyAPI (CPA) plugin that automatically paces and balances credential traffic across all AI providers from fresh quota evidence and remaining pace headroom (`remaining_headroom`). The plugin ID, dynamic library basename, and CPA configuration key are all `quota-pacer`.

## Navigation

- [Overview](#overview)
- [Workflow](#workflow)
- [Remaining Headroom](#remaining-headroom)
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
- **Headroom-based pacing**: `remaining_headroom` directly drives each credential’s scheduling weight, and values above `1.0` are valid. Depleted accounts (`Remaining <= 0`) receive Priority `0`; invalid OAuth credentials (401) are disabled.
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
  -> Compute `remaining_headroom` for each credential from probed quota evidence
  -> Build a sorting plan only from fresh and ready evidence in this run:
       - Positive remaining quota: use `remaining_headroom` to drive scheduling weight
       - Depleted quota (Remaining <= 0): Priority = 0, Reason = "fresh remaining depleted"
       - Auth invalid (401): Priority = -1, Disabled = true, Reason = "xai auth invalid"
  -> Decide whether to write back by run mode:
       - apply: write priority and enabled state through host.auth.save
       - preview / dry_run: update status, diagnostics, snapshot, and logs only
  -> Show redacted statistics, audit summary, and sorting result on the management page
```

## Remaining Headroom

Each credential's scheduling weight is driven by `remaining_headroom`, computed from fresh quota evidence for the current run. It replaces the retired PacingScore metric.

For each known quota window, raw headroom is the pace surplus:

```
raw headroom = remaining quota % - remaining time %
```

Multi-window credentials use their lowest raw-headroom window as the bottleneck; raw values may be negative and remain visible for pacing diagnostics. Among fresh credentials with actual positive remaining quota, the planner applies one shared global translation:

```
uplift = max(0, -min(raw headroom of eligible credentials))
normalized headroom = raw headroom + uplift
```

`normalized headroom` drives the proportional scheduling weight. Accounts with zero remaining quota receive weight `0` and are excluded from the uplift baseline, so normalization cannot revive them. An expiring Codex banked reset credit adds `1.0` only to the final weight calculation (uncapped); it never changes raw headroom, the global uplift, or normalized headroom.

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
  Returns the latest redacted decision snapshot with `remaining_headroom`, scheduling-weight, and reset-credit details.

## Acknowledgments

- This project was originally forked from [Cody292/credential-priority](https://github.com/Cody292/credential-priority) — thanks to the original author for the plugin scaffold and provider-probing logic.
- Thanks to [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) for the host plugin platform (`host.auth.*` callbacks, Management Key verification, hot-reload, and more), which let this plugin focus purely on the pacing algorithm.

## License

This project is licensed under the MIT License. See [LICENSE](./LICENSE).
