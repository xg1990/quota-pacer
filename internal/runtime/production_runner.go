package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"time"

	"quota-pacer/internal/apply"
	"quota-pacer/internal/config"
	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
	"quota-pacer/internal/priority"
	"quota-pacer/internal/schedule"
	"quota-pacer/internal/state"
)

var errMissingHostCallbacks = errors.New("runtime: host callbacks are required")

const (
	autoQuotaProbeAttempts = 3
	autoQuotaProbeDelay    = 10 * time.Second
	// defaultProbeCacheTTL 是非 xAI 探测证据 freshness（包内常量，不可配置）。
	defaultProbeCacheTTL = 15 * time.Minute
)

func (r *Runtime) runProductionTask(ctx context.Context, request TaskRequest) error {
	if r.hostCallbacks == nil {
		return errMissingHostCallbacks
	}
	now := r.clock.Now().UTC()
	client := host.NewClient(r.hostCallbacks)
	files, err := client.ListAuthFiles(ctx)
	if err != nil {
		return err
	}
	credentials, accountIDs := credentialsFromAuthFiles(files)
	credentials = filterCredentialsByProvider(credentials, request.Config)
	credentials = filterCredentialsByAuthIndex(credentials, request.AuthIndexes)
	credentials, authMaterials, err := enrichCredentialsFromAuthDocuments(ctx, client, credentials)
	if err != nil {
		return err
	}
	store, err := state.Load(ctx, config.DefaultStateCachePath)
	if err != nil {
		return err
	}
	probes, err := probesForRequest(ctx, store, credentials, scheduleOptions(request.Config, now), request.AuthIndexes, request.Config.AntigravityModelGroup, defaultProbeCacheTTL, request.Trigger)
	if err != nil {
		return err
	}
	evidence, err := r.collectEvidenceForTrigger(ctx, collectInput{client: client, store: store, probes: probes, accountIDs: accountIDs, authMaterials: authMaterials, now: now, cacheTTL: defaultProbeCacheTTL, forceProbe: request.Trigger == TriggerManualApply, maxConcurrency: request.Config.MaxConcurrency, antigravityModelGroup: request.Config.AntigravityModelGroup}, request.Trigger)
	if err != nil {
		return err
	}
	if err := store.SaveAtomic(ctx); err != nil {
		return err
	}
	evidence = attachCachedEvidence(credentials, evidence, store, request.Config.AntigravityModelGroup, now)
	plan := priority.PlanFreshOnly(credentials, evidence, priorityOptions(request.Config, now))
	plan = preserveProbeFailureState(plan, evidence)
	if request.Trigger == TriggerManual {
		result := apply.Result{Snapshot: apply.Snapshot(plan)}
		providerEntries := runHistoryProvidersFromResult(result)
		r.snapshotRunEntry(result, "dry-run plan generated", RunHistoryEntry{
			Kind:      "dry_run",
			Trigger:   string(request.Trigger),
			Attempted: result.Attempted,
			Succeeded: result.Succeeded,
			Failed:    result.Failed,
			Skipped:   result.Skipped,
			Providers: providerEntries,
			Message:   "dry-run plan generated",
		})
		return nil
	}
	result, err := apply.Apply(ctx, apply.Request{Host: client, Auditor: r, Plan: plan, ReportSkippedPlan: true})
	if err != nil {
		return err
	}
	providerEntries := runHistoryProvidersFromResult(result)
	summary := fmt.Sprintf("apply credentials=%d succeeded=%d failed=%d skipped=%d", result.Attempted+result.Skipped, result.Succeeded, result.Failed, result.Skipped)
	r.snapshotRunEntry(result, summary, RunHistoryEntry{
		Kind:      "apply",
		Trigger:   string(request.Trigger),
		Attempted: result.Attempted,
		Succeeded: result.Succeeded,
		Failed:    result.Failed,
		Skipped:   result.Skipped,
		Providers: providerEntries,
		Message:   summary,
	})
	return nil
}

