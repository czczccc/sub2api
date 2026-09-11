package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// modelCapabilitySettingRepo 是只实现本文件用到方法的 settings 仓库替身。
// 刻意不复用带 //go:build unit 标签的 stubSettingRepo：本文件需要在默认
// （不带 tag）测试运行下也可编译。
type modelCapabilitySettingRepo struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
}

func (r *modelCapabilitySettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[key], nil
}

func (r *modelCapabilitySettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func newModelCapabilityServiceForTest() (*SettingService, *modelCapabilitySettingRepo) {
	repo := &modelCapabilitySettingRepo{values: map[string]string{}}
	return &SettingService{settingRepo: repo}, repo
}

func TestModelCapabilityConfigRoundTripAndNormalization(t *testing.T) {
	t.Parallel()

	svc, repo := newModelCapabilityServiceForTest()
	ctx := context.Background()

	// 未配置过：返回空配置而不是错误。
	require.Empty(t, svc.GetModelCapabilityConfig(ctx).Models)

	saved, err := svc.UpdateModelCapabilityConfig(ctx, ModelCapabilityConfig{Models: []ModelCapabilityEntry{
		{
			Platform: "  CodeBuddy ", ModelID: " hy3 ",
			ContextWindow: 200_000, MaxOutputTokens: 64_000,
			InputModalities: []string{"TEXT", "image", "image", "png"},
		},
		{ModelID: "glm-5v-turbo", InputModalities: []string{"text", "image"}},
		{ModelID: "  "},                          // 无模型 ID → 丢弃
		{ModelID: "empty-entry"},                 // 全空 → 丢弃
		{ModelID: "negative", ContextWindow: -5}, // 钳制为 0 → 变全空 → 丢弃
		{ModelID: "clamped", MaxOutputTokens: -1, ContextWindow: 128_000},
	}})
	require.NoError(t, err)
	require.Len(t, saved.Models, 3)

	// 平台与模态都归一化为小写，未知模态（png）被丢弃且去重。
	require.Equal(t, "codebuddy", saved.Models[0].Platform)
	require.Equal(t, "hy3", saved.Models[0].ModelID)
	require.Equal(t, []string{"text", "image"}, saved.Models[0].InputModalities)

	// 落盘内容与返回值一致。
	raw := repo.values[SettingKeyModelCapabilityConfig]
	var persisted ModelCapabilityConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &persisted))
	require.Equal(t, saved, persisted)

	// 命中进程内缓存：再次读取不需要重新解析也能拿到同样结果。
	require.Equal(t, saved, svc.GetModelCapabilityConfig(ctx))
}

func TestModelCapabilityConfigResolvePrefersPlatformEntry(t *testing.T) {
	t.Parallel()

	svc, _ := newModelCapabilityServiceForTest()
	ctx := context.Background()
	_, err := svc.UpdateModelCapabilityConfig(ctx, ModelCapabilityConfig{Models: []ModelCapabilityEntry{
		{ModelID: "deepseek-v4-pro", ContextWindow: 128_000},
		{Platform: PlatformTencentCodeBuddy, ModelID: "deepseek-v4-pro", ContextWindow: 400_000},
	}})
	require.NoError(t, err)

	// 平台精确条目优先于平台无关条目。
	entry, ok := svc.ResolveModelCapability(ctx, PlatformTencentCodeBuddy, "deepseek-v4-pro")
	require.True(t, ok)
	require.Equal(t, int64(400_000), entry.ContextWindow)

	// 其它平台回落到平台无关条目。
	entry, ok = svc.ResolveModelCapability(ctx, PlatformDeepseek, "deepseek-v4-pro")
	require.True(t, ok)
	require.Equal(t, int64(128_000), entry.ContextWindow)

	// 大小写与空白不敏感。
	entry, ok = svc.ResolveModelCapability(ctx, "CodeBuddy", " DeepSeek-V4-Pro ")
	require.True(t, ok)
	require.Equal(t, int64(400_000), entry.ContextWindow)

	// 未声明的模型不返回条目。
	_, ok = svc.ResolveModelCapability(ctx, PlatformTencentCodeBuddy, "unknown-model")
	require.False(t, ok)
}

// 库里存着被写坏的 JSON 时不能让 /v1/models 与 Codex manifest 整体失败。
func TestModelCapabilityConfigToleratesCorruptStoredValue(t *testing.T) {
	t.Parallel()

	svc, repo := newModelCapabilityServiceForTest()
	repo.values[SettingKeyModelCapabilityConfig] = "{not json"

	require.Empty(t, svc.GetModelCapabilityConfig(context.Background()).Models)
	_, ok := svc.ResolveModelCapability(context.Background(), PlatformTencentCodeBuddy, "hy3")
	require.False(t, ok)
}

func TestModelCapabilityConfigUnavailableRepoFallsBackToEmpty(t *testing.T) {
	t.Parallel()

	svc := &SettingService{}
	require.Empty(t, svc.GetModelCapabilityConfig(context.Background()).Models)
	_, ok := svc.ResolveModelCapability(context.Background(), PlatformTencentCodeBuddy, "hy3")
	require.False(t, ok)
	_, err := svc.UpdateModelCapabilityConfig(context.Background(), ModelCapabilityConfig{})
	require.ErrorIs(t, err, errModelCapabilityRepoUnavailable)
}
