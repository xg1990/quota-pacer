package priority

import (
	"cmp"
	"math"
	"slices"
	"time"

	"quota-pacer/internal/core"
)

const maxEnabledPriority = 999

// Options 是 fresh-only 优先级规划器的策略参数。
type Options struct {
	Now         time.Time
	MaxPriority int
	MinChange   int
}

// ProbeEvidence 是本轮 probe 产出的排序证据；EvidenceFresh=false 时不得驱动变更。
type ProbeEvidence struct {
	Provider          core.Provider
	AuthIndex         string
	ObservedAt        time.Time
	ResetAt           *time.Time
	Remaining         *int64
	Windows           []core.QuotaWindow
	LongWindowResetAt *time.Time
	// ShortWindowRemaining/ShortWindowResetAt: 5h 窗口剩余%与 reset 时间（provider 确认时才填）。
	ShortWindowRemaining *int64
	ShortWindowResetAt   *time.Time
	// LongWindowRemaining 与既有 LongWindowResetAt 配对：长/周窗口剩余%。
	LongWindowRemaining *int64
	Freshness           core.Freshness
	ProbeStatus         core.ProbeStatus
	Status              EvidenceStatus
	PlanType            core.PlanType
	EvidenceFresh       bool
	// XAIDepletedKind: free | weekly | monthly_and_weekly；空表示非 xAI 耗尽语义。
	XAIDepletedKind string
	// QuotaKnown 仅 xAI：false 时禁止驱动 priority/disabled 变更。
	QuotaKnown bool
	// XAIPlanClass: free | paid（套餐分类，非额度探测）。
	XAIPlanClass string
	// XAINextEligibleAt: free 冷却到期；未到期且 free 冷却中 → priority=-1。
	XAINextEligibleAt *time.Time
	// XAIQuotaFailCount: 连续额度类失败次数。
	XAIQuotaFailCount int
	// AvailableResetCredits 仅 Codex：当前 available 状态的银行化重置额度数量。
	AvailableResetCredits int
	// NearestResetCreditExpiresAt 仅 Codex：available 额度中最近的过期时间；nil 表示无可用额度。
	NearestResetCreditExpiresAt *time.Time
}

// EvidenceStatus 标识本轮 probe evidence 对规划器是否可用。
type EvidenceStatus string

const (
	// EvidenceStatusUnknown 表示没有可用于规划的 probe 结论。
	EvidenceStatusUnknown EvidenceStatus = "unknown"
	// EvidenceStatusReady 表示 evidence 可用于 fresh-only 规划。
	EvidenceStatusReady EvidenceStatus = "ready"
	// EvidenceStatusProbeFailed 表示本轮 probe 失败，必须保持现状。
	EvidenceStatusProbeFailed EvidenceStatus = "probe_failed"
	// EvidenceStatusUnsupported 表示 provider 不支持自动规划。
	EvidenceStatusUnsupported EvidenceStatus = "unsupported"
	// EvidenceStatusUnavailable 表示凭证当前不可用，必须保持现状。
	EvidenceStatusUnavailable EvidenceStatus = "unavailable"
	// EvidenceStatusAuthInvalid 表示 xAI OAuth 凭证失效，必须硬禁用。
	EvidenceStatusAuthInvalid EvidenceStatus = "auth_invalid"
)