// runAutoParallelProviders 在一轮自动任务内按 provider 并行探测与写回。
// 共享同一 state.Store，结束后统一 SaveAtomic，避免并行 Load/Save 互相覆盖。
// 调用方必须已持有 runMu（AutoApply 入口）。
func (r *Runtime) runAutoParallelProviders(ctx context.Context, request TaskRequest) error {
	if r.hostCallbacks == nil {
		return errMissingHostCallbacks
	}
	now := r.clock.Now().UTC()
	client := host.NewClient(r.hostCallbacks)
	files, err := client.ListAuthFiles(ctx)
	if err != nil {
		return err
	}
	allCredentials, accountIDs := credentialsFromAuthFiles(files)
	allCredentials = filterCredentialsByProvider(allCredentials, request.Config)
	allCredentials = filterCredentialsByAuthIndex(allCredentials, request.AuthIndexes)
	allCredentials, authMaterials, err := enrichCredentialsFromAuthDocuments(ctx, client, allCredentials)
	if err != nil {
		return err
	}
	store, err := state.Load(ctx, config.DefaultStateCachePath)
	if err != nil {
		return err
	}

	providers := autoProvidersFromCredentials(allCredentials, request.Config)
	if len(providers) == 0 {
		r.snapshotRunEntry(apply.Result{}, "auto_apply no supported providers", RunHistoryEntry{
			Kind:    "auto_apply",
			Trigger: string(TriggerAutoApply),
			Message: "auto_apply no supported providers",
		})
		return nil
	}

	type providerEvidenceResult struct {
		provider core.Provider
		evidence []priority.ProbeEvidence
		err      error
	}
	evidenceChan := make(chan providerEvidenceResult, len(providers))
	var wg sync.WaitGroup
	for _, providerName := range providers {
		providerName := providerName
		wg.Add(1)
		go func() {
			defer wg.Done()
			provider := core.Provider(providerName)
			credentials := filterCredentialsToProvider(allCredentials, provider)
			if len(credentials) == 0 {
				evidenceChan <- providerEvidenceResult{provider: provider}
				return
			}
			probes, err := probesForRequest(ctx, store, credentials, scheduleOptions(request.Config, now), request.AuthIndexes, request.Config.AntigravityModelGroup, defaultProbeCacheTTL, TriggerAutoApply)
			if err != nil {
				evidenceChan <- providerEvidenceResult{provider: provider, err: err}
				return
			}
			evidence, err := r.collectEvidenceForTrigger(ctx, collectInput{
				client: client, store: store, probes: probes, accountIDs: accountIDs, authMaterials: authMaterials,
				now: now, cacheTTL: defaultProbeCacheTTL, forceProbe: false,
				maxConcurrency: request.Config.MaxConcurrency, antigravityModelGroup: request.Config.AntigravityModelGroup,
			}, TriggerAutoApply)
			evidenceChan <- providerEvidenceResult{provider: provider, evidence: evidence, err: err}
		}()
	}
	wg.Wait()
	close(evidenceChan)

	var (
		firstErr    error
		allEvidence []priority.ProbeEvidence
	)
	probeErrors := make(map[core.Provider]string)
	for item := range evidenceChan {
		if item.err != nil {
			probeErrors[item.provider] = item.err.Error()
			if firstErr == nil {
				firstErr = fmt.Errorf("%s probe: %w", item.provider, item.err)
			}
		}
		if len(item.evidence) > 0 {
			allEvidence = append(allEvidence, item.evidence...)
		}
	}

	allEvidence = attachCachedEvidence(allCredentials, allEvidence, store, request.Config.AntigravityModelGroup, now)
	plan := priority.PlanFreshOnly(allCredentials, allEvidence, priorityOptions(request.Config, now))
	plan = preserveProbeFailureState(plan, allEvidence)
	result, applyErr := apply.Apply(ctx, apply.Request{Host: client, Auditor: r, Plan: plan, ReportSkippedPlan: true})
	if applyErr != nil && firstErr == nil {
		firstErr = applyErr
	}

	providerEntries := runHistoryProvidersFromResult(result)
	for i := range providerEntries {
		if errText, ok := probeErrors[core.Provider(providerEntries[i].Name)]; ok {
			if providerEntries[i].Error == "" {
				providerEntries[i].Error = errText
			}
		}
	}

	parts := make([]string, 0, len(providerEntries))
	for _, entry := range providerEntries {
		parts = append(parts, fmt.Sprintf("%s attempted=%d succeeded=%d failed=%d skipped=%d", entry.Name, entry.Attempted, entry.Succeeded, entry.Failed, entry.Skipped))
	}
	summary := "auto_apply: " + strings.Join(parts, "; ")
	if len(parts) == 0 {
		summary = fmt.Sprintf("auto_apply credentials=%d attempted=%d succeeded=%d failed=%d skipped=%d", len(allCredentials), result.Attempted, result.Succeeded, result.Failed, result.Skipped)
	}

	// 先写 history，再 SaveAtomic：避免 SaveAtomic/ctx 失败导致「无记录却算跑过」。
	r.snapshotRunEntry(result, summary, RunHistoryEntry{
		Kind:      "auto_apply",
		Trigger:   string(TriggerAutoApply),
		Attempted: result.Attempted,
		Succeeded: result.Succeeded,
		Failed:    result.Failed,
		Skipped:   result.Skipped,
		Providers: providerEntries,
		Message:   summary,
	})
	if err := store.SaveAtomic(ctx); err != nil {
		return err
	}
	return firstErr
}

