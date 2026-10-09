package service

import (
	"context"
	"encoding/json"
	"sort"
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

	// 日常保号任务（北京时间整点执行；Hours 为空时取默认时点）。
	ActivityTask WorkBuddyTaskSchedule `json:"activity_task"`
	StreakTask   WorkBuddyTaskSchedule `json:"streak_task"`
	TravelTask   WorkBuddyTaskSchedule `json:"travel_task"`
	NicknameTask WorkBuddyTaskSchedule `json:"nickname_task"`
	// 余额定时刷新：积分恢复后自动解除"积分耗尽"暂停。
	BalanceRefreshDisabled bool `json:"balance_refresh_disabled"`
	BalanceRefreshMinutes  int  `json:"balance_refresh_minutes"`
}

// WorkBuddyTaskSchedule 是单个日常任务的开关与执行时点。
type WorkBuddyTaskSchedule struct {
	Disabled bool  `json:"disabled"`
	Hours    []int `json:"hours"`
}

// 日常任务名，也是手动执行接口的路径参数。
const (
	WorkBuddyTaskActivity = "activity"
	WorkBuddyTaskStreak   = "streak"
	WorkBuddyTaskTravel   = "travel"
	WorkBuddyTaskNickname = "nickname"
	WorkBuddyTaskBalance  = "balance"
)

// workBuddyDefaultTaskHours 是各任务的默认执行时点（北京时间），与参考实现一致。
var workBuddyDefaultTaskHours = map[string][]int{
	WorkBuddyTaskActivity: {10},
	WorkBuddyTaskStreak:   {9, 21},
	WorkBuddyTaskTravel:   {9, 21},
	WorkBuddyTaskNickname: {8},
}

const workBuddyDefaultBalanceRefreshMinutes = 5

func normalizeWorkBuddyTaskSchedule(schedule WorkBuddyTaskSchedule, task string) WorkBuddyTaskSchedule {
	seen := map[int]bool{}
	hours := make([]int, 0, len(schedule.Hours))
	for _, h := range schedule.Hours {
		if h < 0 || h > 23 || seen[h] {
			continue
		}
		seen[h] = true
		hours = append(hours, h)
	}
	if len(hours) == 0 {
		hours = append(hours, workBuddyDefaultTaskHours[task]...)
	}
	sort.Ints(hours)
	schedule.Hours = hours
	return schedule
}

// TaskSchedule 返回任务的调度配置；未知任务返回禁用。
func (cfg WorkBuddyConfig) TaskSchedule(task string) WorkBuddyTaskSchedule {
	switch task {
	case WorkBuddyTaskActivity:
		return cfg.ActivityTask
	case WorkBuddyTaskStreak:
		return cfg.StreakTask
	case WorkBuddyTaskTravel:
		return cfg.TravelTask
	case WorkBuddyTaskNickname:
		return cfg.NicknameTask
	}
	return WorkBuddyTaskSchedule{Disabled: true}
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
	cfg.ActivityTask = normalizeWorkBuddyTaskSchedule(cfg.ActivityTask, WorkBuddyTaskActivity)
	cfg.StreakTask = normalizeWorkBuddyTaskSchedule(cfg.StreakTask, WorkBuddyTaskStreak)
	cfg.TravelTask = normalizeWorkBuddyTaskSchedule(cfg.TravelTask, WorkBuddyTaskTravel)
	cfg.NicknameTask = normalizeWorkBuddyTaskSchedule(cfg.NicknameTask, WorkBuddyTaskNickname)
	if cfg.BalanceRefreshMinutes <= 0 {
		cfg.BalanceRefreshMinutes = workBuddyDefaultBalanceRefreshMinutes
	}
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
