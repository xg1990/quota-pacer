<div align="center">

# Quota Pacer

[English](./README.md) | [中文](./README.zh.md)

</div>

CLIProxyAPI (CPA) 额度节奏（Pacing）跨提供商自动平衡插件，前身为 credential-priority。插件 ID、动态库基础名与 CPA 配置键均为 `quota-pacer`。

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
- 当前支持 Antigravity、Codex、Claude 与 xAI 凭证在统一的全局优先级下协同调度。
- **纯粹的 PacingScore 算法**：移除所有提供商特判与复杂耗尽分支。正额度账号根据 PacingScore 自动排序；额度耗尽（`Remaining <= 0`）自然得分为 0 并分配优先级 `0`；OAuth 失效（401）标记硬禁用。
- 状态页、诊断、快照与日志只输出脱敏后的凭证信息。
- **配置管理**：通过 CPA **插件管理可视化配置字段**（`ConfigFields`）编辑，或直接修改 `config.yaml` / `plugins.configs.quota-pacer`。
- **插件页**支持 Management Key 验证、概览（只读生效配置）、执行记录（近 5 次）、帮助，以及手动触发排序。

## 工作流程

```text
加载插件
  -> 读取 plugins.configs.quota-pacer 配置
  -> 通过 host.auth.list 获取 CPA 凭证列表
  -> 按 provider_scope（all 或 antigravity|codex|claude|xai）筛选当前支持的提供商
       - Antigravity：按所选模型组探测剩余额度
       - Codex：探测可用性与剩余额度
       - Claude：按会话/5 小时重置窗口探测可用性与配额
       - xAI：通过业务用量及 OAuth 状态探测额度与重置窗口
  -> 根据最新探测额度与剩余时间计算每个账号的 PacingScore
  -> 基于本轮 fresh 证据生成规划结果：
       - 正额度账号：按 PacingScore 降序从 MaxPriority（如 100）向下分配全局唯一优先级
       - 额度耗尽账号（Remaining <= 0）：Priority = 0, Reason = "fresh remaining depleted"
       - 凭据失效（401）：Priority = -1, Disabled = true, Reason = "xai auth invalid"
  -> 根据运行模式决定是否写回：
       - apply：通过 host.auth.save 写回优先级与启用状态
       - preview / dry_run：仅更新状态、诊断、快照与日志
  -> 在管理页面展示脱敏后的统计、审计摘要与 Pacing 计算详情
```

## PacingScore 算法

排序不再依赖固定阈值或提供商规则，核心是一个无量纲的节奏健康度评分，用于在所有提供商、所有账号之间做统一的全局比较：

```
PacingScore = 剩余额度百分比 ÷ 剩余时间百分比
```

- **剩余额度百分比**：本轮探测得到的 `Remaining`（0-100）。探测失败或额度为 0 时得分直接为 `0`，账号自动沉底（`Priority = 0`）。
- **剩余时间百分比**：距离配额重置的剩余时间 ÷ 所属周期总长度，裁剪到 `[0.001, 1.0]`。周期总长度判定：
  - 探测到长周期重置时间（如 OAuth 周长窗 `LongWindowResetAt`）时固定为 7 天（168h）；
  - 否则按短周期重置时间（`ResetAt`）距今的剩余时长动态归类：`> 48 小时`按 7 天算，`6-48 小时`按 24 小时算，`< 6 小时`按 5 小时算（对应 Claude / Codex 常见的会话级窗口）；
  - 完全没有重置时间证据时，退化为只用剩余额度百分比排序。
- **满额优先**：本轮 `Remaining >= 100` 时，直接按最高档位（`Remaining / 0.001`）计分，跳过剩余时间比例的计算。部分账号在额度周期 reset 后真实计费窗口要等首次消费时才开始计时；优先分配它们能让周期尽快启动。

得分越高，说明该账号"额度消耗进度落后于时间流逝进度"（用得比预期慢），应优先分配流量去消耗；得分 < 1 则说明消耗过快，应压低优先级。

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
      antigravity_model_group: "gemini" # 或 "claude_gpt"
      interval: "15m"
      immediate_probe_limit: 30
      max_concurrency: 6
      active_group_size: 10
```

字段说明：

| 字段 | 说明 | 默认值 |
| :--- | :--- | :--- |
| `enabled` | 单插件开关。还需要全局 `plugins.enabled: true`。 | `true` |
| `priority` | CPA 宿主加载与执行插件的顺序，数值越大越先执行。 | `10` |
| `auto_apply` | 是否由定时器自动执行并写回排序结果。 | `false` |
| `provider_scope` | `all` 处理全部支持的提供商；或用 `\|` 分隔指定提供商。 | `all` |
| `antigravity_model_group` | Antigravity 配额模型组，支持 `gemini` 与 `claude_gpt`。 | `gemini` |
| `interval` | 自动排序/探测分批时间步长（如 `15m`）。 | `15m` |
| `immediate_probe_limit` | 单轮立即探测的凭证数量上限。 | `30` |
| `max_concurrency` | 探测并发上限。 | `6` |
| `active_group_size` | 分批探测时每批凭证数量。 | `10` |

## 管理页面与接口

插件通过 `management.register` 分别注册 **resources**（静态网页）与 **routes**（动态业务）。

### 产品边界

| 能力 | 入口 | 说明 |
| :--- | :--- | :--- |
| 自动优先级配置 | CPA 插件管理可视化字段（推荐）或 `config.yaml` | `auto_apply`、`provider_scope`、`interval` 等 |
| 插件资源页 | `/v0/resource/plugins/quota-pacer/status` | 静态 HTML：Key 验证 + 概览/执行记录/帮助 + 手动排序 |
| 手动 apply | `/v0/management/plugins/quota-pacer/run` | 需要 Management Key |
| 只读配置 | 宿主 `GET /v0/management/plugins/quota-pacer/config` | 插件页只读展示，不在插件页 PATCH |

### 资源页面（静态）

- `GET /v0/resource/plugins/quota-pacer/status`
  返回静态 HTML 壳。浏览器侧用 Management Key 拉取只读数据与执行记录，并调用 management 路径手动排序。

### 管理 API（动态，需密钥）

- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider_scope=all&antigravity_model_group=gemini`
  手动触发探测、规划并写回凭证优先级。
- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider=antigravity&antigravity_model_group=claude_gpt`
  只处理 Antigravity 凭证并使用 Claude/GPT 模型组。
- `POST /v0/management/plugins/quota-pacer/run?mode=apply&provider=codex`
  只处理 Codex 凭证。
- `GET /v0/management/plugins/quota-pacer/diagnostics`
  导出脱敏诊断信息与历史记录。
- `GET /v0/management/plugins/quota-pacer/snapshot/latest`
  获取最近一次运行的脱敏决策快照与 PacingScore 计算表。

## 致谢

- 本项目最初 fork 自 [Cody292/credential-priority](https://github.com/Cody292/credential-priority)，感谢原作者搭建的插件骨架与提供商探测逻辑。
- 感谢 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 提供的插件宿主平台（`host.auth.*` 回调、Management Key 校验、热加载等能力），使本插件可以专注于节奏调度算法本身。

## 许可证

本项目使用 MIT License，详见 [LICENSE](./LICENSE)。