// PlanItem 表示单个凭证在本轮规划后的目标状态。
type PlanItem struct {
	Credential core.Credential
	Priority   int
	// Weight 是本轮为该凭证算出的 CPA 加权轮询权重（仅对共享最高 priority tier
	// 的健康凭证有意义，其余凭证保持零值）。语义与算法见 weightFromHeadroom。
	Weight            int
	Disabled          bool
	PlanType          core.PlanType
	ResetAt           *time.Time
	Remaining         *int64
	Windows           []core.QuotaWindow
	LongWindowResetAt *time.Time
	// ShortWindowRemaining/ShortWindowResetAt: 5h 窗口剩余%与 reset 时间（provider 确认时才填）。
	ShortWindowRemaining *int64
	ShortWindowResetAt   *time.Time
	// LongWindowRemaining 与既有 LongWindowResetAt 配对：长/周窗口剩余%。
	LongWindowRemaining *int64
	EvidenceFresh       bool
	// ForceWrite 允许无本轮 fresh 证据的同伴因同 provider 优先级去重而写回宿主。
	ForceWrite bool
	Reason     string
	// RemainingHeadroom 是排序时实际使用的配速富余度快照（remainingHeadroom 的结果，
	// 与驱动 Weight 的是同一个指标），供审计/展示使用。
	RemainingHeadroom float64
	// AvailableResetCredits/NearestResetCreditExpiresAt 语义同 ProbeEvidence 同名字段。
	AvailableResetCredits       int
	NearestResetCreditExpiresAt *time.Time
}

// Change 表示需要由后续 apply writer 写回宿主的 fresh 证据变更。
type Change struct {
	Credential core.Credential
	Priority   int
	// Weight 镜像 PlanItem.Weight——共享最高 priority tier 的健康凭证才有非零值。
	Weight        int
	Disabled      bool
	EvidenceFresh bool
	Reason        string
}

// Plan 是 fresh-only 优先级规划结果。
type Plan struct {
	Items   []PlanItem
	Changes []Change
}

// PlanFreshOnly 只使用本轮 fresh probe evidence 生成优先级和禁用变更。
// 若某些凭证仅有缓存证据（EvidenceFresh=false），则仅填充剩余量与配额窗口供打分和审计展示，不产生写入变更。
func PlanFreshOnly(credentials []core.Credential, evidence []ProbeEvidence, options Options) Plan {
	freshByAuth := freshEvidenceByAuthIndex(evidence)
	cachedByAuth := cachedEvidenceByAuthIndex(evidence)
	items := initialItems(credentials, freshByAuth, cachedByAuth)
	planFreshPositive(items, options)
	// 跨账号全局优先级去重：保证全量启用态正优先级槽位唯一，且直接反映全局 Pacing 排序
	ensureUniquePriorities(items, options)
	sortPlanItems(items)
	for i := range items {
		items[i].RemainingHeadroom = remainingHeadroom(items[i], options.Now)
	}
	return Plan{Items: items, Changes: changes(items, options)}
}

func freshEvidenceByAuthIndex(evidence []ProbeEvidence) map[string]ProbeEvidence {
	byAuthIndex := make(map[string]ProbeEvidence, len(evidence))
	for _, item := range evidence {
		if isFreshReadyEvidence(item) {
			byAuthIndex[item.AuthIndex] = item
		}
	}
	return byAuthIndex
}

func cachedEvidenceByAuthIndex(evidence []ProbeEvidence) map[string]ProbeEvidence {
	byAuthIndex := make(map[string]ProbeEvidence, len(evidence))
	for _, item := range evidence {
		if !item.EvidenceFresh && item.Remaining != nil {
			byAuthIndex[item.AuthIndex] = item
		}
	}
	return byAuthIndex
}

func isFreshReadyEvidence(evidence ProbeEvidence) bool {
	return evidence.EvidenceFresh &&
		evidence.Freshness == core.FreshnessFresh &&
		evidence.ProbeStatus == core.ProbeStatusReady &&
		(evidence.Status == EvidenceStatusReady || evidence.Status == EvidenceStatusAuthInvalid)
}

