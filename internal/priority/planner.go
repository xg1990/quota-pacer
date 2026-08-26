package priority

import (
	"cmp"
	"slices"
	"time"

	"quota-pacer/internal/core"
)

const maxEnabledPriority = 999

// Options 是 fresh-only 优先级规划器的已解析策略参数。
type Options struct {
	Now                        time.Time
	MaxPriority                int
	CodexFreeDepletedPriority  *int
	CodexFreeDepletedDisabled  *bool
	CodexPaidDepletedDisabled  *bool
	ClaudeFreeDepletedPriority *int
	ClaudeFreeDepletedDisabled *bool
	ClaudePaidDepletedDisabled *bool
	XAIFreeDepletedPriority    *int
	XAIFreeDepletedDisabled    *bool
	// XAIFreeParticipatesPriority：true 时 free 参与正优先级/free-first/uniqueness；nil/false（默认）时仅保留耗尽/冷却/401 链。
	XAIFreeParticipatesPriority         *bool
	XAIWeeklyDepletedPriority           *int
	XAIMonthlyAndWeeklyDepletedPriority *int
	XAIMonthlyAndWeeklyDepletedDisabled *bool
	MinChange                           int
	PaidFirst                           bool
}

// ProbeEvidence 是本轮 probe 产出的排序证据；EvidenceFresh=false 时不得驱动变更。
type ProbeEvidence struct {
	Provider          core.Provider
	AuthIndex         string
	ObservedAt        time.Time
	ResetAt           *time.Time
	Remaining         *int64
	LongWindowResetAt *time.Time
	Freshness         core.Freshness
	ProbeStatus       core.ProbeStatus
	Status            EvidenceStatus
	PlanType          core.PlanType
	EvidenceFresh     bool
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
	LongWindowResetAt *time.Time
	EvidenceFresh     bool
	// ForceWrite 允许无本轮 fresh 证据的同伴因同 provider 优先级去重而写回宿主。
	ForceWrite bool
	Reason     string
	// PacingScore 是排序时实际使用的 Pacing 健康度得分快照，供审计/展示使用。
	PacingScore float64
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
func PlanFreshOnly(credentials []core.Credential, evidence []ProbeEvidence, options Options) Plan {
	evidenceByAuthIndex := freshEvidenceByAuthIndex(evidence)
	items := initialItems(credentials, evidenceByAuthIndex, options)
	planFreshPositive(items, options)
	// 跨账号全局优先级去重：保证全量启用态正优先级槽位唯一，且直接反映全局 Pacing 排序
	ensureUniquePriorities(items, options)
	capExcludedXAIFreePriorities(items, options)
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

func isFreshReadyEvidence(evidence ProbeEvidence) bool {
	return evidence.EvidenceFresh &&
		evidence.Freshness == core.FreshnessFresh &&
		evidence.ProbeStatus == core.ProbeStatusReady &&
		(evidence.Status == EvidenceStatusReady || evidence.Status == EvidenceStatusAuthInvalid)
}

func initialItems(credentials []core.Credential, evidenceByAuthIndex map[string]ProbeEvidence, options Options) []PlanItem {
	items := make([]PlanItem, len(credentials))
	for index, credential := range credentials {
		item := PlanItem{
			Credential: credential,
			Priority:   credential.Priority,
			Disabled:   credential.Disabled,
			PlanType:   credential.PlanType,
			Reason:     "keep current state",
		}
		evidence, ok := evidenceByAuthIndex[credential.AuthIndex]
		if ok {
			if isXAIAuthInvalid(credential, evidence) {
				item.EvidenceFresh = true
				item.Priority = -1
				item.Disabled = true
				item.Reason = "xai auth invalid"
			} else if isCodexFreeDepleted(credential, evidence) {
				item.PlanType = evidence.PlanType
				item.ResetAt = evidence.ResetAt
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
				item.Priority = codexFreeDepletedPriority(options)
				item.Disabled = credential.Disabled || codexFreeDepletedDisabled(options)
				item.Reason = "fresh remaining depleted"
			} else if isCodexPaidDepleted(credential, evidence) {
				item.PlanType = evidence.PlanType
				item.ResetAt = evidence.ResetAt
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
				item.Priority = codexFreeDepletedPriority(options)
				item.Disabled = credential.Disabled || codexPaidDepletedDisabled(options)
				item.Reason = "fresh paid remaining depleted"
			} else if isClaudeFreeDepleted(credential, evidence) {
				item.PlanType = evidence.PlanType
				item.ResetAt = evidence.ResetAt
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
				item.Priority = claudeFreeDepletedPriority(options)
				item.Disabled = credential.Disabled || claudeFreeDepletedDisabled(options)
				item.Reason = "fresh remaining depleted"
			} else if isClaudePaidDepleted(credential, evidence) {
				item.PlanType = evidence.PlanType
				item.ResetAt = evidence.ResetAt
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
				item.Priority = claudeFreeDepletedPriority(options)
				item.Disabled = credential.Disabled || claudePaidDepletedDisabled(options)
				item.Reason = "fresh paid remaining depleted"
			} else if isXAIFreeCooldown(credential, evidence, options) {
				item.PlanType = evidence.PlanType
				if item.PlanType == core.PlanTypeUnknown && evidence.XAIPlanClass == "free" {
					item.PlanType = core.PlanTypeFree
				}
				item.ResetAt = evidence.ResetAt
				if evidence.XAINextEligibleAt != nil {
					item.ResetAt = evidence.XAINextEligibleAt
				}
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
				item.Priority = xaiFreeDepletedPriority(options)
				// Soft disable default: free_depleted_disabled=false clears host hard-disable.
				item.Disabled = xaiFreeDepletedDisabled(options)
				item.Reason = "fresh remaining depleted"
			} else if isXAIMonthlyAndWeeklyDepleted(credential, evidence) {
				item.PlanType = evidence.PlanType
				item.ResetAt = evidence.ResetAt
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
				item.Priority = xaiMonthlyAndWeeklyDepletedPriority(options)
				item.Disabled = credential.Disabled || xaiMonthlyAndWeeklyDepletedDisabled(options)
				item.Reason = "fresh monthly and weekly depleted"
			} else if isXAIWeeklyDepleted(credential, evidence) {
				item.PlanType = evidence.PlanType
				item.ResetAt = evidence.ResetAt
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
				item.Priority = xaiWeeklyDepletedPriority(options)
				// 仅周限额耗尽：降优先级，不禁用。
				// 不得继承宿主 free_depleted 的 Disabled=true，否则 free 刷新后若再 probe 到 weekly 会永久锁死。
				item.Disabled = false
				item.Reason = "fresh weekly depleted"
			} else if isAntigravityWeeklyDepleted(credential, evidence) {
				item.PlanType = evidence.PlanType
				item.ResetAt = evidence.ResetAt
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
				item.Priority = -1
				item.Disabled = true
				item.Reason = "fresh remaining depleted"
			} else if evidence.Remaining != nil && evidence.ResetAt != nil {
				item.PlanType = evidence.PlanType
				item.ResetAt = evidence.ResetAt
				item.Remaining = evidence.Remaining
				item.LongWindowResetAt = evidence.LongWindowResetAt
				item.EvidenceFresh = true
			}
		}
		items[index] = item
	}
	return items
}

func isCodexFreeDepleted(credential core.Credential, evidence ProbeEvidence) bool {
	return planItemProvider(PlanItem{Credential: credential}) == core.ProviderCodex &&
		evidence.PlanType == core.PlanTypeFree &&
		evidence.Remaining != nil &&
		*evidence.Remaining <= 0
}

func isCodexPaidDepleted(credential core.Credential, evidence ProbeEvidence) bool {
	return planItemProvider(PlanItem{Credential: credential}) == core.ProviderCodex &&
		paidRank(evidence.PlanType) > 0 &&
		evidence.Remaining != nil &&
		*evidence.Remaining <= 0
}

func isClaudeFreeDepleted(credential core.Credential, evidence ProbeEvidence) bool {
	return planItemProvider(PlanItem{Credential: credential}) == core.ProviderClaude &&
		evidence.PlanType == core.PlanTypeFree &&
		evidence.Remaining != nil &&
		*evidence.Remaining <= 0
}

func isClaudePaidDepleted(credential core.Credential, evidence ProbeEvidence) bool {
	return planItemProvider(PlanItem{Credential: credential}) == core.ProviderClaude &&
		paidRank(evidence.PlanType) > 0 &&
		evidence.Remaining != nil &&
		*evidence.Remaining <= 0
}

func isAntigravityWeeklyDepleted(credential core.Credential, evidence ProbeEvidence) bool {
	return planItemProvider(PlanItem{Credential: credential}) == core.ProviderAntigravity &&
		evidence.Remaining != nil &&
		*evidence.Remaining <= 0
}

func isXAIAuthInvalid(credential core.Credential, evidence ProbeEvidence) bool {
	return isXAICredential(credential) && evidence.Status == EvidenceStatusAuthInvalid
}

func isFreeOrUnknownPlan(planType core.PlanType) bool {
	return planType == core.PlanTypeFree || planType == core.PlanTypeUnknown
}

func codexFreeDepletedPriority(options Options) int {
	if options.CodexFreeDepletedPriority == nil {
		return -1
	}
	return *options.CodexFreeDepletedPriority
}

func codexFreeDepletedDisabled(options Options) bool {
	if options.CodexFreeDepletedDisabled == nil {
		return true
	}
	return *options.CodexFreeDepletedDisabled
}

func codexPaidDepletedDisabled(options Options) bool {
	if options.CodexPaidDepletedDisabled == nil {
		return false
	}
	return *options.CodexPaidDepletedDisabled
}

func claudeFreeDepletedPriority(options Options) int {
	if options.ClaudeFreeDepletedPriority == nil {
		return -1
	}
	return *options.ClaudeFreeDepletedPriority
}

func claudeFreeDepletedDisabled(options Options) bool {
	if options.ClaudeFreeDepletedDisabled == nil {
		return true
	}
	return *options.ClaudeFreeDepletedDisabled
}

func claudePaidDepletedDisabled(options Options) bool {
	if options.ClaudePaidDepletedDisabled == nil {
		return false
	}
	return *options.ClaudePaidDepletedDisabled
}

func isXAICredential(credential core.Credential) bool {
	return planItemProvider(PlanItem{Credential: credential}) == core.ProviderXAI
}

func isXAIFreeDepleted(credential core.Credential, evidence ProbeEvidence) bool {
	return isXAICredential(credential) &&
		(evidence.XAIDepletedKind == "free" || (evidence.PlanType == core.PlanTypeFree && evidence.Remaining != nil && *evidence.Remaining <= 0 && evidence.XAIDepletedKind == ""))
}

// isXAIFreeCooldown: free path only uses consecutive-fail + 24h cooldown (not weekly/monthly).
func isXAIFreeCooldown(credential core.Credential, evidence ProbeEvidence, options Options) bool {
	if !isXAICredential(credential) {
		return false
	}
	if evidence.XAIDepletedKind == "weekly" || evidence.XAIDepletedKind == "monthly_and_weekly" {
		return false
	}
	if !isXAIFreeDepleted(credential, evidence) && evidence.XAIQuotaFailCount < 3 {
		return false
	}
	// Cooldown active when next_eligible is in the future, or depleted with remaining<=0.
	if evidence.XAINextEligibleAt != nil && options.Now.Before(*evidence.XAINextEligibleAt) {
		return true
	}
	if evidence.XAINextEligibleAt == nil && isXAIFreeDepleted(credential, evidence) {
		return true
	}
	return false
}

func isXAIWeeklyDepleted(credential core.Credential, evidence ProbeEvidence) bool {
	return isXAICredential(credential) && evidence.XAIDepletedKind == "weekly"
}

func isXAIMonthlyAndWeeklyDepleted(credential core.Credential, evidence ProbeEvidence) bool {
	return isXAICredential(credential) && evidence.XAIDepletedKind == "monthly_and_weekly"
}

func xaiFreeDepletedPriority(options Options) int {
	if options.XAIFreeDepletedPriority == nil {
		return -1
	}
	return *options.XAIFreeDepletedPriority
}

func xaiFreeDepletedDisabled(options Options) bool {
	// 默认软禁用：nil 回退 false，仅降 priority；yaml 显式 true 仍硬禁用。
	if options.XAIFreeDepletedDisabled == nil {
		return false
	}
	return *options.XAIFreeDepletedDisabled
}

// xaiFreeParticipatesPriority 默认 false：free 不参与正优先级提升与 free-first；显式 true 才 opt-in。
func xaiFreeParticipatesPriority(options Options) bool {
	if options.XAIFreeParticipatesPriority == nil {
		return false
	}
	return *options.XAIFreeParticipatesPriority
}

// isXAIFreePlanItem 识别 xAI free/unknown 套餐（不含 paid）。
func isXAIFreePlanItem(item PlanItem) bool {
	if planItemProvider(item) != core.ProviderXAI {
		return false
	}
	return item.PlanType == core.PlanTypeFree || item.PlanType == core.PlanTypeUnknown
}

func xaiWeeklyDepletedPriority(options Options) int {
	if options.XAIWeeklyDepletedPriority == nil {
		return -1
	}
	return *options.XAIWeeklyDepletedPriority
}

func xaiMonthlyAndWeeklyDepletedPriority(options Options) int {
	if options.XAIMonthlyAndWeeklyDepletedPriority == nil {
		return -1
	}
	return *options.XAIMonthlyAndWeeklyDepletedPriority
}

func xaiMonthlyAndWeeklyDepletedDisabled(options Options) bool {
	if options.XAIMonthlyAndWeeklyDepletedDisabled == nil {
		return true
	}
	return *options.XAIMonthlyAndWeeklyDepletedDisabled
}

func planFreshPositive(items []PlanItem, options Options) {
	candidates := positiveCandidates(items, options)
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
// 不改写 disabled 或 priority<=0（含 depleted -1）的凭证。
func ensureUniquePriorities(items []PlanItem, options Options) {
	group := make([]int, 0, len(items))
	for index, item := range items {
		if item.Disabled || item.Priority < 1 {
			continue
		}
		// free_participates_priority=false：xAI free 不参与 uniqueness 重排。
		if !xaiFreeParticipatesPriority(options) && isXAIFreePlanItem(item) {
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

func capExcludedXAIFreePriorities(items []PlanItem, options Options) {
	if xaiFreeParticipatesPriority(options) {
		return
	}
	for index := range items {
		item := &items[index]
		if item.Disabled || item.Priority <= maxEnabledPriority || !isXAIFreePlanItem(*item) {
			continue
		}
		item.Priority = maxEnabledPriority
		if !item.EvidenceFresh {
			item.ForceWrite = true
			item.Reason = "xai free priority cap"
		} else if item.Reason == "keep current state" || item.Reason == "" {
			item.Reason = "xai free priority cap"
		}
	}
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

func positiveCandidates(items []PlanItem, options Options) []int {
	candidates := make([]int, 0, len(items))
	for index, item := range items {
		if !item.EvidenceFresh || item.Remaining == nil {
			continue
		}
		// free_participates_priority=false：xAI free 不进正优先级提升。
		if !xaiFreeParticipatesPriority(options) && isXAIFreePlanItem(item) {
			continue
		}
		if *item.Remaining > 0 {
			candidates = append(candidates, index)
		}
	}
	return candidates
}

func compareCandidates(left PlanItem, right PlanItem, options Options) int {
	// xAI: free eligible ranks above paid; then pacing score, remaining, reset, AuthIndex.
	if planItemProvider(left) == core.ProviderXAI || planItemProvider(right) == core.ProviderXAI {
		leftFree := isXAIFreeEligibleItem(left, options)
		rightFree := isXAIFreeEligibleItem(right, options)
		switch {
		case leftFree && !rightFree:
			return -1
		case rightFree && !leftFree:
			return 1
		}
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
	if options.PaidFirst && paidRank(left.PlanType) != paidRank(right.PlanType) {
		return paidRank(right.PlanType) - paidRank(left.PlanType)
	}
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

// pacingScore 计算凭据的额度消耗健康度得分（Pacing / Burn Rate Ratio）。
// Score = (Remaining Quota %) / (Remaining Time %)
// 得分越高表示当前额度越富余、或重置时间越临近，应拥有越高优先级。
func pacingScore(item PlanItem, now time.Time) float64 {
	if item.Remaining == nil || *item.Remaining <= 0 {
		return 0
	}
	remainingRatio := float64(*item.Remaining) / 100.0

	// 确定重置时间与所属周期总长度
	// 优先以周窗口（LongWindowResetAt）为基准；若无则退回短窗口/日窗口（ResetAt）
	var resetAt *time.Time
	var totalWindow time.Duration

	if item.LongWindowResetAt != nil {
		resetAt = item.LongWindowResetAt
		totalWindow = 7 * 24 * time.Hour
	} else if item.ResetAt != nil {
		resetAt = item.ResetAt
		timeRemaining := resetAt.Sub(now)
		if timeRemaining > 48*time.Hour {
			totalWindow = 7 * 24 * time.Hour
		} else if timeRemaining > 6*time.Hour {
			totalWindow = 24 * time.Hour
		} else {
			totalWindow = 5 * time.Hour
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

// isXAIFreeEligibleItem: free plan with positive remaining (or unknown remaining) ranks high.
// free_participates_priority=false 时永不视为 free-first 候选。
func isXAIFreeEligibleItem(item PlanItem, options Options) bool {
	if !xaiFreeParticipatesPriority(options) {
		return false
	}
	if planItemProvider(item) != core.ProviderXAI {
		return false
	}
	if item.PlanType != core.PlanTypeFree && item.PlanType != core.PlanTypeUnknown {
		return false
	}
	if item.Remaining != nil && *item.Remaining <= 0 {
		return false
	}
	return true
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
