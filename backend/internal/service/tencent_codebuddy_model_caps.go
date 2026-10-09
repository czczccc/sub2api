package service

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 本文件处理 CodeBuddy / WorkBuddy 模型的能力参数：上下文长度、最大输出 tokens、
// 是否识图。
//
// 来源依据（2026-10-09 拆包官方 CLI npm @tencent-ai/codebuddy-code 2.163.0）：
// 客户端的模型对象统一带 maxInputTokens / maxOutputTokens / supportsImages /
// disabledMultimodal / name 等字段，来源有三处并按 id 合并：
//   - 包内 product.json 的 models[]（国际站内置表）；
//   - GET {endpoint}/v3/config 下发的产品配置里的 models[]；
//   - GET {endpoint}/console/enterprises/{enterpriseId}/config/models（企业配置）。
//
// sub2api 使用的目录接口 /v2/enterprises/personal/models 是否带这些字段尚未用大陆站
// 真实账号确认，因此解析对字段缺失保持宽容：有什么取什么，缺的留空，再由
// 内置表（DefaultTencentCodeBuddyModelCapabilities）兜底。
//
// 取值优先级（高 → 低）：
//  1. 后台「模型能力覆盖」手填值（由调用方叠加，见 SettingService.ResolveModelCapability）；
//  2. 账号快照：模型同步 / 每日签到时从上游解析并写入 extra；
//  3. 内置参数表；
//  4. 平台默认：输入模态 text+image，上下文与输出上限留空。

const (
	// TencentCodeBuddyModelCapabilitiesExtraKey 是账号 extra 中上游模型能力快照的键。
	TencentCodeBuddyModelCapabilitiesExtraKey = "codebuddy_model_capabilities"

	// tencentCodeBuddyProductConfigPath 是官方客户端拉取产品配置（含模型参数）的路径，挂在站点根上。
	tencentCodeBuddyProductConfigPath = "/v3/config"
	// tencentCodeBuddyMaxConfigBody 限制产品配置响应大小（CLI 内置 product.json 约 370KB）。
	tencentCodeBuddyMaxConfigBody = 2 * 1024 * 1024
)

// 能力来源标识，用于后台展示。
const (
	TencentCodeBuddyCapabilitySourceOverride = "override"
	TencentCodeBuddyCapabilitySourceUpstream = "upstream"
	TencentCodeBuddyCapabilitySourceBuiltin  = "builtin"
	TencentCodeBuddyCapabilitySourceDefault  = "default"
)