func initialItems(credentials []core.Credential, freshByAuth map[string]ProbeEvidence, cachedByAuth map[string]ProbeEvidence) []PlanItem {
	items := make([]PlanItem, len(credentials))
	for index, credential := range credentials {
		item := PlanItem{
			Credential: credential,
			Priority:   credential.Priority,
			Disabled:   credential.Disabled,
			PlanType:   credential.PlanType,
			Reason:     "keep current state",
		}
		if fresh, ok := freshByAuth[credential.AuthIndex]; ok {
			item.PlanType = fresh.PlanType
			item.ResetAt = fresh.ResetAt
			item.Remaining = fresh.Remaining
			item.Windows = fresh.Windows
			item.LongWindowResetAt = fresh.LongWindowResetAt
			item.ShortWindowRemaining = fresh.ShortWindowRemaining
			item.ShortWindowResetAt = fresh.ShortWindowResetAt
			item.LongWindowRemaining = fresh.LongWindowRemaining
			item.AvailableResetCredits = fresh.AvailableResetCredits
			item.NearestResetCreditExpiresAt = fresh.NearestResetCreditExpiresAt
			item.EvidenceFresh = true

			if isXAIAuthInvalid(credential, fresh) {
				item.Priority = -1
				item.Disabled = true
				item.Reason = "xai auth invalid"
			} else if fresh.Remaining != nil && *fresh.Remaining <= 0 {
				item.Priority = 0
				item.Reason = "fresh remaining depleted"
			}
		} else if cached, ok := cachedByAuth[credential.AuthIndex]; ok {
			if cached.PlanType != core.PlanTypeUnknown && cached.PlanType != "" {
				item.PlanType = cached.PlanType
			}
			item.ResetAt = cached.ResetAt
			item.Remaining = cached.Remaining
			item.Windows = cached.Windows
			item.LongWindowResetAt = cached.LongWindowResetAt
			item.ShortWindowRemaining = cached.ShortWindowRemaining
			item.ShortWindowResetAt = cached.ShortWindowResetAt
			item.LongWindowRemaining = cached.LongWindowRemaining
			item.AvailableResetCredits = cached.AvailableResetCredits
			item.NearestResetCreditExpiresAt = cached.NearestResetCreditExpiresAt
			item.EvidenceFresh = false
		}
		items[index] = item
	}
	return items
}

func isXAIAuthInvalid(credential core.Credential, evidence ProbeEvidence) bool {
	return isXAICredential(credential) && evidence.Status == EvidenceStatusAuthInvalid
}

func isXAICredential(credential core.Credential) bool {
	return planItemProvider(PlanItem{Credential: credential}) == core.ProviderXAI
}

// weightScaleReference is the reference weight assigned to a fresh-positive
// tier member with a full [0,1] remaining-pace headroom (weightFromHeadroom).
// Kept well below CPA's [0, 1_000_000] weight ceiling (credentialweight.Max)
// so tier weights stay human-readable in the CPA management UI (e.g.
// 1000/340/12 instead of unwieldy six-digit numbers) while leaving ample
// headroom (1000x) for future fine-grained tuning.
const weightScaleReference = 1000

// weightFloor is the minimum weight ever assigned to a fresh-positive tier
// member. CPA's WeightedRoundRobinSelector excludes any credential with
// weight<=0 from the rotation entirely (its positiveWeightAuths filter) —
// weight 0 is not "a tiny trickle of traffic", it is "none at all". Flooring
// at 1 guarantees every currently-healthy credential in the shared tier
// keeps at least a minimal share — including one that has already burned
// past its pace target (remainingHeadroom floors to 0 for it) — so it is
// never fully starved out of the rotation.
const weightFloor = 1

// weightFromHeadroom maps a tier member's remaining-pace headroom (see
// remainingHeadroom, already floored to [0,1]) to an integer CPA weight,
// linearly proportional to weightScaleReference, floored at weightFloor.
// headroom=1.0 (full remaining-pace headroom) -> weightScaleReference;
// headroom=0 (already burned past pace target, no headroom left) still maps
// to weightFloor rather than 0, so it keeps a minimal share of traffic
// instead of being excluded from CPA's weighted rotation entirely.
func weightFromHeadroom(headroom float64) int {
	weight := int(math.Round(weightScaleReference * headroom))
	if weight < weightFloor {
		return weightFloor
	}
	return weight
}

