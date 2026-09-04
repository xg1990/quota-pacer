package priority

import (
	"cmp"
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
	Credential        core.Credential
	Priority          int
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
	// PacingScore 是排序时实际使用的 Pacing 健康度得分快照，供审计/展示使用。
	PacingScore float64
	// AvailableResetCredits/NearestResetCreditExpiresAt 语义同 ProbeEvidence 同名字段。
	AvailableResetCredits       int
	NearestResetCreditExpiresAt *time.Time
}

// Change 表示需要由后续 apply writer 写回宿主的 fresh 证据变更。
type Change struct {
	Credential    core.Credential
	Priority      int
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
		items[i].PacingScore = pacingScore(items[i], options.Now)
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

func planFreshPositive(items []PlanItem, options Options) {
	candidates := positiveCandidates(items)
	slices.SortStableFunc(candidates, func(left int, right int) int {
		return compareCandidates(items[left], items[right], options)
	})
	startPriority := normalizedMaxPriority(options.MaxPriority)
	if startPriority < 1 {
		startPriority = 100
	}
	priority := startPriority
	for _, itemIndex := range candidates {
		items[itemIndex].Priority = priority
		// 禁用因额度耗尽的凭证，在探测到正向剩余额度后自动恢复启用并参与常规排序。
		items[itemIndex].Disabled = false
		items[itemIndex].Reason = "fresh remaining positive"
		priority--
		if priority < 1 {
			priority = 1
		}
	}
}

// ensureUniquePriorities 保证跨账号全局启用态 priority>=1 的槽位唯一。
// 参与者包括：本轮 fresh 正额度、以及仍占用正优先级的无 fresh 同伴（历史局部写回残留）。
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
	if !hasPriorityCollision(items, group) && !needsStartRealign(items, group, options) {
		return
	}
	slices.SortStableFunc(group, func(left int, right int) int {
		return compareUniquenessCandidates(items[left], items[right], options)
	})
	assigned := make(map[int]int, len(group))
	used := make(map[int]struct{}, len(group))
	startPriority := normalizedMaxPriority(options.MaxPriority)
	if startPriority < 1 {
		startPriority = 100
	}
	priority := startPriority
	for _, itemIndex := range group {
		nextPriority := nextAvailablePriority(priority, used)
		assigned[itemIndex] = nextPriority
		used[nextPriority] = struct{}{}
		priority--
		if priority < 1 {
			priority = 1
		}
	}
	for _, itemIndex := range group {
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

func hasPriorityCollision(items []PlanItem, group []int) bool {
	seen := make(map[int]struct{}, len(group))
	for _, index := range group {
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

func compareUniquenessCandidates(left PlanItem, right PlanItem, options Options) int {
	leftFreshPositive := left.EvidenceFresh && left.Remaining != nil && *left.Remaining > 0
	rightFreshPositive := right.EvidenceFresh && right.Remaining != nil && *right.Remaining > 0
	switch {
	case leftFreshPositive && rightFreshPositive:
		return compareCandidates(left, right, options)
	case leftFreshPositive:
		return -1
	case rightFreshPositive:
		return 1
	}
	// 无 fresh 同伴：较高现有优先级在前，其次 AuthIndex，保证稳定可复现。
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

func compareCandidates(left PlanItem, right PlanItem, options Options) int {
	// 基于 Pacing 健康度评分排序：优先消耗剩余额度比例落后于时间流逝比例的账号
	if scoreCmp := comparePacingScores(pacingScore(left, options.Now), pacingScore(right, options.Now)); scoreCmp != 0 {
		return scoreCmp
	}
	if left.Remaining != nil && right.Remaining != nil && *left.Remaining != *right.Remaining {
		if *left.Remaining > *right.Remaining {
			return -1
		}
		return 1
	}
	if result := compareResetAt(left.ResetAt, right.ResetAt); result != 0 {
		return result
	}
	return cmp.Compare(left.Credential.AuthIndex, right.Credential.AuthIndex)
}

const floatEpsilon = 1e-6

func comparePacingScores(scoreLeft, scoreRight float64) int {
	diff := scoreLeft - scoreRight
	if diff > floatEpsilon {
		return -1 // scoreLeft > scoreRight，left 优先
	}
	if diff < -floatEpsilon {
		return 1 // scoreRight > scoreLeft，right 优先
	}
	return 0
}

const shortWindowDuration = 5 * time.Hour
const longWindowDuration = 7 * 24 * time.Hour

// windowPacingScore 计算单个窗口的 Pacing 健康度子分数（数值语义与原单窗口算法一致）。
func windowPacingScore(remaining int64, resetAt time.Time, duration time.Duration, now time.Time) float64 {
	if remaining <= 0 {
		return 0
	}
	remainingRatio := float64(remaining) / 100.0
	if remaining >= 100 {
		return remainingRatio / 0.001
	}

	timeRemaining := resetAt.Sub(now)
	if timeRemaining <= 0 {
		return remainingRatio / 0.001
	}

	timeRemainingRatio := float64(timeRemaining) / float64(duration)
	if timeRemainingRatio > 1.0 {
		timeRemainingRatio = 1.0
	}
	if timeRemainingRatio < 0.001 {
		timeRemainingRatio = 0.001
	}

	return remainingRatio / timeRemainingRatio
}

// codexResetCreditExpiryWindow 是"额度即将过期"的判定阈值：available 额度的过期时间落在
// 未来 14 天内（且尚未过期）时，视为"再不用就浪费"，需要更激进地消耗额度以便触发限流兑换。
const codexResetCreditExpiryWindow = 14 * 24 * time.Hour

// codexResetCreditBoostMultiplier 是命中提升条件时对 Remaining 施加的相对倍数（而非绝对
// 百分点叠加）——windowPacingScore 对 remaining>=100 有专门的"满额天花板"分支，绝对值
// +100 会让几乎所有有正余量的凭证瞬间撞进该分支、抹平彼此的相对紧急度差异；改为倍增
// remainingRatio 只放大分子，让不同真实剩余量的凭证在提升后依然保有区分度。
const codexResetCreditBoostMultiplier = 2

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

// boostRemaining 在命中提升条件时对窗口剩余百分比做相对倍增；不封顶——windowPacingScore
// 的 remaining>=100 分支本身就会按 remainingRatio 的实际值区分不同原始剩余量的凭证，
// 提前封顶反而会让多个提升后凭证的分数重新退化为同一个值。
func boostRemaining(remaining int64, boost bool) int64 {
	if !boost {
		return remaining
	}
	return remaining * codexResetCreditBoostMultiplier
}

// pacingScore 计算凭据的额度消耗健康度得分（Pacing / Burn Rate Ratio）。
// 遍历凭据的所有有效额度窗口（Windows），取最紧张（Pacing 分数最低，即瓶颈窗口）的那个；
// 若无多窗口结构，则尝试短窗/长窗 legacy 字段，最终回退到单窗口启发式。
// Codex 凭证若命中"即将过期的银行化重置额度"提升条件（见 codexResetCreditBoostActive），
// 会在计算各窗口分数前对 Remaining 做相对倍增，鼓励更激进地消耗以避免额度浪费。
func pacingScore(item PlanItem, now time.Time) float64 {
	if item.Remaining == nil || *item.Remaining <= 0 {
		return 0 // 短路：primary 窗口已耗尽 = 当前不可用，不受多窗口逻辑影响，也不受提升逻辑影响
	}
	boost := codexResetCreditBoostActive(item, now)

	if len(item.Windows) > 0 {
		scores := make([]float64, 0, len(item.Windows))
		for _, w := range item.Windows {
			if w.Duration <= 0 || w.ResetAt.IsZero() {
				continue
			}
			scores = append(scores, windowPacingScore(boostRemaining(w.Remaining, boost), w.ResetAt, w.Duration, now))
		}
		if len(scores) > 0 {
			return slices.Min(scores)
		}
	}

	scores := make([]float64, 0, 2)
	if item.ShortWindowRemaining != nil && item.ShortWindowResetAt != nil {
		scores = append(scores, windowPacingScore(boostRemaining(*item.ShortWindowRemaining, boost), *item.ShortWindowResetAt, shortWindowDuration, now))
	}
	if item.LongWindowRemaining != nil && item.LongWindowResetAt != nil {
		scores = append(scores, windowPacingScore(boostRemaining(*item.LongWindowRemaining, boost), *item.LongWindowResetAt, longWindowDuration, now))
	}
	if len(scores) > 0 {
		return slices.Min(scores)
	}

	return legacyPacingScore(item, now, boost)
}

// legacyPacingScore 是新字段未提供时（xAI 等）的单窗口启发式回退路径。
func legacyPacingScore(item PlanItem, now time.Time, boost bool) float64 {
	remaining := boostRemaining(*item.Remaining, boost)
	remainingRatio := float64(remaining) / 100.0

	// 满额（Remaining=100%）账号优先处理：部分账号在额度周期 reset 后不会立即激活，
	// 真实计费窗口从首次消费才开始计时，此时按剩余时间比例计分会把它们排到最后。
	if remaining >= 100 {
		return remainingRatio / 0.001
	}

	// 确定重置时间与所属周期总长度
	// 仅当 LongWindowResetAt 与 ResetAt 指向同一时刻（或 ResetAt 缺失）时，才说明
	// Remaining 本身就来自这个周窗口，可以用 7 天做基准；否则（如 xAI 日冷却+周账单）
	// Remaining/ResetAt 来自另一个更短的窗口，绝不能借用 LongWindowResetAt 的时间做
	// 分母，否则分子分母不是同一个窗口。
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
		return remainingRatio
	}

	timeRemaining := resetAt.Sub(now)
	if timeRemaining <= 0 {
		return remainingRatio / 0.001
	}

	timeRemainingRatio := float64(timeRemaining) / float64(totalWindow)
	if timeRemainingRatio > 1.0 {
		timeRemainingRatio = 1.0
	}
	if timeRemainingRatio < 0.001 {
		timeRemainingRatio = 0.001
	}

	return remainingRatio / timeRemainingRatio
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

func compareResetAt(left *time.Time, right *time.Time) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return 1
	case right == nil:
		return -1
	case left.Equal(*right):
		return 0
	case left.Before(*right):
		return -1
	default:
		return 1
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