// TencentCodeBuddyModelCapability 是单个模型的能力参数。零值字段表示"未知"。
type TencentCodeBuddyModelCapability struct {
	ContextWindow   int64  `json:"context_window,omitempty"`
	MaxOutputTokens int64  `json:"max_output_tokens,omitempty"`
	SupportsImages  *bool  `json:"supports_images,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
	// SupportedEfforts 是模型接受的 reasoning_effort 档位（上游 reasoning.supportedEfforts）；
	// 为空表示未知，网关不做档位降级。
	SupportedEfforts []string `json:"supported_efforts,omitempty"`
	// DefaultEffort 是模型声明的默认档位（reasoning.defaultEffort，老字段 reasoning.effort）。
	DefaultEffort string `json:"default_effort,omitempty"`
}

// IsEmpty 报告是否没有任何能力字段。
func (c TencentCodeBuddyModelCapability) IsEmpty() bool {
	return c.ContextWindow <= 0 && c.MaxOutputTokens <= 0 && c.SupportsImages == nil && len(c.SupportedEfforts) == 0
}

// InputModalities 把识图标记转成输入模态；未知时返回 nil。
func (c TencentCodeBuddyModelCapability) InputModalities() []string {
	if c.SupportsImages == nil {
		return nil
	}
	if *c.SupportsImages {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// mergeMissing 用 fallback 补齐 c 中缺失的字段（c 已有的值不被覆盖）。
func (c TencentCodeBuddyModelCapability) mergeMissing(fallback TencentCodeBuddyModelCapability) TencentCodeBuddyModelCapability {
	if c.ContextWindow <= 0 {
		c.ContextWindow = fallback.ContextWindow
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = fallback.MaxOutputTokens
	}
	if c.SupportsImages == nil && fallback.SupportsImages != nil {
		v := *fallback.SupportsImages
		c.SupportsImages = &v
	}
	if strings.TrimSpace(c.DisplayName) == "" {
		c.DisplayName = fallback.DisplayName
	}
	// 档位与默认档同源：快照没有档位时整组取兜底，不跨源拼接。
	if len(c.SupportedEfforts) == 0 && len(fallback.SupportedEfforts) > 0 {
		c.SupportedEfforts = append([]string(nil), fallback.SupportedEfforts...)
		c.DefaultEffort = fallback.DefaultEffort
	}
	return c
}

// TencentCodeBuddyModelCapabilitySnapshot 是写入账号 extra 的上游能力快照。
type TencentCodeBuddyModelCapabilitySnapshot struct {
	SyncedAt string                                     `json:"synced_at"`
	Models   map[string]TencentCodeBuddyModelCapability `json:"models"`
}

// DefaultTencentCodeBuddyModelCapabilities 是内置参数表，仅在上游未给出时兜底。
//
// 数值取自官方 CLI 2.163.0 内置 product.json（国际站）与 product.cloudhosted.json，
// **并非大陆站实测**：大陆站同名模型的上限可能不同。识图不在表里声明——2026-09-12
// 对 15 个大陆模型的实测全部能识图，交给平台默认（text+image）即可。
// 因为是推断值，网关裁剪 max_tokens 时不使用本表（见 tencentCodeBuddyClampMaxOutputTokens）。
func DefaultTencentCodeBuddyModelCapabilities() map[string]TencentCodeBuddyModelCapability {
	return map[string]TencentCodeBuddyModelCapability{
		// 大陆版与国际版重名模型
		"hy4-preview":         {ContextWindow: 1_000_000, MaxOutputTokens: 64_000},
		"hy3":                 {ContextWindow: 192_000, MaxOutputTokens: 64_000},
		"deepseek-v4.1-flash": {ContextWindow: 1_000_000, MaxOutputTokens: 128_000},
		"deepseek-v4-pro":     {ContextWindow: 1_000_000, MaxOutputTokens: 50_000},
		"glm-5.3":             {ContextWindow: 1_000_000, MaxOutputTokens: 48_000},
		"glm-5.3-flash":       {ContextWindow: 1_000_000, MaxOutputTokens: 32_000},
		"glm-5.2":             {ContextWindow: 1_000_000, MaxOutputTokens: 48_000},
		"glm-5.1":             {ContextWindow: 200_000, MaxOutputTokens: 48_000},
		"glm-5v-turbo":        {ContextWindow: 200_000, MaxOutputTokens: 38_000},
		"kimi-k2.6":           {ContextWindow: 256_000, MaxOutputTokens: 32_000},
		"auto":                {ContextWindow: 168_000, MaxOutputTokens: 32_000},
		// 国际版 WorkBuddy
		"default-model":    {ContextWindow: 176_000, MaxOutputTokens: 24_000},
		"fast-model":       {ContextWindow: 200_000, MaxOutputTokens: 32_000},
		"balanced-model":   {ContextWindow: 256_000, MaxOutputTokens: 32_000},
		"primary-model":    {ContextWindow: 272_000, MaxOutputTokens: 72_000},
		"deep-model":       {ContextWindow: 176_000, MaxOutputTokens: 24_000},
		"gpt-5.6-sol":      {ContextWindow: 1_000_000, MaxOutputTokens: 128_000},
		"gpt-5.6-terra":    {ContextWindow: 1_000_000, MaxOutputTokens: 128_000},
		"gpt-5.6-luna":     {ContextWindow: 1_000_000, MaxOutputTokens: 128_000},
		"gpt-5.5":          {ContextWindow: 1_000_000, MaxOutputTokens: 128_000},
		"gpt-5.4":          {ContextWindow: 272_000, MaxOutputTokens: 72_000},
		"gemini-3.5-flash": {ContextWindow: 1_000_000, MaxOutputTokens: 65_536},
		"kimi-k3":          {ContextWindow: 1_000_000, MaxOutputTokens: 32_000},
	}
}

// tencentCodeBuddyBuiltinEfforts 是大陆站模型的推理档位兜底表，上游目录没有下发
// reasoning.supportedEfforts 时使用。数值照抄参考实现 workbuddy2api-panel
// internal/upstream/effort_catalog.go 的 cnEffortFallback（来自官方客户端内置表）。
var tencentCodeBuddyBuiltinEfforts = map[string]TencentCodeBuddyModelCapability{
	"deepseek-v4-flash":   {SupportedEfforts: []string{"low", "high", "max"}},
	"deepseek-v4.1-flash": {SupportedEfforts: []string{"low", "high", "max"}, DefaultEffort: "high"},
	"deepseek-v4-pro":     {SupportedEfforts: []string{"low", "high", "xhigh"}, DefaultEffort: "high"},
	"hy4-preview":         {SupportedEfforts: []string{"high"}, DefaultEffort: "high"},
	"hy4-preview-x":       {SupportedEfforts: []string{"high"}},
	"hy3":                 {SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high"},
	"hy3-x":               {SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high"},
	"glm-5.3":             {SupportedEfforts: []string{"low", "high", "max"}, DefaultEffort: "high"},
	"glm-5.3-flash":       {SupportedEfforts: []string{"low", "high", "max"}, DefaultEffort: "high"},
	"glm-5.2":             {SupportedEfforts: []string{"high", "xhigh"}, DefaultEffort: "high"},
	"glm-5.1":             {SupportedEfforts: []string{"medium"}},
	"glm-5v-turbo":        {SupportedEfforts: []string{"medium"}},
	"kimi-k3-1":           {SupportedEfforts: []string{"medium"}},
	"kimi-k2.7":           {SupportedEfforts: []string{"medium"}},
	"kimi-k2.6":           {SupportedEfforts: []string{"medium"}},
	"minimax-m3":          {SupportedEfforts: []string{"medium"}},
}

// tencentCodeBuddyBuiltinCapability 返回内置表条目：参数表与档位表按字段合并。
func tencentCodeBuddyBuiltinCapability(modelID string) (TencentCodeBuddyModelCapability, bool) {
	capability, ok := DefaultTencentCodeBuddyModelCapabilities()[modelID]
	if efforts, hasEfforts := tencentCodeBuddyBuiltinEfforts[modelID]; hasEfforts {
		capability = capability.mergeMissing(efforts)
		ok = true
	}
	return capability, ok
}

// ===== 解析 =====

// parseTencentCodeBuddyModelCapabilities 从上游响应中解析每个模型的能力参数。
//
// 兼容的形态（字段缺失时该模型照样返回，只是对应字段为空）：
//   - 目录接口 {code,data:{models:[{...}]}}；
//   - 企业配置 {code,data:[{...}]}；
//   - 产品配置 {models:[...]} 或 {code,data:{...,models:[...]}}。
//
// 只返回至少带一个能力字段的模型。
func parseTencentCodeBuddyModelCapabilities(body []byte) map[string]TencentCodeBuddyModelCapability {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	root := gjson.ParseBytes(body)
	var items []gjson.Result
	for _, path := range []string{"data.models", "models", "data"} {
		if value := root.Get(path); value.IsArray() {
			items = value.Array()
			break
		}
	}
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]TencentCodeBuddyModelCapability, len(items))
	for _, item := range items {
		if !item.IsObject() {
			continue
		}
		id := ""
		for _, key := range []string{"id", "modelId", "model"} {
			if v := strings.TrimSpace(item.Get(key).String()); v != "" {
				id = v
				break
			}
		}
		if id == "" {
			continue
		}
		capability := parseTencentCodeBuddyModelCapabilityItem(item)
		if capability.IsEmpty() {
			continue
		}
		out[id] = capability
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseTencentCodeBuddyModelCapabilityItem(item gjson.Result) TencentCodeBuddyModelCapability {
	var capability TencentCodeBuddyModelCapability
	capability.ContextWindow = firstPositiveInt(item,
		"maxInputTokens", "max_input_tokens", "contextLength", "context_length", "contextWindow", "context_window")
	capability.MaxOutputTokens = firstPositiveInt(item,
		"maxOutputTokens", "max_output_tokens", "maxTokens", "max_tokens")
	for _, key := range []string{"supportsImages", "supports_images", "supportsVision", "vision"} {
		if v := item.Get(key); v.Exists() && (v.Type == gjson.True || v.Type == gjson.False) {
			supports := v.Bool()
			capability.SupportsImages = &supports
			break
		}
	}
	// disabledMultimodal=true 表示该模型在客户端被关闭了多模态，优先级高于 supportsImages。
	if v := item.Get("disabledMultimodal"); v.Type == gjson.True {
		supports := false
		capability.SupportsImages = &supports
	}
	for _, v := range item.Get("reasoning.supportedEfforts").Array() {
		if effort := strings.ToLower(strings.TrimSpace(v.String())); effort != "" {
			capability.SupportedEfforts = append(capability.SupportedEfforts, effort)
		}
	}
	if len(capability.SupportedEfforts) > 0 {
		// 新字段 defaultEffort 优先，老模型回落 effort；不在档位表里的默认档不采信。
		for _, key := range []string{"reasoning.defaultEffort", "reasoning.effort"} {
			if def := strings.ToLower(strings.TrimSpace(item.Get(key).String())); def != "" {
				if stringSliceContains(capability.SupportedEfforts, def) {
					capability.DefaultEffort = def
				}
				break
			}
		}
	}
	if capability.IsEmpty() {
		return capability
	}
	capability.DisplayName = strings.TrimSpace(item.Get("name").String())
	return capability
}

// firstPositiveInt 依次读取 keys，返回第一个正整数值（字符串数字也接受）。
// contextWindow 在客户端里可能是对象（{supportedLengths:[...]}），此时跳过。
func firstPositiveInt(item gjson.Result, keys ...string) int64 {
	for _, key := range keys {
		v := item.Get(key)
		if !v.Exists() || v.IsObject() || v.IsArray() {
			continue
		}
		if n := v.Int(); n > 0 {
			return n
		}
	}
	return 0
}

// ===== 拉取 =====

// FetchModelCatalog 拉取模型目录，同时解析模型能力参数。
//
// 目录接口自身没带能力字段的模型，再尽力打一次 /v3/config（官方客户端的产品配置）
// 按 id 补齐；/v3/config 失败只忽略，不影响目录本身。
func (c *TencentCodeBuddyClient) FetchModelCatalog(ctx context.Context, account *Account) ([]string, map[string]TencentCodeBuddyModelCapability, int, error) {
	models, capabilities, status, err := c.fetchModelsWithCapabilities(ctx, account)
	if err != nil || len(models) == 0 {
		return models, capabilities, status, err
	}
	missing := false
	for _, id := range models {
		if _, ok := capabilities[id]; !ok {
			missing = true
			break
		}
	}
	if missing {
		if extra := c.fetchProductConfigCapabilities(ctx, account); len(extra) > 0 {
			if capabilities == nil {
				capabilities = make(map[string]TencentCodeBuddyModelCapability, len(extra))
			}
			for id, capability := range extra {
				capabilities[id] = capabilities[id].mergeMissing(capability)
			}
		}
	}
	return models, filterTencentCodeBuddyCapabilities(models, capabilities), status, nil
}

// filterTencentCodeBuddyCapabilities 只保留目录里存在的模型，避免产品配置里大量无关模型写进快照。
func filterTencentCodeBuddyCapabilities(models []string, capabilities map[string]TencentCodeBuddyModelCapability) map[string]TencentCodeBuddyModelCapability {
	if len(capabilities) == 0 {
		return nil
	}
	out := make(map[string]TencentCodeBuddyModelCapability, len(models))
	for _, id := range models {
		if capability, ok := capabilities[id]; ok && !capability.IsEmpty() {
			out[id] = capability
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fetchProductConfigCapabilities 尽力拉取 /v3/config 并解析其中的模型参数，失败返回 nil。
func (c *TencentCodeBuddyClient) fetchProductConfigCapabilities(ctx context.Context, account *Account) map[string]TencentCodeBuddyModelCapability {
	if !c.configured() || account == nil {
		return nil
	}
	cred := account.TencentCodeBuddyCredential()
	if !cred.HasAccessToken() {
		return nil
	}
	url := strings.TrimRight(cred.Endpoint().Host, "/") + tencentCodeBuddyProductConfigPath
	resp, err := c.do(ctx, account, cred, http.MethodGet, url, nil, false, nil)
	if err != nil || resp == nil || resp.Body == nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, tencentCodeBuddyMaxConfigBody))
	return parseTencentCodeBuddyModelCapabilities(body)
}

// ===== 账号快照 =====

// GetTencentCodeBuddyModelCapabilitySnapshot 读取账号 extra 中的上游能力快照。
func (a *Account) GetTencentCodeBuddyModelCapabilitySnapshot() *TencentCodeBuddyModelCapabilitySnapshot {
	if a == nil || a.Extra == nil {
		return nil
	}
	raw, ok := a.Extra[TencentCodeBuddyModelCapabilitiesExtraKey]
	if !ok || raw == nil {
		return nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot TencentCodeBuddyModelCapabilitySnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil || len(snapshot.Models) == 0 {
		return nil
	}
	return &snapshot
}

// TencentCodeBuddyModelCapability 返回账号对某个上游模型的能力参数与来源：
// 上游快照 > 内置表；两者都没有时返回 (零值, "default")。字段级补齐：
// 快照缺的字段用内置表补。
func (a *Account) TencentCodeBuddyModelCapability(upstreamModel string) (TencentCodeBuddyModelCapability, string) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	builtin, hasBuiltin := tencentCodeBuddyBuiltinCapability(upstreamModel)
	if snapshot := a.GetTencentCodeBuddyModelCapabilitySnapshot(); snapshot != nil {
		if capability, ok := snapshot.Models[upstreamModel]; ok && !capability.IsEmpty() {
			return capability.mergeMissing(builtin), TencentCodeBuddyCapabilitySourceUpstream
		}
	}
	if hasBuiltin {
		return builtin, TencentCodeBuddyCapabilitySourceBuiltin
	}
	return TencentCodeBuddyModelCapability{}, TencentCodeBuddyCapabilitySourceDefault
}

// tencentCodeBuddyUpstreamMaxOutputTokens 返回账号上游快照里报告的输出上限（不含内置表推断值）。
func (a *Account) tencentCodeBuddyUpstreamMaxOutputTokens(upstreamModel string) int64 {
	snapshot := a.GetTencentCodeBuddyModelCapabilitySnapshot()
	if snapshot == nil {
		return 0
	}
	return snapshot.Models[strings.TrimSpace(upstreamModel)].MaxOutputTokens
}

// saveTencentCodeBuddyModelCapabilities 把解析到的能力写入账号快照。capabilities 为空时不写，
// 保留旧快照（上游临时不返回字段时不应抹掉已知数据）。
func saveTencentCodeBuddyModelCapabilities(
	ctx context.Context,
	repo AccountRepository,
	account *Account,
	capabilities map[string]TencentCodeBuddyModelCapability,
) error {
	if repo == nil || account == nil || account.ID <= 0 || len(capabilities) == 0 {
		return nil
	}
	snapshot := TencentCodeBuddyModelCapabilitySnapshot{
		SyncedAt: time.Now().UTC().Format(time.RFC3339),
		Models:   capabilities,
	}
	if err := repo.UpdateExtra(ctx, account.ID, map[string]any{TencentCodeBuddyModelCapabilitiesExtraKey: snapshot}); err != nil {
		return err
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[TencentCodeBuddyModelCapabilitiesExtraKey] = snapshot
	return nil
}

// tencentCodeBuddyCatalogFetcher 是签到服务刷新模型能力所需的能力（*TencentCodeBuddyClient 实现）。
// 单独成接口，测试替身不实现时签到服务直接跳过刷新。
type tencentCodeBuddyCatalogFetcher interface {
	FetchModelCatalog(ctx context.Context, account *Account) ([]string, map[string]TencentCodeBuddyModelCapability, int, error)
}

// refreshTencentCodeBuddyModelCapabilities 拉取目录并写入能力快照。
func refreshTencentCodeBuddyModelCapabilities(ctx context.Context, repo AccountRepository, fetcher tencentCodeBuddyCatalogFetcher, account *Account) error {
	_, capabilities, _, err := fetcher.FetchModelCatalog(ctx, account)
	if err != nil {
		return err
	}
	return saveTencentCodeBuddyModelCapabilities(ctx, repo, account, capabilities)
}

// ===== 分组聚合 =====

// ResolveTencentCodeBuddyGroupModelCapability 在一组账号上聚合某个对外模型的能力：
// 上下文与输出上限取各账号的最小值（保守，避免声明超过某个账号的实际能力），
// 识图要求所有已知账号都支持。来源取"最可信"的一档（upstream > builtin > default）。
func ResolveTencentCodeBuddyGroupModelCapability(accounts []Account, modelID string) (TencentCodeBuddyModelCapability, string) {
	modelID = strings.TrimSpace(modelID)
	var result TencentCodeBuddyModelCapability
	source := ""
	found := false
	for i := range accounts {
		account := &accounts[i]
		if !account.IsTencentCodeBuddy() || !account.IsModelSupported(modelID) {
			continue
		}
		capability, capabilitySource := account.TencentCodeBuddyModelCapability(account.GetMappedModel(modelID))
		if capability.IsEmpty() {
			continue
		}
		if !found {
			result = capability
			source = capabilitySource
			found = true
			continue
		}
		result.ContextWindow = minPositiveInt64(result.ContextWindow, capability.ContextWindow)
		result.MaxOutputTokens = minPositiveInt64(result.MaxOutputTokens, capability.MaxOutputTokens)
		if capability.SupportsImages != nil {
			if result.SupportsImages == nil {
				v := *capability.SupportsImages
				result.SupportsImages = &v
			} else if !*capability.SupportsImages {
				v := false
				result.SupportsImages = &v
			}
		}
		result.SupportedEfforts, result.DefaultEffort = intersectTencentCodeBuddyEfforts(
			result.SupportedEfforts, result.DefaultEffort, capability.SupportedEfforts, capability.DefaultEffort)
		if capabilitySource == TencentCodeBuddyCapabilitySourceUpstream {
			source = capabilitySource
		}
	}
	if !found {
		// 分组里没有账号认领该模型（或都无数据）时，仍可用内置表给出参考值。
		if builtin, ok := tencentCodeBuddyBuiltinCapability(modelID); ok {
			return builtin, TencentCodeBuddyCapabilitySourceBuiltin
		}
		return TencentCodeBuddyModelCapability{}, TencentCodeBuddyCapabilitySourceDefault
	}
	return result, source
}

// intersectTencentCodeBuddyEfforts 合并两个账号的档位声明：取交集（只声明所有账号都接受的档位），
// 有一方未知时沿用另一方。默认档不在交集里时清空。
func intersectTencentCodeBuddyEfforts(a []string, aDefault string, b []string, bDefault string) ([]string, string) {
	if len(a) == 0 {
		return append([]string(nil), b...), bDefault
	}
	if len(b) == 0 {
		return a, aDefault
	}
	out := make([]string, 0, len(a))
	for _, effort := range a {
		if stringSliceContains(b, effort) {
			out = append(out, effort)
		}
	}
	if len(out) == 0 {
		return nil, ""
	}
	if !stringSliceContains(out, aDefault) {
		aDefault = ""
	}
	return out, aDefault
}

// minPositiveInt64 返回两个值中较小的正数；有一方 <=0（未知）时返回另一方。
func minPositiveInt64(a, b int64) int64 {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return min(a, b)
}

// ===== 转发时裁剪输出上限 =====

// tencentCodeBuddyClampMaxOutputTokens 把请求体里超过模型输出上限的 max_tokens /
// max_completion_tokens 改小到上限。
//
// 上限只取可信来源：后台手填（overrideLimit）优先，其次账号上游快照；内置表是推断值，
// 不用于裁剪，避免把回答截短。没有可信上限时原样返回。
func tencentCodeBuddyClampMaxOutputTokens(body []byte, account *Account, overrideLimit int64) []byte {
	if len(body) == 0 || account == nil || !gjson.ValidBytes(body) {
		return body
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	limit := overrideLimit
	if limit <= 0 {
		limit = account.tencentCodeBuddyUpstreamMaxOutputTokens(model)
	}
	if limit <= 0 {
		return body
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		value := gjson.GetBytes(body, field)
		if !value.Exists() || value.Type != gjson.Number || value.Int() <= limit {
			continue
		}
		updated, err := sjson.SetBytes(body, field, limit)
		if err != nil {
			continue
		}
		log.Printf("[CodeBuddy] clamp %s %d -> %d (account=%d model=%s)", field, value.Int(), limit, account.ID, model)
		body = updated
	}
	return body
}

// ResolveTencentCodeBuddyModelCapabilities 为 /v1/models 解析分组内各模型的能力参数
// （不含后台手填覆盖，覆盖由调用方叠加）。groupID 为 nil 时使用全部可调度账号。
func (s *GatewayService) ResolveTencentCodeBuddyModelCapabilities(
	ctx context.Context,
	groupID *int64,
	modelIDs []string,
) map[string]TencentCodeBuddyModelCapability {
	if s == nil || s.accountRepo == nil || len(modelIDs) == 0 {
		return nil
	}
	var accounts []Account
	var err error
	if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	} else {
		accounts, err = s.accountRepo.ListSchedulable(ctx)
	}
	if err != nil {
		accounts = nil
	}
	out := make(map[string]TencentCodeBuddyModelCapability, len(modelIDs))
	for _, modelID := range modelIDs {
		capability, _ := ResolveTencentCodeBuddyGroupModelCapability(accounts, modelID)
		if !capability.IsEmpty() {
			out[modelID] = capability
		}
	}
	return out
}

// tencentCodeBuddyMaxOutputOverride 返回后台「模型能力覆盖」为请求模型手填的输出上限（未填为 0）。
func (s *OpenAIGatewayService) tencentCodeBuddyMaxOutputOverride(ctx context.Context, body []byte) int64 {
	if s == nil || s.settingService == nil {
		return 0
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return 0
	}
	entry, ok := s.settingService.ResolveModelCapability(ctx, PlatformTencentCodeBuddy, model)
	if !ok {
		return 0
	}
	return entry.MaxOutputTokens
}

// ===== 后台展示 =====

// TencentCodeBuddyModelCapabilityRow 是后台「生效参数」表的一行（不含后台手填覆盖，前端叠加）。
type TencentCodeBuddyModelCapabilityRow struct {
	ModelID         string `json:"model_id"`
	ContextWindow   int64  `json:"context_window,omitempty"`
	MaxOutputTokens int64  `json:"max_output_tokens,omitempty"`
	SupportsImages  *bool  `json:"supports_images,omitempty"`
	Source          string `json:"source"`
}

// TencentCodeBuddyModelCapabilityReport 汇总所有 CodeBuddy / WorkBuddy 账号的模型能力。
type TencentCodeBuddyModelCapabilityReport struct {
	Models []TencentCodeBuddyModelCapabilityRow `json:"models"`
	// SyncedAt 是各账号快照中最近一次同步时间；为空表示还没有任何上游快照。
	SyncedAt string `json:"synced_at,omitempty"`
}

// BuildTencentCodeBuddyModelCapabilityReport 列出账号涉及的全部模型及其生效能力。
// 模型集合 = 各账号上游快照 ∪ 模型映射键 ∪ 对应站点的静态兜底目录。
func BuildTencentCodeBuddyModelCapabilityReport(accounts []Account) TencentCodeBuddyModelCapabilityReport {
	modelSet := make(map[string]struct{})
	report := TencentCodeBuddyModelCapabilityReport{}
	codeBuddyAccounts := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if !account.IsTencentCodeBuddy() {
			continue
		}
		codeBuddyAccounts = append(codeBuddyAccounts, *account)
		if snapshot := account.GetTencentCodeBuddyModelCapabilitySnapshot(); snapshot != nil {
			for id := range snapshot.Models {
				modelSet[id] = struct{}{}
			}
			if snapshot.SyncedAt > report.SyncedAt {
				report.SyncedAt = snapshot.SyncedAt
			}
		}
		for id := range account.GetModelMapping() {
			modelSet[id] = struct{}{}
		}
		for _, id := range DefaultTencentCodeBuddyModelIDsForEndpoint(account.GetTencentCodeBuddyEndpoint()) {
			modelSet[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(modelSet))
	for id := range modelSet {
		if strings.TrimSpace(id) != "" && !strings.Contains(id, "*") {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	report.Models = make([]TencentCodeBuddyModelCapabilityRow, 0, len(ids))
	for _, id := range ids {
		capability, source := ResolveTencentCodeBuddyGroupModelCapability(codeBuddyAccounts, id)
		report.Models = append(report.Models, TencentCodeBuddyModelCapabilityRow{
			ModelID:         id,
			ContextWindow:   capability.ContextWindow,
			MaxOutputTokens: capability.MaxOutputTokens,
			SupportsImages:  capability.SupportsImages,
			Source:          source,
		})
	}
	return report
}

// RefreshTencentCodeBuddyModelCapabilities 为全部 active 的 CodeBuddy 账号刷新能力快照，
// 返回成功与失败的账号数。供后台「从上游刷新」按钮使用。
func RefreshTencentCodeBuddyModelCapabilities(ctx context.Context, repo AccountRepository, client *TencentCodeBuddyClient) (int, int, error) {
	if repo == nil || client == nil || !client.configured() {
		return 0, 0, nil
	}
	accounts, err := repo.ListByPlatform(ctx, PlatformTencentCodeBuddy)
	if err != nil {
		return 0, 0, err
	}
	ok, failed := 0, 0
	for i := range accounts {
		account := &accounts[i]
		if !account.IsActive() || !account.TencentCodeBuddyCredential().HasAccessToken() {
			continue
		}
		accountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := refreshTencentCodeBuddyModelCapabilities(accountCtx, repo, client, account); err != nil {
			failed++
			log.Printf("[CodeBuddy] refresh model capabilities for account %d: %v", account.ID, err)
		} else {
			ok++
		}
		cancel()
	}
	return ok, failed, nil
}