// planFreshPositive assigns every fresh-positive candidate (this round's
// live probe evidence, Remaining > 0) to the SAME shared top priority,
// replacing the old strictly-unique-descending-priority ranking. Relative
// health is now expressed entirely through Weight (weightFromHeadroom, driven
// by remainingHeadroom — "距离配速目标用量，还可以多用掉多少百分比" — not by
// how the pacing score ranks against the tier's other members), which CPA's
// weighted-round-robin scheduler uses to proportionally split concurrent
// traffic among same-priority credentials — see ensureUniquePriorities for
// why this intentional sharing does not get "corrected" back into unique
// slots.
func planFreshPositive(items []PlanItem, options Options) {
	candidates := positiveCandidates(items)
	if len(candidates) == 0 {
		return
	}
	sharedPriority := normalizedMaxPriority(options.MaxPriority)
	if sharedPriority < 1 {
		sharedPriority = 100
	}

	for _, itemIndex := range candidates {
		headroom := remainingHeadroom(items[itemIndex], options.Now)
		items[itemIndex].Priority = sharedPriority
		items[itemIndex].Weight = weightFromHeadroom(headroom)
		// 禁用因额度耗尽的凭证，在探测到正向剩余额度后自动恢复启用并参与常规排序。
		items[itemIndex].Disabled = false
		items[itemIndex].Reason = "fresh remaining positive"
	}
}

// isFreshPositiveTierMember reports whether item belongs to this round's
// shared top-priority tier assigned by planFreshPositive (fresh evidence,
// Remaining > 0). Multiple such items intentionally sharing the same
// priority value is by design — see planFreshPositive and
// ensureUniquePriorities — not a collision to correct.
func isFreshPositiveTierMember(item PlanItem) bool {
	return item.EvidenceFresh && item.Remaining != nil && *item.Remaining > 0
}

// sharedTierPriority returns this round's shared top-priority value and
// whether any fresh-positive tier member exists in group. Every tier member
// carries the identical priority assigned by planFreshPositive, so the
// first one found is sufficient.
func sharedTierPriority(items []PlanItem, group []int) (int, bool) {
	for _, index := range group {
		if isFreshPositiveTierMember(items[index]) {
			return items[index].Priority, true
		}
	}
	return 0, false
}

// ensureUniquePriorities 保证跨账号全局启用态 priority>=1 的槽位在"非本轮共享
// 健康 tier"成员之间唯一。本轮共享 tier 内多个凭证持有相同的最高 priority 是
// planFreshPositive 的设计意图（配合 Weight 做同 tier 内加权分流），本函数不再
// 把这种情况当作冲突去纠正；但如果某个陈旧/无 fresh 证据的遗留凭证仍占用着
// tier 的那个 priority 槽位，或多个陈旧凭证彼此 priority 冲突，仍然按原逻辑
// 重新分配——只是排他区间从"tier 槽位以下"开始，不会挤占 tier 本身。
// 不改写 disabled 或 priority<=0（含 depleted 0 / auth invalid -1）的凭证。
func ensureUniquePriorities(items []PlanItem, options Options) {
	group := make([]int, 0, len(items))
	for index, item := range items {
		if item.Disabled || item.Priority < 1 {
			continue
		}
		group = append(group, index)
	}
	if len(group) == 0 {
		return
	}
	if !hasFreshPositive(items, group) {
		return
	}

	tierPriority, hasTier := sharedTierPriority(items, group)
	nonTierGroup := make([]int, 0, len(group))
	for _, index := range group {
		if !isFreshPositiveTierMember(items[index]) {
			nonTierGroup = append(nonTierGroup, index)
		}
	}

	if !hasPriorityCollision(items, nonTierGroup, tierPriority, hasTier) && !needsStartRealign(items, group, options) {
		return
	}
	slices.SortStableFunc(nonTierGroup, func(left int, right int) int {
		return compareUniquenessCandidates(items[left], items[right])
	})
	assigned := make(map[int]int, len(nonTierGroup))
	used := make(map[int]struct{}, len(nonTierGroup)+1)
	if hasTier {
		// 预占 tier 的 priority 槽位，防止非 tier 成员被重新分配到同一个值。
		used[tierPriority] = struct{}{}
	}
	startPriority := normalizedMaxPriority(options.MaxPriority)
	if startPriority < 1 {
		startPriority = 100
	}
	if hasTier && tierPriority <= startPriority {
		startPriority = tierPriority - 1
		if startPriority < 1 {
			startPriority = 1
		}
	}
	priority := startPriority
	for _, itemIndex := range nonTierGroup {
		nextPriority := nextAvailablePriority(priority, used)
		assigned[itemIndex] = nextPriority
		used[nextPriority] = struct{}{}
		priority--
		if priority < 1 {
			priority = 1
		}
	}
	for _, itemIndex := range nonTierGroup {
		nextPriority := assigned[itemIndex]
		if items[itemIndex].Priority != nextPriority {
			if !items[itemIndex].EvidenceFresh {
				items[itemIndex].ForceWrite = true
				items[itemIndex].Reason = "priority uniqueness"
			} else if items[itemIndex].Reason == "keep current state" || items[itemIndex].Reason == "" {
				items[itemIndex].Reason = "priority uniqueness"
			}
			items[itemIndex].Priority = nextPriority
		}
	}
}