// runHistoryProvidersFromResult 将 apply 结果按提供商分桶，供执行记录 UI 与自动排序对齐展示。
func runHistoryProvidersFromResult(result apply.Result) []RunHistoryProvider {
	order := []string{string(core.ProviderAntigravity), string(core.ProviderCodex), string(core.ProviderClaude), string(core.ProviderXAI)}
	buckets := make(map[string]*RunHistoryProvider, len(order))
	for _, name := range order {
		buckets[name] = &RunHistoryProvider{Name: name}
	}
	for _, change := range result.Changes {
		name := strings.ToLower(strings.TrimSpace(change.Provider))
		if name == "" {
			continue
		}
		bucket, ok := buckets[name]
		if !ok {
			bucket = &RunHistoryProvider{Name: name}
			buckets[name] = bucket
			order = append(order, name)
		}
		switch change.Status {
		case apply.ChangeStatusSuccess:
			bucket.Attempted++
			bucket.Succeeded++
		case apply.ChangeStatusFailed:
			bucket.Attempted++
			bucket.Failed++
		case apply.ChangeStatusSkipped:
			bucket.Skipped++
		}
	}
	out := make([]RunHistoryProvider, 0, len(order))
	for _, name := range order {
		bucket := buckets[name]
		if bucket == nil {
			continue
		}
		if bucket.Attempted == 0 && bucket.Succeeded == 0 && bucket.Failed == 0 && bucket.Skipped == 0 {
			continue
		}
		out = append(out, *bucket)
	}
	return out
}

func autoProvidersFromCredentials(credentials []core.Credential, cfg config.Config) []string {
	order := []string{string(core.ProviderAntigravity), string(core.ProviderCodex), string(core.ProviderClaude), string(core.ProviderXAI)}
	present := map[string]struct{}{}
	for _, credential := range credentials {
		p := filterProvider(credential)
		if p == core.ProviderAntigravity || p == core.ProviderCodex || p == core.ProviderClaude || p == core.ProviderXAI {
			present[string(p)] = struct{}{}
		}
	}
	selectedFilter := map[string]struct{}{}
	if cfg.ProviderScope == config.ProviderScopeSelected && len(cfg.SelectedProviders) > 0 {
		for _, provider := range cfg.SelectedProviders {
			selectedFilter[provider] = struct{}{}
		}
	}
	result := make([]string, 0, len(present))
	for _, provider := range order {
		if _, ok := present[provider]; !ok {
			continue
		}
		if len(selectedFilter) > 0 {
			if _, ok := selectedFilter[provider]; !ok {
				continue
			}
		}
		result = append(result, provider)
	}
	return result
}

func filterCredentialsToProvider(credentials []core.Credential, provider core.Provider) []core.Credential {
	filtered := make([]core.Credential, 0, len(credentials))
	for _, credential := range credentials {
		if filterProvider(credential) == provider {
			filtered = append(filtered, credential)
		}
	}
	return filtered
}

