package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// PluginID 是 CPA 宿主识别该插件的固定 ID。
	PluginID = "quota-pacer"
	// DirectoryName 是源码目录和插件目录约定名。
	DirectoryName = "quota-pacer"
	// DynamicLibraryBaseName 是构建动态库时不含平台扩展名的文件名。
	DynamicLibraryBaseName = "quota-pacer"
	// CPAConfigKey 是 `plugins.configs` 下该插件的配置键。
	CPAConfigKey = "quota-pacer"
	// DefaultStateCachePath 是探测状态缓存落盘路径（包内常量，不可配置）。
	DefaultStateCachePath = DirectoryName + "/refresh-cache.json"
)

// ErrInvalidConfig 标识配置解析或校验失败。
var ErrInvalidConfig = errors.New("config: invalid")

// Config 是插件自有配置的已校验形态。
//
// Enabled 为顶层插件开关。
// DisabledGroupSize / DisabledProbeInterval 仍可从旧配置解析进字段，但不再驱动探测调度
// （调度使用 Interval 与 ActiveGroupSize）。
// 探测缓存路径与 freshness TTL 为包内常量，不暴露为可配置字段。
type Config struct {
	Enabled               bool                        `json:"enabled"`
	AutoApply             bool                        `json:"auto_apply"`
	ProviderScope         ProviderScope               `json:"provider_scope"`
	SelectedProviders     []string                    `json:"selected_providers"`
	AntigravityModelGroup AntigravityModelGroup       `json:"antigravity_model_group"`
	Interval              time.Duration               `json:"interval"`
	ImmediateProbeLimit   int                         `json:"immediate_probe_limit"`
	MaxConcurrency        int                         `json:"max_concurrency"`
	MinChange             int                         `json:"min_change"`
	TopPriorityProbeCount int                         `json:"top_priority_probe_count"`
	ActiveGroupSize       int                         `json:"active_group_size"`
	ActiveGroupJitter     time.Duration               `json:"active_group_jitter"`
	// DisabledGroupSize 兼容旧键 disabled_group_size；不再驱动调度。
	DisabledGroupSize int `json:"disabled_group_size"`
	// DisabledProbeInterval 兼容旧键 disabled_probe_interval；不再驱动 1h 冷冻调度。
	DisabledProbeInterval time.Duration               `json:"disabled_probe_interval"`
	ProviderOverrides     map[string]ProviderOverride `json:"provider_overrides,omitempty"`
}

// ProviderOverride 是按 provider 覆盖的可选配置。
type ProviderOverride struct {
	Enabled        *bool         `json:"enabled,omitempty"`
	AutoApply      *bool         `json:"auto_apply,omitempty"`
	Interval       time.Duration `json:"interval,omitempty"`
	MaxConcurrency int           `json:"max_concurrency,omitempty"`
}

type rawConfig struct {
	Enabled               *bool                          `json:"enabled"`
	AutoApply             *bool                          `json:"auto_apply"`
	ProviderScope         *string                        `json:"provider_scope"`
	SelectedProviders     selectedProviderList           `json:"selected_providers"`
	AntigravityModelGroup *string                        `json:"antigravity_model_group"`
	Interval              *rawDuration                   `json:"interval"`
	ImmediateProbeLimit   *int                           `json:"immediate_probe_limit"`
	MaxConcurrency        *int                           `json:"max_concurrency"`
	MinChange             *int                           `json:"min_change"`
	TopPriorityProbeCount *int                           `json:"top_priority_probe_count"`
	ActiveGroupSize       *int                           `json:"active_group_size"`
	ActiveGroupJitter     *rawDuration                   `json:"active_group_jitter"`
	DisabledGroupSize     *int                           `json:"disabled_group_size"`
	DisabledProbeInterval *rawDuration                   `json:"disabled_probe_interval"`
	ProviderOverrides     map[string]rawProviderOverride `json:"provider_overrides"`
}

type rawProviderOverride struct {
	Enabled        *bool        `json:"enabled"`
	AutoApply      *bool        `json:"auto_apply"`
	Interval       *rawDuration `json:"interval"`
	MaxConcurrency *int         `json:"max_concurrency"`
}

type selectedProviderList struct {
	values []string
	set    bool
}

type rawDuration string

func (duration *rawDuration) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		return nil
	}
	var text string
	if len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
	} else {
		text = trimmed
	}
	*duration = rawDuration(text)
	return nil
}

func (list *selectedProviderList) UnmarshalJSON(data []byte) error {
	list.set = true
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		list.values = nil
		return nil
	}
	var values []string
	if err := json.Unmarshal(data, &values); err == nil {
		list.values = values
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		list.values = nil
		return nil
	}
	list.values = []string{value}
	return nil
}

// Default 返回稳定的插件配置默认值。
func Default() Config {
	return Config{
		Enabled:               true,
		AutoApply:             false,
		ProviderScope:         ProviderScopeAll,
		AntigravityModelGroup: AntigravityModelGroupGemini,
		Interval:              15 * time.Minute,
		ImmediateProbeLimit:   30,
		MaxConcurrency:        6,
		MinChange:             1,
		TopPriorityProbeCount: 10,
		ActiveGroupSize:       10,
		ActiveGroupJitter:     10 * time.Minute,
		// DisabledGroupSize / DisabledProbeInterval：保留字段以兼容旧配置解析；
		// 不再作为默认 1h 冷冻调度语义（调度改用 Interval + ActiveGroupSize）。
		DisabledGroupSize:     5,
		DisabledProbeInterval: 0,
	}
}