func nextAvailablePriority(preferred int, used map[int]struct{}) int {
	if preferred > maxEnabledPriority {
		preferred = maxEnabledPriority
	}
	if preferred < 1 {
		preferred = 1
	}
	for priority := preferred; priority >= 1; priority-- {
		if _, exists := used[priority]; !exists {
			return priority
		}
	}
	return 1
}

func hasFreshPositive(items []PlanItem, group []int) bool {
	for _, index := range group {
		item := items[index]
		if item.EvidenceFresh && item.Remaining != nil && *item.Remaining > 0 {
			return true
		}
		if item.EvidenceFresh && item.Reason == "fresh remaining positive" {
			return true
		}
	}
	return false
}

// hasPriorityCollision 只检查非 tier 成员之间、以及非 tier 成员与共享 tier
// priority 槽位之间的冲突。多个 tier 成员彼此共享同一个 tierPriority 是
// planFreshPositive 的设计意图，不在这里被当作冲突处理（调用方已把它们从
// nonTierGroup 中排除）。
func hasPriorityCollision(items []PlanItem, nonTierGroup []int, tierPriority int, hasTier bool) bool {
	seen := make(map[int]struct{}, len(nonTierGroup)+1)
	if hasTier {
		seen[tierPriority] = struct{}{}
	}
	for _, index := range nonTierGroup {
		priority := items[index].Priority
		if priority > maxEnabledPriority {
			return true
		}
		if _, ok := seen[priority]; ok {
			return true
		}
		seen[priority] = struct{}{}
	}
	return false
}

func needsStartRealign(items []PlanItem, group []int, options Options) bool {
	// 预留：当前仅在碰撞时 re-pack；保留钩子便于后续策略扩展。
	_ = options
	_ = items
	_ = group
	return false
}

func compareUniquenessCandidates(left PlanItem, right PlanItem) int {
	// 无 fresh 同伴：较高现有优先级在前，其次 AuthIndex，保证稳定可复现。这里不需要
	// 任何"健康度"信号——调用方（ensureUniquePriorities）已经把本轮有 fresh 正向证据
	// 的 tier 成员整体排除在外了，走到这里的两个候选永远都不是 tier 成员。
	if left.Priority != right.Priority {
		return right.Priority - left.Priority
	}
	return cmp.Compare(left.Credential.AuthIndex, right.Credential.AuthIndex)
}

func planItemProvider(item PlanItem) core.Provider {
	if item.Credential.Provider != "" {
		return item.Credential.Provider
	}
	switch item.Credential.Type {
	case core.CredentialTypeCodex:
		return core.ProviderCodex
	case core.CredentialTypeAntigravity:
		return core.ProviderAntigravity
	case core.CredentialTypeClaude:
		return core.ProviderClaude
	case core.CredentialTypeXAI:
		return core.ProviderXAI
	default:
		return core.ProviderUnknown
	}
}

