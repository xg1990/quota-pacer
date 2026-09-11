<div align="center">

# Quota Pacer

[English](./README.md) | [中文](./README.zh.md)

</div>

Quota Pacer（原 credential-priority）是面向 CLIProxyAPI (CPA) 的多提供商配额感知与流量平衡插件。它依据各账号本轮最新获取的额度证据及配速富余度（`remaining_headroom`），自动计算调度权重并动态平衡凭证流量。插件 ID、动态库文件名与 CPA 配置键统一为 `quota-pacer`。

## 导航

- [功能概览](#功能概览)
- [为什么用权重而非仅优先级](#为什么用权重而非仅优先级)
- [工作流程](#工作流程)
- [配速富余度](#配速富余度)
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
- **基于配速富余度的调度**：`remaining_headroom` 直接驱动每个账号的调度权重，允许超过 `1.0`；额度耗尽（`Remaining <= 0`）分配优先级 `0`；OAuth 失效（401）标记硬禁用。
- 状态页、诊断、快照与日志只输出脱敏后的凭证信息。
- **配置管理**：通过 CPA **插件管理可视化配置字段**（`ConfigFields`）编辑，或直接修改 `config.yaml` / `plugins.configs.quota-pacer`。
- **插件页**支持 Management Key 验证、概览（只读生效配置）、执行记录（近 5 次）、帮助，以及手动触发排序。

## 为什么用权重而非仅优先级

CLIProxyAPI 的 `fill-first`（默认）路由策略会把全部流量压给排序最靠前的单一凭证，直到它耗尽或进入冷却，才会轮到下一个。这意味着即使还有多个凭证配额充裕、处于空闲状态，并发上限也会被死死卡在这一个凭证自身的速率限制上——这正是多凭证场景下"高并发时反而吞吐上不去"的根因。

quota-pacer 面向的是 CPA 的 `weighted-round-robin` 调度策略：在同一优先级档位内，请求会按 `weight` 比例（Smooth Weighted Round Robin 平滑加权轮询）分发到所有健康凭证，而不是全部堆到一个账号上。这样一来，并发场景下的整体可用吞吐量趋近于多个健康凭证速率限制之和，而不是被单个账号封顶。

`priority` 依然是硬性的档位门槛——调度器永远只在"当前健康的最高优先级档位"内选择；`weight` 与之正交，只负责该档位内部的比例分配。这正是 quota-pacer 把 `remaining_headroom` 换算成 `weight` 值、而不是单纯重排 `priority` 的原因。

注：CPA 的 `session-affinity` 会让已建立的长会话继续粘在原凭证上（为了保持 prompt cache 一致性），只有新会话才会按权重重新分发——首次切换调度策略时看到这个现象是设计使然，不是 bug。

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
  -> 根据本轮最新探测证据计算每个账号的 `remaining_headroom`
  -> 基于本轮 fresh 证据生成规划结果：
       - 正额度账号：由 `remaining_headroom` 驱动调度权重
       - 额度耗尽账号（Remaining <= 0）：Priority = 0, Reason = "fresh remaining depleted"
       - 凭据失效（401）：Priority = -1, Disabled = true, Reason = "xai auth invalid"
  -> 根据运行模式决定是否写回：
       - apply：通过 host.auth.save 写回优先级与启用状态
       - preview / dry_run：仅更新状态、诊断、快照与日志
  -> 在管理页面展示脱敏后的统计、审计摘要与 Pacing 计算详情
```

## 配速富余度

每个账号的调度权重由本轮最新额度证据计算出的 `remaining_headroom` 驱动；它已取代早期废弃的 PacingScore 指标。

对于每个已知的额度窗口，原始配速富余度（raw headroom）定义为配额结余与时间进度的差值：

```
原始富余度 (raw headroom) = 剩余额度百分比 - 剩余时间百分比
```

多窗口账号以各个窗口中最低的原始富余度作为当前瓶颈。原始富余度允许为负值（表示该账号当前用量已落后于配速目标），并在管理面板保留以供配速诊断。

在所有具备本轮新鲜证据且实际有正剩余额度的凭证之间，规划器会应用一个统一的全局平移（global uplift）：

```
平移量 (uplift) = max(0, -min(合格凭证的原始富余度))
归一化富余度 (normalized headroom) = 原始富余度 + 平移量
```

`归一化富余度` 负责按比例驱动各个凭据的调度权重。这样即使所有账号均处于超速亏空状态（原始富余度全为负），调度器依然能按相对配速优劣拉开分配差距，而不是一律归零。

剩余配额耗尽（`Remaining <= 0`）的账号调度权重直接归零，且不计入全局平移的计算基准，绝不会被平移“复活”。Codex 即将过期的银行化重置额度加成（+1.0）仅不封顶地叠加在最终的权重计算链路上，不会反向污染原始富余度、全局平移量或归一化富余度本身。

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
  获取最近一次运行的脱敏决策快照，以及 `remaining_headroom`、调度权重和重置额度详情。

## 致谢

- 本项目最初 fork 自 [Cody292/credential-priority](https://github.com/Cody292/credential-priority)，感谢原作者搭建的插件骨架与提供商探测逻辑。
- 感谢 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 提供的插件宿主平台（`host.auth.*` 回调、Management Key 校验、热加载等能力），使本插件可以专注于节奏调度算法本身。

## 许可证

本项目使用 MIT License，详见 [LICENSE](./LICENSE)。
