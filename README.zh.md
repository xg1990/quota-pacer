<div align="center">

# Quota Pacer

[English](./README.md) | [中文](./README.zh.md)

</div>

CLIProxyAPI (CPA) 额度节奏（Pacing）自动调整插件，前身为 credential-priority。插件 ID、动态库基础名与 CPA 配置键均为 `quota-pacer`。

<div align="center">
  <img src="./picture/密钥验证页.png" alt="密钥验证页" width="32%" />
  <img src="./picture/概览页.png" alt="概览页" width="32%" />
  <img src="./picture/配置页.png" alt="配置页" width="32%" />
</div>

## 导航

- [功能概览](#功能概览)
- [工作流程](#工作流程)
- [PacingScore 算法](#pacingscore-算法)
- [构建与安装](#构建与安装)
- [插件商店来源](#插件商店来源)
- [配置说明](#配置说明)
- [管理页面与接口](#管理页面与接口)
- [致谢](#致谢)
- [许可证](#许可证)

## 功能概览

- 通过宿主回调 `host.auth.list`、`host.auth.get`、`host.auth.get_runtime`、`host.auth.save` 复用 CPA 的凭证、代理和写入链路。
- 只对本轮最新且可用的探测证据生成排序变更，避免用过期缓存调整凭证状态。
- 当前支持 Antigravity、Codex 与 xAI 凭证；后续可扩展其他提供商配置。
- 不同提供商的排序规则彼此独立，Antigravity、Codex 与 xAI 不共享额度耗尽策略。
- 状态页、诊断、快照与日志只输出脱敏后的凭证信息。
- **自动优先级与规则**通过 CPA **插件管理可视化配置字段**（`ConfigFields`）编辑；也可直接改 `config.yaml` / `plugins.configs.quota-pacer`。
- **插件页**支持 Management Key 验证、概览（只读生效配置）、执行记录（近 5 次）、帮助，以及手动 apply（management 路径）；**不在插件页内保存配置**。

## 工作流程

```text
加载插件
  -> 读取 plugins.configs.quota-pacer 配置
  -> 通过 host.auth.list 获取 CPA 凭证列表
  -> 按 provider_scope（all 或 antigravity|codex|claude|xai）筛选当前支持的提供商
       - Antigravity：按所选模型组探测剩余额度
       - Codex：按账号计划与额度状态探测可用性
       - Claude：按会话/5 小时重置窗口探测可用性与配额
        - xAI：FetchPlan（settings / billing / JWT）识别 free/paid；auto 不再 chat 多模型探额度
             业务额度由 usage.handle 累计；30 分钟内 2 次 429 → free 软禁用；401 AuthInvalid → 硬禁用
             Free 默认不参与优先级排序（free_participates_priority 默认 false）
  -> 只使用本轮最新且可用的探测证据生成排序计划
  -> 根据运行模式决定是否写回
       - apply：通过 host.auth.save 写回优先级与启用状态
       - preview：仅更新状态、诊断、快照与日志
  -> 在管理页面展示脱敏后的统计、审计摘要与排序结果
```

## PacingScore 算法

排序不再依赖固定阈值或提权规则，核心是一个无量纲的节奏健康度评分，用于在所有提供商、所有账号之间做统一的全局比较：

```
PacingScore = 剩余额度百分比 ÷ 剩余时间百分比
```

- **剩余额度百分比**：本轮探测得到的 `Remaining`（0-100）。探测失败或额度为 0 时得分直接为 0，账号自动沉底，不需要额外的"耗尽"判断分支。
- **剩余时间百分比**：距离配额重置的剩余时间 ÷ 所属周期总长度，裁剪到 `[0.001, 1.0]`（重置时间已过但证据未刷新时同样取下限，得分被放大）。周期总长度的判定：
  - 探测到长周期重置时间（如 OAuth 周长窗 `LongWindowResetAt`）时固定为 7 天；
  - 否则按短周期重置时间（`ResetAt`）距今的剩余时长动态归类：> 48 小时按 7 天算，6-48 小时按 24 小时算，< 6 小时按 5 小时算（对应 Claude / Codex 常见的会话级窗口）；
  - 完全没有重置时间证据时，退化为只用剩余额度百分比排序。
- **满额优先**：本轮 `Remaining >= 100` 时，直接按"刚重置"档位给出最高分，跳过剩余时间比例的计算，适用于全部提供商。部分账号在额度周期 reset 后不会立即激活真实计费窗口——窗口要等首次消费到 100% 额度时才开始计时。若不做这个特判，刚重置、尚未激活的满额账号会被算成全局最低分之一，长期闲置；优先处理它们能让周期尽快启动。

得分越高，说明这个账号"额度消耗进度落后于时间流逝进度"（用得比预期慢），应优先分配流量去消耗；得分 < 1 则说明消耗过快，应压低优先级。因为是两个百分比的比值，不同提供商即使配额语义完全不同（Antigravity 的模型组配额、Codex/Claude 的套餐窗口、xAI 的周/月限额），也能直接放在同一条全局优先级队列里比较，无需为每个提供商单独设计打分标准。

## 构建与安装

插件以 CGO 动态库形式运行，宿主会从动态库文件名去掉扩展名得到插件 ID，因此文件名必须保持为 `quota-pacer.<ext>`。

```bash
go build -buildmode=c-shared -o quota-pacer.so .
```

把产物放入 CPA 插件发现目录之一：

- `plugins/<GOOS>/<GOARCH>/quota-pacer.<ext>`
- `plugins/<GOOS>/<GOARCH>-<variant>/quota-pacer.<ext>`
- `plugins/quota-pacer.<ext>`

扩展名：Linux/FreeBSD 为 `.so`，macOS 为 `.dylib`，Windows 为 `.dll`。

## 插件商店来源

如需通过 CPA 插件商店安装本插件，第三方来源必须指向 `registry.json` 的原始 JSON 文本：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/xg1990/quota-pacer/main/registry.json"
```

不要使用 `https://github.com/xg1990/quota-pacer/blob/main/registry.json`。该地址返回 GitHub HTML 页面，CPA 无法按插件商店 registry 解析。修改 `store-sources` 后，重启 CPA 或通过管理端重新加载配置，再刷新插件商店列表。

## 配置说明

在 CPA `config.yaml` 中启用插件系统，并在 `plugins.configs.quota-pacer` 下保留插件自有配置：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    quota-pacer:
      enabled: true
      priority: 10
      auto_apply: false
      provider_scope: "all"   # 或 "antigravity|codex|claude|xai"
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

字段说明：

| 字段 | 说明 |
| :--- | :--- |
| `enabled` | 单插件开关；还需要全局 `plugins.enabled: true` 且动态库注册成功。与 `priority_rules.enabled` 语义独立。 |
| `priority` | CPA 宿主加载与执行插件的顺序，数值越大优先级越高。 |
| `auto_apply` | 是否由定时器自动执行并写回排序结果，默认 `false`。 |
| `provider_scope` | `all` 处理全部当前支持的提供商；也可填单个或多个提供商，多个用 `\|` 分隔，例如 `antigravity\|codex\|claude\|xai`。兼容旧配置 `selected` + `selected_providers`。 |
| `antigravity_model_group` | Antigravity 配额模型组，支持 `gemini` 与 `claude_gpt`。 |
| `priority_rules.enabled` | 是否启用自定义排序规则；关闭时使用内置排序策略。与顶层 `enabled` 独立。 |
| `interval` | 自动排序/探测分批时间步长（默认 15m）。disabled 凭证分批递进也使用该间隔（不再使用固定 1h 冷冻）。 |
| `immediate_probe_limit` / `active_group_size` | 控制本轮立即探测数量与 active 分批大小；disabled 分批组大小与 active 共用 `active_group_size`。 |

> **配置要点**（用户向）：支持扁平 `priority_rules.*` 配置；优先级完全由跨提供商统一的节奏算法（PacingScore）全局计算，不存在按提供商单独生效的起始优先级。`priority_rules.enabled` 控制额度耗尽等策略字段是否生效。禁用/耗尽凭证不再固定等 1 小时，节奏跟 `interval` 与分批参数走。临近额度刷新约 24 小时内且仍有额度时优先用完（Antigravity/Codex/Claude 以及 OAuth 付费 xAI 官方周长窗适用，xAI 免费不参与）。本轮探测到 Remaining=100% 的账号无论所属 provider 均排在最前。

### 提供商独立排序规则

Antigravity 规则

- 只排序本轮成功获取到所选模型组配额的 Antigravity 凭证。
- 配额获取失败或剩余额度不可用时保留当前优先级与启用状态。

Codex 规则

- `priority_rules.codex.free_depleted_priority`：Free 凭证额度为 0 时写入的优先级，默认 `-1`。
- `priority_rules.codex.free_depleted_disabled`：Free 凭证额度为 0 时是否禁用，默认 `true`。
- `priority_rules.codex.paid_depleted_disabled`：Plus、Pro、Team 额度耗尽时是否禁用；`true`=禁用，`false`=保持启用，默认 `false`。兼容旧键 `paid_depleted_keeps_enabled`（语义取反）。

Claude 规则

- `priority_rules.claude.free_depleted_priority`：Free 凭证额度为 0 时写入的优先级，默认 `-1`。
- `priority_rules.claude.free_depleted_disabled`：Free 凭证额度为 0 时是否禁用，默认 `true`。
- `priority_rules.claude.paid_depleted_disabled`：Pro、Team 额度耗尽时是否禁用；`true`=禁用，`false`=保持启用，默认 `false`。

xAI 规则

**套餐识别（FetchPlan）**

- 通过 settings / billing / JWT tier 轻量分类套餐为 `free` 或 `paid`，**auto 不再**对 chat 多模型探额度。
- 无法识别（网络失败、404、无套餐字段等）时**默认 `free`**；仅在明确付费 product/tier 时标 `paid`。
- **Free 排序默认关闭**：`free_participates_priority` 默认 `false`，Free 不参与正优先级提升 / free-first / uniqueness 重排。
- 仅当显式设置 `priority_rules.xai.free_participates_priority: true` 时，可参与排序的 free 才优先于 paid（优先消耗 free）。

**free 额度与 24h 锚点（与排序开关独立）**

- 业务额度由宿主 `usage.handle` 累计，不依赖 chat 探测。
- **30 分钟内累计 2 次** 429 → `priority=-1` **软禁用**（仅降优先级）；与 free 是否参与排序无关，默认仍生效。
- `free_depleted_disabled` 默认 `false`（软禁用，不硬 `disabled`）；显式设为 `true` 时才硬禁用。
- 冷却锚点：`first_success_at + 24h`（若无成功记录则用触发耗尽的失败时刻 + 24h）；到期后可恢复。

**401 AuthInvalid（硬禁用）**

- OAuth force 刷新后仍 401 / 凭证失效文案 → `priority=-1` 且 `disabled=true` 硬禁用。
- 硬禁用后需用户**重新登录**恢复；不计入 free 额度失败次数。

**配置字段**

- `priority_rules.xai.free_depleted_priority`：免费额度耗尽（软禁用）时写入的优先级，默认 `-1`。
- `priority_rules.xai.free_depleted_disabled`：免费额度耗尽时是否硬禁用，默认 `false`（软禁用：仅降 priority）。
- `priority_rules.xai.free_participates_priority`：Free 是否参与正优先级排序 / free-first，默认 `false`；显式 `true` 才 opt-in。关闭时不影响 429 耗尽、冷却与 401。
- `priority_rules.xai.weekly_depleted_priority`：仅周限额耗尽时写入的优先级，默认 `-1`（不禁用）。
- `priority_rules.xai.monthly_and_weekly_depleted_priority`：周与月均耗尽时写入的优先级，默认 `-1`。
- `priority_rules.xai.monthly_and_weekly_depleted_disabled`：周与月均耗尽时是否禁用，默认 `true`。

## 管理页面与接口

插件通过 `management.register` 分别注册 **resources**（静态壳）与 **routes**（动态业务）。

### 产品边界

| 能力 | 入口 | 说明 |
| :--- | :--- | :--- |
| 自动优先级与规则配置 | CPA 插件管理可视化字段（推荐）或 `config.yaml` | `auto_apply`、`provider_scope`（all 或 a\|b\|c）、`interval`、`priority_rules.*` 等 |
| 插件资源页 | `/v0/resource/plugins/quota-pacer/status` | 静态 HTML：Key 验证 + 概览/执行记录/帮助 + 手动排序 |
| 手动 apply | `/v0/management/plugins/quota-pacer/run` | 需要 Management Key |
| 只读配置 | 宿主 `GET /v0/management/plugins/quota-pacer/config` | 插件页只读展示，不在插件页 PATCH |

### 资源页面（静态）

- `GET /v0/resource/plugins/quota-pacer/status`
  返回静态 HTML 壳。浏览器侧用 Management Key 拉取只读数据与执行记录，并调用 management 路径手动排序。**无插件页内配置保存控件。**

### 管理 API（动态，需密钥）

- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider_scope=all&antigravity_model_group=gemini`
  手动触发探测、规划并写回凭证优先级。
- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider=antigravity&antigravity_model_group=claude_gpt`
  只处理 Antigravity 凭证并使用 Claude/GPT 模型组。
- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider=codex`
  只处理 Codex 凭证。
- `GET /v0/management/plugins/quota-pacer/diagnostics`
  导出脱敏诊断信息。
- `GET /v0/management/plugins/quota-pacer/snapshot/latest`
  获取最近一次运行的脱敏决策快照。

## 致谢

- 本项目最初 fork 自 [Cody292/credential-priority](https://github.com/Cody292/credential-priority)，感谢原作者搭建的插件骨架与提供商探测逻辑。
- 感谢 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 提供的插件宿主平台（`host.auth.*` 回调、Management Key 校验、热加载等能力），使本插件可以专注于节奏调度算法本身。

## 许可证

本项目使用 MIT License，详见 [LICENSE](./LICENSE)。