func positiveCandidates(items []PlanItem) []int {
	candidates := make([]int, 0, len(items))
	for index, item := range items {
		if !item.EvidenceFresh || item.Remaining == nil {
			continue
		}
		if *item.Remaining > 0 {
			candidates = append(candidates, index)
		}
	}
	return candidates
}

const shortWindowDuration = 5 * time.Hour
const longWindowDuration = 7 * 24 * time.Hour

// codexResetCreditExpiryWindow 是"额度即将过期"的判定阈值：available 额度的过期时间落在
// 未来 14 天内（且尚未过期）时，视为"再不用就浪费"，需要更激进地消耗额度以便触发限流兑换。
const codexResetCreditExpiryWindow = 14 * 24 * time.Hour

// codexResetCreditBoostActive 判断是否应对该 Codex 凭证的本轮打分施加"即将过期额度"提升。
// 仅限 Codex provider；要求存在 available 额度（AvailableResetCredits > 0）且其最近过期时间
// 落在 (now, now+14d] 区间内——已过期（<=0）或超过 14 天的额度均不触发。
func codexResetCreditBoostActive(item PlanItem, now time.Time) bool {
	if planItemProvider(item) != core.ProviderCodex {
		return false
	}
	if item.AvailableResetCredits <= 0 || item.NearestResetCreditExpiresAt == nil {
		return false
	}
	untilExpiry := item.NearestResetCreditExpiresAt.Sub(now)
	return untilExpiry > 0 && untilExpiry <= codexResetCreditExpiryWindow
}

// windowRemainingHeadroom 计算单个窗口的"配速富余度"：距离配速目标用量，还可以多用掉多少
// 配额比例（0..1），而不是距离硬上限（100%）还剩多少。
//
// 配速目标已用比例 = 1 - timeRemainingRatio（这个窗口已经流逝的时间比例，假设配额应随时间
// 匀速消耗，此刻"本该"用掉这么多）；实际已用比例 = 1 - remainingRatio。两者之差
// （配速目标已用比例 - 实际已用比例，等价于 remainingRatio - timeRemainingRatio）就是
// "追上配速目标之前，还能多用掉多少百分比"。若实际已用比例已经追上或超过配速目标（差值为
// 负），说明没有富余空间了，floor 到 0——注意这里只把"没有富余"floor 到 0，最终写回 CPA
// 的整数 weight 仍会在 weightFromHeadroom 里被 floor 到 weightFloor（而非 0），避免这类
// "已落后于配速"的账号被 WeightedRoundRobinSelector 的 weight<=0 过滤器踢出轮转。
//
// 两处边界特判（均直接给满 headroom=1.0，不计算时间比例）：
//   - remaining>=100：额度周期到点但计费尚未真正开始（部分 provider 的计费窗口从首次消费才
//     开始计时），此时剩余时间比例不可信，视同"完全没有落后于配速"。
//   - timeRemaining<=0：reset 时间已过但 remaining 尚未刷新（陈旧/边界数据），同样不信任此刻
//     算出的时间比例，按满 headroom 处理。
func windowRemainingHeadroom(remaining int64, resetAt time.Time, duration time.Duration, now time.Time) float64 {
	if remaining >= 100 {
		return 1.0
	}
	remainingRatio := float64(remaining) / 100.0

	timeRemaining := resetAt.Sub(now)
	if timeRemaining <= 0 {
		return 1.0
	}

	timeRemainingRatio := float64(timeRemaining) / float64(duration)
	if timeRemainingRatio > 1.0 {
		timeRemainingRatio = 1.0
	}
	if timeRemainingRatio < 0.001 {
		timeRemainingRatio = 0.001
	}

	headroom := remainingRatio - timeRemainingRatio
	if headroom < 0 {
		headroom = 0
	}
	return headroom
}

