package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// errModelCapabilityRepoUnavailable 表示服务未装配 settings 仓库（测试或裁剪部署）。
var errModelCapabilityRepoUnavailable = errors.New("setting repository is not configured")

// SettingKeyModelCapabilityConfig 是"模型能力覆盖"配置在 settings 表中的键。
//
// 存 JSON 而不是新建表：这是一份由管理员手工维护的少量覆盖项，读多写极少，
// 不需要关系约束与查询能力。与 image_storage_config / ops_* 等既有后台配置一致。
const SettingKeyModelCapabilityConfig = "model_capability_config"

// 与 setting_gateway_runtime.go 中其它热路径设置的进程内缓存保持一致：
// 60s 正常 TTL，出错时 5s 短 TTL 快速重试。
const (
	modelCapabilityCacheTTL  = 60 * time.Second
	modelCapabilityErrorTTL  = 5 * time.Second
	modelCapabilityDBTimeout = 5 * time.Second
)

// ModelCapabilityEntry 描述某个平台下某个模型对外声明的能力。
//
// 为什么需要它：部分上游（如腾讯 CodeBuddy 的私有目录接口）不返回任何能力字段，
// 公共注册表也匹配不到，于是 context_window / 输出上限 / 输入模态只能由管理员
// 手工声明。网关侧只把这套声明用于"对外广告"，不影响实际转发行为。
type ModelCapabilityEntry struct {
	// Platform 为空表示该条目对所有平台生效（按模型 ID 匹配）。
	Platform string `json:"platform"`
	ModelID  string `json:"model_id"`
	// ContextWindow / MaxOutputTokens 为 0 表示"未声明"，调用方应继续沿用
	// 自身的兜底逻辑，而不是把 0 当作真实值。
	ContextWindow   int64    `json:"context_window,omitempty"`
	MaxOutputTokens int64    `json:"max_output_tokens,omitempty"`
	InputModalities []string `json:"input_modalities,omitempty"`
}

// ModelCapabilityConfig 是后台可编辑的完整配置。
type ModelCapabilityConfig struct {
	Models []ModelCapabilityEntry `json:"models"`
}

// modelCapabilityIndex 是配置的查找视图，避免每次请求线性扫描切片。
type modelCapabilityIndex struct {
	// exact 的键为 platform + "\x00" + modelID（均为小写）。
	exact map[string]ModelCapabilityEntry
	// wildcard 的键为小写 modelID，对应 Platform 为空的条目。
	wildcard map[string]ModelCapabilityEntry
}

type cachedModelCapabilityConfig struct {
	config    ModelCapabilityConfig
	index     modelCapabilityIndex
	expiresAt int64 // unix nano
}

// modelCapabilityInputModalities 是允许声明的输入模态白名单。
// 收敛取值可以避免后台误填（例如 "png"）被原样透给客户端。
var modelCapabilityInputModalities = map[string]struct{}{
	"text":  {},
	"image": {},
	"audio": {},
	"video": {},
}

func modelCapabilityKey(platform, modelID string) string {
	return strings.ToLower(strings.TrimSpace(platform)) + "\x00" +
		strings.ToLower(strings.TrimSpace(modelID))
}

// normalizeModelCapabilityConfig 归一化后台提交或库中读到的配置：
// 丢弃无模型 ID 的条目、钳制负数、模态去重并限定在白名单内。
//
// 归一化在读取路径也执行：库里的值可能是旧版本写入的，不能假设它已经合法。
func normalizeModelCapabilityConfig(cfg ModelCapabilityConfig) (ModelCapabilityConfig, modelCapabilityIndex) {
	normalized := ModelCapabilityConfig{Models: make([]ModelCapabilityEntry, 0, len(cfg.Models))}
	index := modelCapabilityIndex{
		exact:    make(map[string]ModelCapabilityEntry, len(cfg.Models)),
		wildcard: make(map[string]ModelCapabilityEntry, len(cfg.Models)),
	}

	for _, entry := range cfg.Models {
		modelID := strings.TrimSpace(entry.ModelID)
		if modelID == "" {
			continue
		}
		entry.Platform = strings.ToLower(strings.TrimSpace(entry.Platform))
		entry.ModelID = modelID
		if entry.ContextWindow < 0 {
			entry.ContextWindow = 0
		}
		if entry.MaxOutputTokens < 0 {
			entry.MaxOutputTokens = 0
		}
		entry.InputModalities = normalizeModelCapabilityModalities(entry.InputModalities)
		if entry.ContextWindow == 0 && entry.MaxOutputTokens == 0 && len(entry.InputModalities) == 0 {
			// 全空的条目没有信息量，落盘只会让后台多出一行噪声。
			continue
		}

		normalized.Models = append(normalized.Models, entry)
		key := modelCapabilityKey(entry.Platform, entry.ModelID)
		if entry.Platform == "" {
			index.wildcard[strings.ToLower(entry.ModelID)] = entry
			continue
		}
		index.exact[key] = entry
	}
	return normalized, index
}