// LoadBytes 将 CPA 传入的插件配置字节解析为 Config。
func LoadBytes(data []byte) (Config, error) {
	raw, err := decodeRaw(data)
	if err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return raw.apply(Default())
}

func decodeRaw(data []byte) (rawConfig, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return rawConfig{}, nil
	}
	var raw rawConfig
	if trimmed[0] == '{' {
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return rawConfig{}, invalid("config", err.Error(), "must match config schema")
		}
		return raw, nil
	}
	yamlMap, err := parseYAMLMap(extractPluginConfigYAML(string(trimmed)))
	if err != nil {
		return rawConfig{}, err
	}
	encoded, err := json.Marshal(yamlMap)
	if err != nil {
		return rawConfig{}, invalid("config", "yaml", "must be encodable")
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return rawConfig{}, invalid("config", err.Error(), "must match config schema")
	}
	return raw, nil
}

func (raw rawConfig) apply(cfg Config) (Config, error) {
	if raw.Enabled != nil {
		cfg.Enabled = *raw.Enabled
	}
	if raw.AutoApply != nil {
		cfg.AutoApply = *raw.AutoApply
	}
	if raw.ProviderScope != nil {
		providerScope, selectedFromScope, err := ParseProviderScopeValue(*raw.ProviderScope)
		if err != nil {
			return Config{}, err
		}
		cfg.ProviderScope = providerScope
		if len(selectedFromScope) > 0 {
			cfg.SelectedProviders = selectedFromScope
		}
	}
	if raw.SelectedProviders.set {
		providers, err := NormalizeSelectedProviders(raw.SelectedProviders.values)
		if err != nil {
			return Config{}, err
		}
		// 旧 selected_providers 仅在 provider_scope 未直接给出列表时生效。
		if raw.ProviderScope == nil || (cfg.ProviderScope == ProviderScopeSelected && len(cfg.SelectedProviders) == 0) {
			cfg.SelectedProviders = providers
			if len(providers) > 0 {
				cfg.ProviderScope = ProviderScopeSelected
			}
		}
	}
	if raw.AntigravityModelGroup != nil {
		modelGroup, err := ParseAntigravityModelGroup(*raw.AntigravityModelGroup)
		if err != nil {
			return Config{}, err
		}
		cfg.AntigravityModelGroup = modelGroup
	}
	if cfg.ProviderScope == ProviderScopeSelected && len(cfg.SelectedProviders) == 0 {
		cfg.ProviderScope = ProviderScopeAll
		cfg.SelectedProviders = nil
	}
	for _, item := range []struct {
		field  string
		raw    *rawDuration
		target *time.Duration
	}{
		{"interval", raw.Interval, &cfg.Interval},
		{"active_group_jitter", raw.ActiveGroupJitter, &cfg.ActiveGroupJitter},
		{"disabled_probe_interval", raw.DisabledProbeInterval, &cfg.DisabledProbeInterval},
	} {
		if item.raw != nil {
			parsed, err := parseDuration(item.field, string(*item.raw))
			if err != nil {
				return Config{}, err
			}
			*item.target = parsed
		}
	}
	for _, item := range []struct {
		field  string
		raw    *int
		target *int
		min    int
	}{
		{"max_concurrency", raw.MaxConcurrency, &cfg.MaxConcurrency, 1},
		{"min_change", raw.MinChange, &cfg.MinChange, 0},
		{"immediate_probe_limit", raw.ImmediateProbeLimit, &cfg.ImmediateProbeLimit, 1},
		{"top_priority_probe_count", raw.TopPriorityProbeCount, &cfg.TopPriorityProbeCount, 1},
		{"active_group_size", raw.ActiveGroupSize, &cfg.ActiveGroupSize, 1},
		{"disabled_group_size", raw.DisabledGroupSize, &cfg.DisabledGroupSize, 1},
	} {
		if item.raw != nil {
			*item.target = *item.raw
		}
		if *item.target < item.min {
			return Config{}, invalid(item.field, fmt.Sprint(*item.target), fmt.Sprintf("must be at least %d", item.min))
		}
	}
	if raw.ProviderOverrides == nil {
		return cfg, nil
	}
	cfg.ProviderOverrides = make(map[string]ProviderOverride, len(raw.ProviderOverrides))
	for providerName, rawOverride := range raw.ProviderOverrides {
		override, err := rawOverride.apply(providerName)
		if err != nil {
			return Config{}, err
		}
		cfg.ProviderOverrides[providerName] = override
	}
	return cfg, nil
}

func (raw rawProviderOverride) apply(providerName string) (ProviderOverride, error) {
	override := ProviderOverride{Enabled: raw.Enabled, AutoApply: raw.AutoApply}
	if raw.Interval != nil {
		parsed, err := parseDuration("provider_overrides."+providerName+".interval", string(*raw.Interval))
		if err != nil {
			return ProviderOverride{}, err
		}
		override.Interval = parsed
	}
	if raw.MaxConcurrency == nil {
		return override, nil
	}
	if *raw.MaxConcurrency < 1 {
		return ProviderOverride{}, invalid("provider_overrides."+providerName+".max_concurrency", fmt.Sprint(*raw.MaxConcurrency), "must be at least 1")
	}
	override.MaxConcurrency = *raw.MaxConcurrency
	return override, nil
}