// remainingHeadroom 是 windowRemainingHeadroom 的多窗口/多字段入口，窗口来源级联与瓶颈窗口
// 选取（多窗口时取 headroom 最小的那个）：Windows 列表 -> ShortWindowRemaining/
// LongWindowRemaining 短长窗口对 -> legacy 单窗口回退（legacyRemainingHeadroom）。
//
// Codex "即将过期银行化重置额度" 提升（见 codexResetCreditBoostActive）只作用于这条驱动
// weight 的链路：在按正常规则算出瓶颈 headroom 之后，命中条件时直接 +1.0，不做任何上限
// clamp。之所以不是把 headroom 封顶在 1.0（满额）：如果只封顶到 1.0，这个即将浪费的账号会
// 和一个普通满额账号（headroom 同样是 1.0）算出完全一样的 weight，起不到"应该被优先消耗掉"
// 的效果；不封顶让它的 headroom 能明显超过 1.0（比如 0.5+1.0=1.5），对应 weight 明显超过
// 1000 这个正常上限，CPA 的 WeightedRoundRobinSelector 才会真的把更大比例的流量倾斜过去，
// 加速消耗这笔即将作废的额度，这才是提升机制的本意。即使正常算出的 headroom 已经 floor 到
// 0（已经落后于配速），+1.0 后仍能拿到 1.0，不会因为这类账号"原本就差"而在提升上吃亏。
func remainingHeadroom(item PlanItem, now time.Time) float64 {
	if item.Remaining == nil || *item.Remaining <= 0 {
		return 0 // 短路：primary 窗口已耗尽，理论上不会进入共享 tier
	}
	headroom := unboostedRemainingHeadroom(item, now)
	if codexResetCreditBoostActive(item, now) {
		headroom += 1.0
	}
	return headroom
}

// unboostedRemainingHeadroom 按正常规则（不含任何提升）计算瓶颈窗口的 headroom，供
// remainingHeadroom 在此基础上叠加 Codex reset-credit 提升。
func unboostedRemainingHeadroom(item PlanItem, now time.Time) float64 {
	if len(item.Windows) > 0 {
		headrooms := make([]float64, 0, len(item.Windows))
		for _, w := range item.Windows {
			if w.Duration <= 0 || w.ResetAt.IsZero() {
				continue
			}
			headrooms = append(headrooms, windowRemainingHeadroom(w.Remaining, w.ResetAt, w.Duration, now))
		}
		if len(headrooms) > 0 {
			return slices.Min(headrooms)
		}
	}

	headrooms := make([]float64, 0, 2)
	if item.ShortWindowRemaining != nil && item.ShortWindowResetAt != nil {
		headrooms = append(headrooms, windowRemainingHeadroom(*item.ShortWindowRemaining, *item.ShortWindowResetAt, shortWindowDuration, now))
	}
	if item.LongWindowRemaining != nil && item.LongWindowResetAt != nil {
		headrooms = append(headrooms, windowRemainingHeadroom(*item.LongWindowRemaining, *item.LongWindowResetAt, longWindowDuration, now))
	}
	if len(headrooms) > 0 {
		return slices.Min(headrooms)
	}

	return legacyRemainingHeadroom(item, now)
}

// legacyRemainingHeadroom 是新字段未提供时（xAI 等）的单窗口启发式回退路径。不含提升逻辑——
// 由 remainingHeadroom 统一在最终结果上叠加。
func legacyRemainingHeadroom(item PlanItem, now time.Time) float64 {
	remaining := *item.Remaining
	if remaining >= 100 {
		return 1.0
	}
	remainingRatio := float64(remaining) / 100.0

	var resetAt *time.Time
	var totalWindow time.Duration

	if item.LongWindowResetAt != nil && (item.ResetAt == nil || item.ResetAt.Equal(*item.LongWindowResetAt)) {
		resetAt = item.LongWindowResetAt
		totalWindow = longWindowDuration
	} else if item.ResetAt != nil {
		resetAt = item.ResetAt
		timeRemaining := resetAt.Sub(now)
		if timeRemaining > 48*time.Hour {
			totalWindow = longWindowDuration
		} else if timeRemaining > 6*time.Hour {
			totalWindow = 24 * time.Hour
		} else {
			totalWindow = shortWindowDuration
		}
	}

	if resetAt == nil || totalWindow <= 0 {
		// 没有任何可用的重置时间信息，无法推算配速目标，把全部剩余都当作富余。
		return remainingRatio
	}

	timeRemaining := resetAt.Sub(now)
	if timeRemaining <= 0 {
		return 1.0
	}

	timeRemainingRatio := float64(timeRemaining) / float64(totalWindow)
	if timeRemainingRatio > 1.0 {
		timeRemainingRatio = 1.0
	}
	if timeRemainingRatio < 0.001 {
		timeRemainingRatio = 0.001
	}

	headroom := remainingRatio - timeRemainingRatio
	if headroom < 0 {
		headroom = 0
	}
	return headroom
}