func (r *Runtime) collectEvidenceForTrigger(ctx context.Context, input collectInput, trigger Trigger) ([]priority.ProbeEvidence, error) {
	if trigger != TriggerAutoApply {
		return collectFreshEvidence(ctx, input)
	}
	var evidence []priority.ProbeEvidence
	for attempt := 1; attempt <= autoQuotaProbeAttempts; attempt++ {
		current, err := collectFreshEvidence(ctx, input)
		if err != nil {
			return nil, err
		}
		evidence = current
		if !hasProbeFailure(current) || attempt == autoQuotaProbeAttempts {
			return evidence, nil
		}
		input.forceProbe = true
		if err := r.sleeper.Sleep(ctx, autoQuotaProbeDelay); err != nil {
			return nil, err
		}
	}
	return evidence, nil
}

func hasProbeFailure(evidence []priority.ProbeEvidence) bool {
	return slices.ContainsFunc(evidence, func(item priority.ProbeEvidence) bool {
		return item.Status == priority.EvidenceStatusProbeFailed
	})
}

func preserveProbeFailureState(plan priority.Plan, evidence []priority.ProbeEvidence) priority.Plan {
	failedAuthIndexes := make(map[string]struct{})
	for _, item := range evidence {
		if item.Status == priority.EvidenceStatusProbeFailed {
			failedAuthIndexes[item.AuthIndex] = struct{}{}
		}
	}
	if len(failedAuthIndexes) == 0 {
		return plan
	}

	for index := range plan.Items {
		item := &plan.Items[index]
		if _, failed := failedAuthIndexes[item.Credential.AuthIndex]; !failed {
			continue
		}
		item.Priority = item.Credential.Priority
		item.Disabled = item.Credential.Disabled
		item.EvidenceFresh = false
		item.ForceWrite = false
		item.Reason = "failedQuotaFetch"
	}

	changes := plan.Changes[:0]
	for _, change := range plan.Changes {
		if _, failed := failedAuthIndexes[change.Credential.AuthIndex]; !failed {
			changes = append(changes, change)
		}
	}
	plan.Changes = changes
	return plan
}

func probesForRequest(ctx context.Context, store *state.Store, credentials []core.Credential, options schedule.Options, authIndexes []string, modelGroup config.AntigravityModelGroup, cacheTTL time.Duration, trigger Trigger) ([]schedule.Probe, error) {
	if trigger == TriggerManual || trigger == TriggerManualApply {
		return probesAtCurrentTime(credentials, options.Clock.Now()), nil
	}
	if len(authIndexes) == 0 {
		probePlan, err := schedule.PlanProbeSchedule(credentials, options)
		if err != nil {
			return nil, err
		}
		return dueProbes(ctx, store, probePlan, options.Clock.Now(), modelGroup, cacheTTL)
	}
	return probesAtCurrentTime(credentials, options.Clock.Now()), nil
}

