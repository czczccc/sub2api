package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// SettingKeyWorkBuddyConfig 是 WorkBuddy（腾讯 CodeBuddy 平台）网关行为配置在 settings 表中的键。
const SettingKeyWorkBuddyConfig = "workbuddy_config"

// WorkBuddy 系统提示词模式。
const (
	// WorkBuddyPromptModePassthrough 原样透传客户端的 system 消息。
	WorkBuddyPromptModePassthrough = "passthrough"
	// WorkBuddyPromptModeDegrade 透传；一旦被上游内容审核拦截（11128），当天改用中性提示词。默认值。
	WorkBuddyPromptModeDegrade = "degrade"
	// WorkBuddyPromptModeAppend 保留客户端 system，在其后追加网关提示词；被拦截时同样降级。
	WorkBuddyPromptModeAppend = "append"
	// WorkBuddyPromptModeCustom 用网关提示词替换客户端全部 system 消息。
	WorkBuddyPromptModeCustom = "custom"
)

// WorkBuddyConfig 是后台可编辑的 WorkBuddy 网关行为配置。零值即默认行为。
type WorkBuddyConfig struct {
	// SanitizeDisabled 为 true 时关闭出站指纹脱敏（默认开启）。
	SanitizeDisabled bool `json:"sanitize_disabled"`
	// PromptMode 见 WorkBuddyPromptMode* 常量；空值按 degrade 处理。
	PromptMode string `json:"prompt_mode"`
	// PromptText 是 append / custom 模式使用的网关提示词；为空时用内置默认提示词。
	PromptText string `json:"prompt_text"`
}

type cachedWorkBuddyConfig struct {
	config    WorkBuddyConfig
	expiresAt int64
}

func normalizeWorkBuddyConfig(cfg WorkBuddyConfig) WorkBuddyConfig {
	switch mode := strings.ToLower(strings.TrimSpace(cfg.PromptMode)); mode {
	case WorkBuddyPromptModePassthrough, WorkBuddyPromptModeDegrade, WorkBuddyPromptModeAppend, WorkBuddyPromptModeCustom:
		cfg.PromptMode = mode
	default:
		cfg.PromptMode = WorkBuddyPromptModeDegrade
	}
	cfg.PromptText = strings.TrimSpace(cfg.PromptText)
	return cfg
}

// EffectivePromptText 返回 append / custom 模式实际使用的提示词。
func (cfg WorkBuddyConfig) EffectivePromptText() string {
	if cfg.PromptText != "" {
		return cfg.PromptText
	}
	return TencentCodeBuddyDefaultSystemPrompt
}

// GetWorkBuddyConfig 返回归一化后的配置；读不到时返回默认配置。
func (s *SettingService) GetWorkBuddyConfig(ctx context.Context) WorkBuddyConfig {
	if s == nil || s.settingRepo == nil {
		return normalizeWorkBuddyConfig(WorkBuddyConfig{})
	}
	if cached, ok := s.workBuddyConfigCache.Load().(*cachedWorkBuddyConfig); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.config
	}
	value, _, _ := s.workBuddyConfigSF.Do(SettingKeyWorkBuddyConfig, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.Background(), modelCapabilityDBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(loadCtx, SettingKeyWorkBuddyConfig)
		ttl := modelCapabilityCacheTTL
		var parsed WorkBuddyConfig
		if err != nil {
			ttl = modelCapabilityErrorTTL
		} else if strings.TrimSpace(raw) != "" {
			_ = json.Unmarshal([]byte(raw), &parsed)
		}
		entry := &cachedWorkBuddyConfig{config: normalizeWorkBuddyConfig(parsed), expiresAt: time.Now().Add(ttl).UnixNano()}
		s.workBuddyConfigCache.Store(entry)
		return entry, nil
	})
	if cached, ok := value.(*cachedWorkBuddyConfig); ok && cached != nil {
		return cached.config
	}
	return normalizeWorkBuddyConfig(WorkBuddyConfig{})
}

// UpdateWorkBuddyConfig 归一化并落盘，随后立即刷新进程内缓存。
func (s *SettingService) UpdateWorkBuddyConfig(ctx context.Context, cfg WorkBuddyConfig) (WorkBuddyConfig, error) {
	if s == nil || s.settingRepo == nil {
		return WorkBuddyConfig{}, errModelCapabilityRepoUnavailable
	}
	normalized := normalizeWorkBuddyConfig(cfg)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return WorkBuddyConfig{}, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyWorkBuddyConfig, string(encoded)); err != nil {
		return WorkBuddyConfig{}, err
	}
	s.workBuddyConfigCache.Store(&cachedWorkBuddyConfig{config: normalized, expiresAt: time.Now().Add(modelCapabilityCacheTTL).UnixNano()})
	return normalized, nil
}