func paidRank(planType core.PlanType) int {
	switch planType {
	case core.PlanTypeTeam, core.PlanTypePlus, core.PlanTypePro:
		return 1
	case core.PlanTypeFree, core.PlanTypeUnknown:
		return 0
	default:
		return 0
	}
}

func normalizedMaxPriority(maxPriority int) int {
	if maxPriority < 1 {
		return 1
	}
	if maxPriority > maxEnabledPriority {
		return maxEnabledPriority
	}
	return maxPriority
}

func sortPlanItems(items []PlanItem) {
	slices.SortStableFunc(items, func(left PlanItem, right PlanItem) int {
		if left.EvidenceFresh && right.EvidenceFresh {
			if left.Priority != right.Priority {
				return right.Priority - left.Priority
			}
			return cmp.Compare(left.Credential.AuthIndex, right.Credential.AuthIndex)
		}
		if left.EvidenceFresh {
			return -1
		}
		if right.EvidenceFresh {
			return 1
		}
		return 0
	})
}

func changes(items []PlanItem, options Options) []Change {
	result := make([]Change, 0)
	for _, item := range items {
		if shouldChange(item, options) {
			result = append(result, Change{
				Credential: item.Credential,
				Priority:   item.Priority,
				Weight:     item.Weight,
				Disabled:   item.Disabled,
				// ForceWrite 同伴无本轮 probe，但必须通过 apply 的 EvidenceFresh 写入门闸。
				EvidenceFresh: item.EvidenceFresh || item.ForceWrite,
				Reason:        item.Reason,
			})
		}
	}
	return result
}

func shouldChange(item PlanItem, options Options) bool {
	// ForceWrite：同 provider 优先级去重改写无 fresh 同伴时必须写回宿主。
	if !item.EvidenceFresh && !item.ForceWrite {
		return false
	}
	if item.ForceWrite && !item.EvidenceFresh {
		if item.Priority == item.Credential.Priority && item.Disabled == item.Credential.Disabled {
			return false
		}
		return abs(item.Priority-item.Credential.Priority) >= normalizedMinChange(options.MinChange) ||
			item.Disabled != item.Credential.Disabled ||
			item.Credential.PriorityMissing
	}
	if item.Reason == "fresh remaining positive" {
		// tier 内本轮 Priority 保持不变，但 Weight 需要跟随 headroom 每轮
		// 刷新写回；CPA host.auth.list 当前不回传 weight 字段，无法读回当前值
		// 做等值比较，因此 tier 成员统一按"本轮总是变化"处理。
		return true
	}
	if item.Credential.PriorityMissing {
		return true
	}
	if item.Priority == item.Credential.Priority && item.Disabled == item.Credential.Disabled {
		return false
	}
	if item.Priority == -1 && item.Disabled {
		return item.Credential.Priority != -1 || !item.Credential.Disabled
	}
	if item.Credential.Disabled != item.Disabled {
		return true
	}
	return abs(item.Priority-item.Credential.Priority) >= normalizedMinChange(options.MinChange)
}

func normalizedMinChange(minChange int) int {
	if minChange < 0 {
		return 0
	}
	return minChange
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