func dueProbes(ctx context.Context, store *state.Store, plan schedule.Plan, now time.Time, modelGroup config.AntigravityModelGroup, cacheTTL time.Duration) ([]schedule.Probe, error) {
	result := make([]schedule.Probe, 0, len(plan.Immediate))
	for _, probe := range plan.Immediate {
		provider := filterProvider(probe.Credential)
		groupName := probeModelGroup(provider, modelGroup)
		needsProbe, err := store.NeedsProbe(ctx, state.ProbeCheck{AuthIndex: probe.Credential.AuthIndex, Provider: provider, ModelGroup: groupName, Now: now, Policy: probePolicyForProvider(provider, cacheTTL)})
		if err != nil {
			return nil, err
		}
		if needsProbe {
			result = append(result, probe)
		}
	}
	for _, group := range append(plan.ActiveGroups, plan.DisabledGroups...) {
		for _, probe := range group.Probes {
			provider := filterProvider(probe.Credential)
			groupName := probeModelGroup(provider, modelGroup)
			if !probe.NextProbeAt.After(now) {
				needsProbe, err := store.NeedsProbe(ctx, state.ProbeCheck{AuthIndex: probe.Credential.AuthIndex, Provider: provider, ModelGroup: groupName, Now: now, Policy: probePolicyForProvider(provider, cacheTTL)})
				if err != nil {
					return nil, err
				}
				if needsProbe {
					result = append(result, probe)
				}
				continue
			}
			if store.HasEntry(probe.Credential.AuthIndex, groupName) {
				needsProbe, err := store.NeedsProbe(ctx, state.ProbeCheck{AuthIndex: probe.Credential.AuthIndex, Provider: provider, ModelGroup: groupName, Now: now, Policy: probePolicyForProvider(provider, cacheTTL)})
				if err != nil {
					return nil, err
				}
				if needsProbe {
					result = append(result, schedule.Probe{Credential: probe.Credential, NextProbeAt: now})
				}
				continue
			}
			if err := store.MarkProbeScheduled(ctx, state.ProbeSchedule{AuthIndex: probe.Credential.AuthIndex, Provider: provider, ModelGroup: groupName, NextProbeAt: probe.NextProbeAt}); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func filterCredentialsByAuthIndex(credentials []core.Credential, authIndexes []string) []core.Credential {
	if len(authIndexes) == 0 {
		return credentials
	}
	allowed := make(map[string]struct{}, len(authIndexes))
	for _, authIndex := range authIndexes {
		allowed[authIndex] = struct{}{}
	}
	filtered := make([]core.Credential, 0, len(credentials))
	for _, credential := range credentials {
		if _, ok := allowed[credential.AuthIndex]; ok {
			filtered = append(filtered, credential)
		}
	}
	return filtered
}

func probesAtCurrentTime(credentials []core.Credential, now time.Time) []schedule.Probe {
	probes := make([]schedule.Probe, len(credentials))
	for index, credential := range credentials {
		probes[index] = schedule.Probe{Credential: credential, NextProbeAt: now}
	}
	return probes
}

func filterCredentialsByProvider(credentials []core.Credential, cfg config.Config) []core.Credential {
	if cfg.ProviderScope != config.ProviderScopeSelected || len(cfg.SelectedProviders) == 0 {
		filtered := make([]core.Credential, 0, len(credentials))
		for _, credential := range credentials {
			p := filterProvider(credential)
			if p == core.ProviderAntigravity || p == core.ProviderCodex || p == core.ProviderClaude || p == core.ProviderXAI {
				filtered = append(filtered, credential)
			}
		}
		return filtered
	}
	selected := make(map[core.Provider]struct{}, len(cfg.SelectedProviders))
	for _, provider := range cfg.SelectedProviders {
		selected[core.Provider(provider)] = struct{}{}
	}
	filtered := make([]core.Credential, 0, len(credentials))
	for _, credential := range credentials {
		if _, ok := selected[filterProvider(credential)]; ok {
			filtered = append(filtered, credential)
		}
	}
	return filtered
}

func filterProvider(credential core.Credential) core.Provider {
	if credential.Provider != "" {
		return credential.Provider
	}
	switch credential.Type {
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

func credentialsFromAuthFiles(files []host.AuthFile) ([]core.Credential, map[string]string) {
	credentials := make([]core.Credential, len(files))
	accountIDs := make(map[string]string, len(files))
	for index, file := range files {
		credentials[index] = core.Credential{Name: file.Name, AuthIndex: file.AuthIndex, Provider: core.Provider(file.Provider), Type: core.CredentialType(file.Type), Status: core.CredentialStatus(file.Status), Disabled: file.Disabled, Unavailable: file.Unavailable, Priority: file.Priority, PriorityMissing: file.PriorityMissing, Account: file.Account, Email: file.Email, PlanType: core.PlanType(file.IDToken.PlanType), RawJSON: append([]byte(nil), file.RawJSON...)}
		accountIDs[file.AuthIndex] = file.IDToken.ChatGPTAccountID
	}
	return credentials, accountIDs
}

func scheduleOptions(cfg config.Config, now time.Time) schedule.Options {
	// disabled 分批改用 Interval + ActiveGroupSize；不再传入有效 DisabledProbeInterval。
	return schedule.Options{
		Clock:                 fixedClock{now: now},
		RNG:                   realRNG{},
		ImmediateProbeLimit:   cfg.ImmediateProbeLimit,
		TopPriorityProbeCount: cfg.TopPriorityProbeCount,
		ActiveGroupSize:       cfg.ActiveGroupSize,
		ActiveGroupJitter:     cfg.ActiveGroupJitter,
		Interval:              cfg.Interval,
	}
}

func priorityOptions(cfg config.Config, now time.Time) priority.Options {
	return priority.Options{Now: now, MaxPriority: 100, MinChange: cfg.MinChange}
}

func probePolicy(cacheTTL time.Duration) state.ProbePolicy {
	return state.ProbePolicy{TTL: cacheTTL, ResetStaleAfter: time.Hour}
}

// probePolicyForProvider：xAI 使用 24h TTL，避免默认 15m 覆盖 NextProbeAt 导致狂探。
// 其它 provider 使用包内常量 defaultProbeCacheTTL（15m）。
func probePolicyForProvider(provider core.Provider, cacheTTL time.Duration) state.ProbePolicy {
	if provider == core.ProviderXAI {
		return state.ProbePolicy{TTL: xaiPositiveProbeInterval, ResetStaleAfter: time.Hour}
	}
	return probePolicy(cacheTTL)
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

type zeroRNG struct{}

func (zeroRNG) Int63n(int64) int64 {
	return 0
}

type realRNG struct{}

func (realRNG) Int63n(limit int64) int64 {
	return rand.Int63n(limit)
}

func (r *Runtime) SaveSnapshot(ctx context.Context, snapshot apply.PlanSnapshot) error {
	return ctx.Err()
}

func (r *Runtime) RecordEvent(ctx context.Context, event apply.AuditEvent) error {
	return ctx.Err()
}

var _ apply.Auditor = (*Runtime)(nil)

func attachCachedEvidence(credentials []core.Credential, evidence []priority.ProbeEvidence, store *state.Store, modelGroup config.AntigravityModelGroup, now time.Time) []priority.ProbeEvidence {
	if store == nil {
		return evidence
	}
	hasEvidence := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		hasEvidence[item.AuthIndex] = struct{}{}
	}

	merged := make([]priority.ProbeEvidence, len(evidence), len(credentials))
	copy(merged, evidence)

	for _, credential := range credentials {
		if _, exists := hasEvidence[credential.AuthIndex]; exists {
			continue
		}
		provider := filterProvider(credential)
		groupName := probeModelGroup(provider, modelGroup)
		entry, ok := store.ValidEntry(credential.AuthIndex, groupName, now, probePolicyForProvider(provider, defaultProbeCacheTTL))
		if !ok {
			continue
		}

		rem := int64(entry.Remaining)
		var resetAt *time.Time
		if !entry.ResetAt.IsZero() {
			t := entry.ResetAt.UTC()
			resetAt = &t
		}
		var longWindowResetAt *time.Time
		if !entry.LongWindowResetAt.IsZero() {
			t := entry.LongWindowResetAt.UTC()
			longWindowResetAt = &t
		}
		var shortWindowResetAt *time.Time
		if !entry.ShortWindowResetAt.IsZero() {
			t := entry.ShortWindowResetAt.UTC()
			shortWindowResetAt = &t
		}
		planType := entry.PlanType
		if planType == "" || planType == core.PlanTypeUnknown {
			if credential.PlanType != "" && credential.PlanType != core.PlanTypeUnknown {
				planType = credential.PlanType
			} else if entry.PlanClass != "" {
				planType = xaiPlanTypeFromClass(entry.PlanClass)
			}
		}

		merged = append(merged, priority.ProbeEvidence{
			Provider:             provider,
			AuthIndex:            credential.AuthIndex,
			ObservedAt:           entry.ObservedAt,
			ResetAt:              resetAt,
			Remaining:            &rem,
			LongWindowResetAt:    longWindowResetAt,
			ShortWindowRemaining: cloneInt64Ptr(entry.ShortWindowRemaining),
			ShortWindowResetAt:   shortWindowResetAt,
			LongWindowRemaining:  cloneInt64Ptr(entry.LongWindowRemaining),
			Windows:              cloneQuotaWindows(entry.Windows),
			Freshness:            core.FreshnessStale,
			ProbeStatus:          core.ProbeStatusReady,
			Status:               priority.EvidenceStatusReady,
			PlanType:             planType,
			EvidenceFresh:        false,
		})
	}
	return merged
}

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func cloneQuotaWindows(windows []core.QuotaWindow) []core.QuotaWindow {
	if len(windows) == 0 {
		return nil
	}
	res := make([]core.QuotaWindow, len(windows))
	for i, w := range windows {
		res[i] = core.QuotaWindow{
			Name:      w.Name,
			Duration:  w.Duration,
			Remaining: w.Remaining,
			ResetAt:   w.ResetAt.UTC(),
		}
	}
	return res
}