func normalizeModelCapabilityModalities(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		modality := strings.ToLower(strings.TrimSpace(value))
		if _, ok := modelCapabilityInputModalities[modality]; !ok {
			continue
		}
		if _, dup := seen[modality]; dup {
			continue
		}
		seen[modality] = struct{}{}
		out = append(out, modality)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (idx modelCapabilityIndex) lookup(platform, modelID string) (ModelCapabilityEntry, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ModelCapabilityEntry{}, false
	}
	if entry, ok := idx.exact[modelCapabilityKey(platform, modelID)]; ok {
		return entry, true
	}
	entry, ok := idx.wildcard[strings.ToLower(modelID)]
	return entry, ok
}

// GetModelCapabilityConfig 返回归一化后的配置。读取失败时返回空配置而不是错误：
// 声明能力是"锦上添花"，读不到就退回各调用方原有的兜底逻辑，不应让
// /v1/models、Codex manifest 这类接口整体失败。
func (s *SettingService) GetModelCapabilityConfig(ctx context.Context) ModelCapabilityConfig {
	if s == nil || s.settingRepo == nil {
		return ModelCapabilityConfig{}
	}
	now := time.Now().UnixNano()
	if cached, ok := s.modelCapabilityCache.Load().(*cachedModelCapabilityConfig); ok &&
		cached != nil && now < cached.expiresAt {
		return cached.config
	}

	value, _, _ := s.modelCapabilitySF.Do("model_capability_config", func() (any, error) {
		if cached, ok := s.modelCapabilityCache.Load().(*cachedModelCapabilityConfig); ok &&
			cached != nil && time.Now().UnixNano() < cached.expiresAt {
			return cached, nil
		}
		loadCtx, cancel := context.WithTimeout(context.Background(), modelCapabilityDBTimeout)
		defer cancel()

		raw, err := s.settingRepo.GetValue(loadCtx, SettingKeyModelCapabilityConfig)
		if err != nil {
			// 既可能是"从未配置过"（正常），也可能是 DB 故障。两者都降级为空配置，
			// 并用短 TTL 缓存以便快速重试。
			entry := &cachedModelCapabilityConfig{
				expiresAt: time.Now().Add(modelCapabilityErrorTTL).UnixNano(),
			}
			s.modelCapabilityCache.Store(entry)
			return entry, nil
		}
		var parsed ModelCapabilityConfig
		if strings.TrimSpace(raw) != "" {
			// 解析失败同样降级为空配置：手工写坏的 JSON 不应该打挂模型列表。
			_ = json.Unmarshal([]byte(raw), &parsed)
		}
		normalized, index := normalizeModelCapabilityConfig(parsed)
		entry := &cachedModelCapabilityConfig{
			config:    normalized,
			index:     index,
			expiresAt: time.Now().Add(modelCapabilityCacheTTL).UnixNano(),
		}
		s.modelCapabilityCache.Store(entry)
		return entry, nil
	})

	if cached, ok := value.(*cachedModelCapabilityConfig); ok && cached != nil {
		return cached.config
	}
	return ModelCapabilityConfig{}
}

// ResolveModelCapability 查找 (platform, model) 的能力声明，先精确匹配平台，
// 再回落到平台无关条目。
func (s *SettingService) ResolveModelCapability(ctx context.Context, platform, modelID string) (ModelCapabilityEntry, bool) {
	if s == nil || s.settingRepo == nil {
		return ModelCapabilityEntry{}, false
	}
	s.GetModelCapabilityConfig(ctx)
	cached, ok := s.modelCapabilityCache.Load().(*cachedModelCapabilityConfig)
	if !ok || cached == nil {
		return ModelCapabilityEntry{}, false
	}
	return cached.index.lookup(platform, modelID)
}

// UpdateModelCapabilityConfig 归一化并落盘，随后立即刷新进程内缓存，
// 让后台保存后马上生效（多实例部署下其它实例最多滞后一个 TTL）。
func (s *SettingService) UpdateModelCapabilityConfig(ctx context.Context, cfg ModelCapabilityConfig) (ModelCapabilityConfig, error) {
	if s == nil || s.settingRepo == nil {
		return ModelCapabilityConfig{}, errModelCapabilityRepoUnavailable
	}
	normalized, index := normalizeModelCapabilityConfig(cfg)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return ModelCapabilityConfig{}, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyModelCapabilityConfig, string(encoded)); err != nil {
		return ModelCapabilityConfig{}, err
	}
	s.modelCapabilityCache.Store(&cachedModelCapabilityConfig{
		config:    normalized,
		index:     index,
		expiresAt: time.Now().Add(modelCapabilityCacheTTL).UnixNano(),
	})
	return normalized, nil
}
