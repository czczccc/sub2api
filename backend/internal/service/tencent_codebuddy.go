package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// TencentCodeBuddyProvider 是腾讯 CodeBuddy / WorkBuddy 的独立 Provider。
//
// 与普通 OpenAI-Compatible Provider 的关键差异：该 Provider 有明确的产品语义，
// Base URL 不由用户填写，而是由 Provider 依据 product + region 固定官方 endpoint，
// 用户只需提供登录凭据（access_token / refresh_token 等）。凭据会过期，Provider
// 负责 token refresh；模型目录从腾讯接口获取，静态列表仅作兜底。
//
// 上游契约（参考 HanHan666666/codebuddy2openai，并经官方开放平台文档核对）：
//   - 推理：POST {base}/chat/completions（base 已含 /v2）
//   - 刷新：POST {base}/plugin/auth/token/refresh（X-Refresh-Token 头）
//   - 鉴权：Authorization: Bearer <access_token> + X-User-Id / X-Enterprise-Id /
//     X-Tenant-Id / X-Domain
//
// 上游为 Chat Completions 单协议，/v1/messages（Claude Code）与 /v1/responses
// （Codex）入站请求由 OpenAI 网关既有转换链处理。

// product 维度：品牌。CodeBuddy 是编程助手，WorkBuddy 是办公 Agent，
// 两者共用同一套上游协议与鉴权头，只是站点不同。
const (
	TencentCodeBuddyProductCodeBuddy = "codebuddy" // CodeBuddy（编程助手）
	TencentCodeBuddyProductWorkBuddy = "workbuddy" // WorkBuddy（办公 Agent）
)

// region 维度：站点区域。product × region 共同决定上游 host 与 X-Domain。
const (
	TencentCodeBuddyRegionChina  = "china"  // 中国大陆
	TencentCodeBuddyRegionGlobal = "global" // 国际
)

// 固定官方 endpoint（内部常量，不通过账号配置暴露）。
//
// 四个 host 均已实测存在（2026-09-12）：
//   - copilot.tencent.com ：大陆 CodeBuddy API（APISIX 边缘），现有默认；
//   - www.codebuddy.ai    ：国际 CodeBuddy；
//   - www.workbuddy.cn    ：大陆 WorkBuddy；
//   - www.workbuddy.ai    ：国际 WorkBuddy（TencentEdgeOne 边缘）。
//
// 全部站点的路径结构完全同构（/v2/chat/completions、/v2/plugin/auth/*、
// /v2/plugin/login/account、/v3/config），区别只在 host 与 X-Domain。
// 设备授权返回的 authUrl 由站点自身派生，因此选对 host 就不会串站。
//
// 注意：不存在 copilot.workbuddy.ai / api.workbuddy.ai 子域（TLS 握手失败），
// API host 就是站点本身。
const (
	// 大陆 CodeBuddy：参考实现 codebuddy2openai 硬编码该 host。
	tencentCodeBuddyAPIHost = "https://copilot.tencent.com"
	tencentCodeBuddyAPIRoot = tencentCodeBuddyAPIHost + "/v2"

	// X-Domain 为各站点默认值（账号级 domain 可覆盖）。
	tencentCodeBuddyDomain = "www.codebuddy.cn"

	tencentCodeBuddyAPIHostIntl  = "https://www.codebuddy.ai"
	tencentCodeBuddyDomainIntl   = "www.codebuddy.ai"
	tencentWorkBuddyAPIHostChina = "https://www.workbuddy.cn"
	tencentWorkBuddyDomainChina  = "www.workbuddy.cn"
	tencentWorkBuddyAPIHostIntl  = "https://www.workbuddy.ai"
	tencentWorkBuddyDomainIntl   = "www.workbuddy.ai"

	// 以下两个路径相对于 APIRoot（含 /v2）。
	tencentCodeBuddyTokenRefreshPath    = "/plugin/auth/token/refresh"
	tencentCodeBuddyChatCompletionsPath = "/chat/completions"
	// 模型目录接口。挂在 host 根上，**不在 /v2 前缀之外**——注意下面的路径自带 /v2。
	//
	// 主路径取自**官方客户端**（WorkBuddy / CodeBuddy 桌面端与 CLI）：其
	// daemon.cloudAgent 的 listAvailableModels 调用的就是
	// GET {endpoint}/v2/enterprises/personal/models，响应为
	// {code,msg,data:{models:[{id,...}]}}（客户端取 json.data）。
	//
	// 国际站实测（2026-09-12）：/v2/enterprises/personal/models 在大陆与国际站点
	// 都稳定返回 401（未带令牌），是行为一致的 API 路由；而参考实现用的老路径
	// /console/enterprises/personal/models 在 www.workbuddy.ai 上带令牌恒返回
	// APISIX 原始 500（无令牌 302，是网页控制台路由）。因此以 /v2 为主、
	// /console 仅作兜底。
	tencentCodeBuddyModelsPath       = "/v2/enterprises/personal/models"
	tencentCodeBuddyModelsPathLegacy = "/console/enterprises/personal/models"

	// 设备授权流（向导式获取凭据），三个路径都相对于 APIRoot。
	// 依据 Sliverkiss/workbuddy2api 的 cmd/login（CN realm only，无 PKCE，state 由服务端签发）。
	tencentCodeBuddyAuthStatePath    = "/plugin/auth/state"
	tencentCodeBuddyAuthTokenPath    = "/plugin/auth/token"
	tencentCodeBuddyLoginAccountPath = "/plugin/login/account"
	tencentCodeBuddyAuthPlatform     = "CLI"

	// 出站 UA 对齐官方 WorkBuddy 桌面端（RestOperations 层形状
	// `WorkBuddy/<客户端版本> <平台>/<客户端版本> CLI/<CLI 版本>`），版本取自
	// workbuddy2api 逆向的桌面端 5.5.4 / 内置 CLI 2.137.1。官方客户端不做 UA 随机化。
	// 国际版平台段是 `WorkBuddy AI`，送错可能触发 11140 request illegal。
	tencentWorkBuddyClientVersion = "5.5.4"
	tencentWorkBuddyCLIVersion    = "2.137.1"

	tencentCodeBuddyDefaultModel    = "auto"
	tencentCodeBuddyDefaultTimeout  = 15 * time.Second
	tencentCodeBuddyMaxModelsBody   = 512 * 1024
	tencentCodeBuddyMaxRefreshBytes = 256 * 1024
)

// credentials 键。写入路径（NormalizeTencentCodeBuddyCredentials）与读取路径
// （ParseTencentCodeBuddyCredential）共用这一份键契约。
const (
	tencentCodeBuddyCredAccessToken  = "access_token"
	tencentCodeBuddyCredRefreshToken = "refresh_token"
	tencentCodeBuddyCredProduct      = "product"
	tencentCodeBuddyCredRegion       = "region"
	tencentCodeBuddyCredUserID       = "uid"
	tencentCodeBuddyCredEnterpriseID = "enterprise_id"
	tencentCodeBuddyCredDomain       = "domain"
	tencentCodeBuddyCredExpiresAt    = "expires_at"
)

// TencentCodeBuddyEndpoint 是 Provider 依据 product + region 解析出的官方接入点。
type TencentCodeBuddyEndpoint struct {
	Product string
	Region  string
	// Host 是站点根（不含 /v2）：模型目录等挂在 host 根上的接口用它拼接。
	Host string
	// BaseURL 是 API root（Host + /v2），chat/completions 与 plugin/auth/* 都相对它。
	BaseURL string
	Domain  string
}

// TencentCodeBuddyProducts 返回受支持的 product 列表。
func TencentCodeBuddyProducts() []string {
	return []string{TencentCodeBuddyProductCodeBuddy, TencentCodeBuddyProductWorkBuddy}
}

// TencentCodeBuddyRegions 返回受支持的 region 列表。
func TencentCodeBuddyRegions() []string {
	return []string{TencentCodeBuddyRegionChina, TencentCodeBuddyRegionGlobal}
}

// NormalizeTencentCodeBuddyProduct 归一化 product。空值与未知取值回落到 codebuddy
// （与历史默认一致，保证存量账号语义不变）；workbuddy 是合法取值，不再被收敛。
func NormalizeTencentCodeBuddyProduct(product string) string {
	switch strings.ToLower(strings.TrimSpace(product)) {
	case TencentCodeBuddyProductWorkBuddy:
		return TencentCodeBuddyProductWorkBuddy
	default:
		return TencentCodeBuddyProductCodeBuddy
	}
}

// NormalizeTencentCodeBuddyRegion 归一化 region。空值与未知取值回落到 china
// （与历史默认一致）；global 是合法取值，不再被收敛。
func NormalizeTencentCodeBuddyRegion(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case TencentCodeBuddyRegionGlobal:
		return TencentCodeBuddyRegionGlobal
	default:
		return TencentCodeBuddyRegionChina
	}
}

// IsTencentCodeBuddyProduct 报告 product 是否为受支持的字面值（codebuddy / workbuddy）。
// 注意不要复用 Normalize*：归一化会把未知输入收敛成 codebuddy，那样这里会误判为真。
func IsTencentCodeBuddyProduct(product string) bool {
	switch strings.ToLower(strings.TrimSpace(product)) {
	case TencentCodeBuddyProductCodeBuddy, TencentCodeBuddyProductWorkBuddy:
		return true
	default:
		return false
	}
}

// IsTencentCodeBuddyRegion 报告 region 是否为受支持的字面值（china / global）。
func IsTencentCodeBuddyRegion(region string) bool {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case TencentCodeBuddyRegionChina, TencentCodeBuddyRegionGlobal:
		return true
	default:
		return false
	}
}

// TencentCodeBuddyDefaultDomain 返回默认 X-Domain（大陆 CodeBuddy）。
// 其余 product × region 组合的默认值见 tencentCodeBuddySite 矩阵。
func TencentCodeBuddyDefaultDomain() string {
	return tencentCodeBuddyDomain
}

// tencentCodeBuddySite 是 product × region 对应的站点定义。
type tencentCodeBuddySite struct {
	Host   string
	Domain string
}

// tencentCodeBuddySites 是 product × region 的完整站点矩阵。
//
// 四个组合各自独立：WorkBuddy 与 CodeBuddy 是不同产品线，大陆与国际是不同站点，
// 不能按 region 单独推导（workbuddy/cn 的 host 是 www.workbuddy.cn，而非
// copilot.tencent.com）。全部 host 均已实测存在。
var tencentCodeBuddySites = map[string]map[string]tencentCodeBuddySite{
	TencentCodeBuddyProductCodeBuddy: {
		TencentCodeBuddyRegionChina:  {Host: tencentCodeBuddyAPIHost, Domain: tencentCodeBuddyDomain},
		TencentCodeBuddyRegionGlobal: {Host: tencentCodeBuddyAPIHostIntl, Domain: tencentCodeBuddyDomainIntl},
	},
	TencentCodeBuddyProductWorkBuddy: {
		TencentCodeBuddyRegionChina:  {Host: tencentWorkBuddyAPIHostChina, Domain: tencentWorkBuddyDomainChina},
		TencentCodeBuddyRegionGlobal: {Host: tencentWorkBuddyAPIHostIntl, Domain: tencentWorkBuddyDomainIntl},
	},
}

// ResolveTencentCodeBuddyEndpoint 返回官方接入点。
//
// endpoint 是本模块的**唯一来源**：账号配置不接受 base_url，用户无法覆盖；唯一的
// 覆盖点是账号级 credentials.domain，且它只影响 X-Domain 头（见 credential.Endpoint）。
//
// product / region 先归一化，因此未知取值会回落到 codebuddy × china，
// 与接入国际版之前的既有行为一致。
func ResolveTencentCodeBuddyEndpoint(product, region string) TencentCodeBuddyEndpoint {
	normalizedProduct := NormalizeTencentCodeBuddyProduct(product)
	normalizedRegion := NormalizeTencentCodeBuddyRegion(region)
	site, ok := tencentCodeBuddySites[normalizedProduct][normalizedRegion]
	if !ok {
		// 归一化保证命中；此处仅为防御性兜底，保持"永不为零值"的契约。
		site = tencentCodeBuddySite{Host: tencentCodeBuddyAPIHost, Domain: tencentCodeBuddyDomain}
	}
	return TencentCodeBuddyEndpoint{
		Product: normalizedProduct,
		Region:  normalizedRegion,
		Host:    site.Host,
		BaseURL: site.Host + "/v2",
		Domain:  site.Domain,
	}
}

// DefaultTencentCodeBuddyModelIDs 是 CodeBuddy 的静态兜底模型目录（仅在拉取上游
// 实时目录失败时使用），也用于后台白名单预填建议。
//
// 取自真实订阅账号的 /console/enterprises/personal/models 实测结果。注意该目录随订阅与
// 上游发版变化，因此运行时始终优先使用上游实时目录（见 FetchModelIDs）。
func DefaultTencentCodeBuddyModelIDs() []string {
	return []string{
		"auto",
		"hy4-preview",
		"hy3",
		"hy3-x",
		"deepseek-v4.1-flash",
		"deepseek-v4-pro",
		"glm-5.3",
		"glm-5.3-flash",
		"glm-5.2",
		"glm-5.1",
		"glm-5v-turbo",
		"kimi-k3-1",
		"kimi-k2.7",
		"kimi-k2.6",
		"minimax-m3",
	}
}

// DefaultTencentWorkBuddyModelIDs 是 WorkBuddy（国际版 workbuddy.ai）的静态兜底模型目录。
//
// 基础目录实测来源（2026-09-12，真实 workbuddy.ai 账号）：
// GET {host}/v2/enterprises/personal/models → data.models[].id，共 18 项。
// deepseek-v4.1-flash 根据 WorkBuddy 客户端可用模型补入静态兜底目录。
// 同一响应的 data.models[].credits 就是客户端模型选择器里显示的倍率（如 glm-5.3 = x0.79），
// data.modelPromotions 是 "Free now" 角标的来源。
//
// ⚠️ 该目录**随账号/套餐/时间变化**（促销与可用模型由服务端下发），所以：
//   - 运行时永远优先用上游实时目录（见 FetchModelIDs / 模型同步）；
//   - 本表只是"拉不到实时目录"时的快照兜底，可能落后；
//   - 国际站与大陆站的目录**不重合**（大陆见 DefaultTencentCodeBuddyModelIDs）。
//
// 如需刷新：用账号跑一次模型同步（或用 curl 打上面的接口）后更新本表，
// 并同步 frontend/src/composables/useModelWhitelist.ts。
func DefaultTencentWorkBuddyModelIDs() []string {
	return []string{
		"default-model",
		"fast-model",
		"balanced-model",
		"primary-model",
		"deep-model",
		"hy4-preview",
		"hy3",
		"deepseek-v4.1-flash",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.4",
		"gpt-5.3-codex",
		"gemini-3.5-flash",
		"glm-5.3",
		"glm-5.2",
		"kimi-k3",
		"kimi-k2.6",
	}
}

// DefaultTencentCodeBuddyModelIDsForEndpoint 按账号的 product / region 返回静态兜底目录。
// 未知取值回落到 CodeBuddy 大陆版（与归一化默认一致）。
func DefaultTencentCodeBuddyModelIDsForEndpoint(endpoint TencentCodeBuddyEndpoint) []string {
	if endpoint.Product == TencentCodeBuddyProductWorkBuddy {
		return DefaultTencentWorkBuddyModelIDs()
	}
	return DefaultTencentCodeBuddyModelIDs()
}

// TencentCodeBuddyDefaultModel 返回未指定模型时的兜底模型。
func TencentCodeBuddyDefaultModel() string { return tencentCodeBuddyDefaultModel }

// ===== Account 便捷方法（委托 Provider 语义） =====

// IsTencentCodeBuddy 报告账号是否属于 TencentCodeBuddyProvider。
func (a *Account) IsTencentCodeBuddy() bool {
	return a != nil && a.Platform == PlatformTencentCodeBuddy
}

// CodeBuddyDefaultInputModalities 是 CodeBuddy 模型对外声明的默认输入模态。
//
// 依据（2026-09-12 实测 15/15 模型）：腾讯上游接受 OpenAI 形状的
// messages[].content 图片部分，data URL 与公网 URL 两种形态都能被正确识别；
// 同问题不带图的对照组给出错误答案，确认模型确实看到了图片。
//
// 这是**默认值**而不是硬约束：/v1/models 与 Codex manifest 都优先采用管理员在
// 「模型能力覆盖」里配置的 input_modalities，未配置时才回落到这里。
func CodeBuddyDefaultInputModalities() []string {
	return []string{"text", "image"}
}

// TencentCodeBuddyCredential 返回账号凭据的强类型视图（读取路径唯一入口）。
func (a *Account) TencentCodeBuddyCredential() TencentCodeBuddyCredential {
	if a == nil {
		return ParseTencentCodeBuddyCredential(nil)
	}
	return ParseTencentCodeBuddyCredential(a.Credentials)
}

// GetTencentCodeBuddyEndpoint 返回账号解析后的官方接入点。
func (a *Account) GetTencentCodeBuddyEndpoint() TencentCodeBuddyEndpoint {
	return a.TencentCodeBuddyCredential().Endpoint()
}

// GetTencentCodeBuddyProduct 返回账号 product（workbuddy / codebuddy）。
func (a *Account) GetTencentCodeBuddyProduct() string {
	return a.TencentCodeBuddyCredential().Product
}

// GetTencentCodeBuddyRegion 返回账号 region（global / china）。
func (a *Account) GetTencentCodeBuddyRegion() string {
	return a.TencentCodeBuddyCredential().Region
}

// TencentCodeBuddyBaseURL 返回区域固定官方 base（含 /v2）。不接受账号级覆盖。
func (a *Account) TencentCodeBuddyBaseURL() string {
	return a.TencentCodeBuddyCredential().Endpoint().BaseURL
}

// GetTencentCodeBuddyAccessToken 返回 Bearer 访问令牌。
func (a *Account) GetTencentCodeBuddyAccessToken() string {
	return a.TencentCodeBuddyCredential().AccessToken
}

// GetTencentCodeBuddyRefreshToken 返回刷新令牌。
func (a *Account) GetTencentCodeBuddyRefreshToken() string {
	return a.TencentCodeBuddyCredential().RefreshToken
}

// GetTencentCodeBuddyUserID 返回 X-User-Id 对应的用户 ID。
func (a *Account) GetTencentCodeBuddyUserID() string {
	return a.TencentCodeBuddyCredential().UserID
}

// GetTencentCodeBuddyEnterpriseID 返回 X-Enterprise-Id / X-Tenant-Id 对应的企业 ID。
func (a *Account) GetTencentCodeBuddyEnterpriseID() string {
	return a.TencentCodeBuddyCredential().EnterpriseID
}

// TencentCodeBuddyTokenRefreshURL 返回区域对应的刷新端点。
func (a *Account) TencentCodeBuddyTokenRefreshURL() string {
	return strings.TrimRight(a.TencentCodeBuddyBaseURL(), "/") + tencentCodeBuddyTokenRefreshPath
}

// applyTencentCodeBuddyHeaders 注入上游身份头。Authorization 由转发层以
// Bearer <access_token> 统一写入；这里补齐用户 / 企业 / 域名 / UA 头。
//
// 实现委托给 TencentCodeBuddyClient：鉴权/身份头只有一处实现，网关只保留平台
// 判定这一行钩子，不承载任何腾讯协议细节。
func applyTencentCodeBuddyHeaders(h http.Header, account *Account) {
	if h == nil || account == nil {
		return
	}
	builder := newTencentCodeBuddyHeaderBuilder()
	builder.ApplyIdentityHeaders(h, account.TencentCodeBuddyCredential())
	builder.ApplyChatAttributionHeaders(h)
}

// NormalizeTencentCodeBuddyCredentials 校验并原地归一化凭据。
// 只接受 apikey 类型；access_token 必填；product / region 归一化写回；
// base_url 一律丢弃（endpoint 由 Provider 固定）。
func NormalizeTencentCodeBuddyCredentials(accountType string, credentials map[string]any) error {
	if accountType != AccountTypeAPIKey {
		return infraerrors.Newf(http.StatusBadRequest, "TENCENT_CODEBUDDY_INVALID_ACCOUNT_TYPE",
			"codebuddy accounts must use type %q, got %q", AccountTypeAPIKey, accountType)
	}
	if credentials == nil {
		return infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_MISSING_CREDENTIALS", "credentials are required")
	}
	accessToken := strings.TrimSpace(credentialString(credentials, tencentCodeBuddyCredAccessToken))
	if accessToken == "" {
		return infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN", "credentials.access_token is required")
	}
	// 归一化同样经由强类型凭据：写入路径与读取路径共用 Parse/Apply 这一份键契约，
	// 避免"读走结构、写走下标"两套逻辑漂移。
	parsed := ParseTencentCodeBuddyCredential(credentials)
	for key, value := range parsed.Apply(nil) {
		credentials[key] = value
	}
	// endpoint 固定，忽略并清理任何用户提供的 base_url。
	delete(credentials, "base_url")
	return nil
}

// tencentCodeBuddyCredentialKeys 是 CodeBuddy 专属凭据字段。
// 用于判定批量增量是否携带 CodeBuddy 更新意图——base_url 是多平台共用键，
// 单独出现不构成 CodeBuddy 意图，因此不在本列表中。
var tencentCodeBuddyCredentialKeys = []string{
	tencentCodeBuddyCredAccessToken,
	tencentCodeBuddyCredRefreshToken,
	tencentCodeBuddyCredProduct,
	tencentCodeBuddyCredRegion,
	tencentCodeBuddyCredUserID,
	tencentCodeBuddyCredEnterpriseID,
}

// HasTencentCodeBuddyCredentialKeys 报告批量增量是否携带 CodeBuddy 凭据字段。
// 批量路径据此决定是否触发 CodeBuddy 校验，避免与之无关的更新（model_mapping、
// api_key 等）被卷入。
func HasTencentCodeBuddyCredentialKeys(credentials map[string]any) bool {
	for _, key := range tencentCodeBuddyCredentialKeys {
		if _, ok := credentials[key]; ok {
			return true
		}
	}
	return false
}

// NormalizeTencentCodeBuddyCredentialUpdate 归一化批量更新（JSONB 顶层 key 合并）的增量。
//
// 批量写入语义是 credentials = credentials || increment，增量通常不含 access_token，
// 无法直接交给要求"完整凭据集"的 NormalizeTencentCodeBuddyCredentials。这里先用 base
// （目标账号既有凭据）补全成完整集合、仍走同一入口归一化，再把增量自身携带的键写回：
//
//   - 归一化规则只有 NormalizeTencentCodeBuddyCredentials 一处，本函数不含规则副本；
//   - 只写回增量覆盖的键，不会把 base 中该目标独有的值（如各自的 product）带进共享增量。
func NormalizeTencentCodeBuddyCredentialUpdate(accountType string, increment, base map[string]any) error {
	if len(increment) == 0 {
		return nil
	}
	merged := mergeMap(base, increment)
	if err := NormalizeTencentCodeBuddyCredentials(accountType, merged); err != nil {
		return err
	}
	for _, key := range tencentCodeBuddyCredentialKeys {
		if _, ok := increment[key]; !ok {
			continue
		}
		increment[key] = merged[key]
	}
	// base_url 由入口函数丢弃（endpoint 由 product + region 固定），增量携带时同步删除。
	delete(increment, "base_url")
	return nil
}

// credentialString 从未知类型的凭据值中提取字符串。
func credentialString(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	v, ok := credentials[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ===== Provider 运行时能力：token refresh / 模型目录 / 推理转发 =====

// TencentCodeBuddyProvider 是 CodeBuddy / WorkBuddy 的运行时能力聚合点。
//
// 它不实现任何 HTTP 细节：请求构造、鉴权头、令牌注入与响应解析全部由
// TencentCodeBuddyClient 承担。Provider 只负责平台语义编排，并向
// TokenRefreshService 与网关暴露稳定入口。
type TencentCodeBuddyProvider struct {
	client *TencentCodeBuddyClient
	tasks  *TencentCodeBuddyDailyTaskService
}

// SetDailyTasks 挂上日常保号任务服务，供管理端“立即执行”复用。
func (p *TencentCodeBuddyProvider) SetDailyTasks(tasks *TencentCodeBuddyDailyTaskService) {
	if p != nil {
		p.tasks = tasks
	}
}

// DailyTasks 返回日常保号任务服务，未启用时为 nil。
func (p *TencentCodeBuddyProvider) DailyTasks() *TencentCodeBuddyDailyTaskService {
	if p == nil {
		return nil
	}
	return p.tasks
}

// NewTencentCodeBuddyProvider 构造 Provider。
func NewTencentCodeBuddyProvider(httpUpstream HTTPUpstream) *TencentCodeBuddyProvider {
	return &TencentCodeBuddyProvider{client: NewTencentCodeBuddyClient(httpUpstream)}
}

// Client 返回底层客户端，供同包内的其它组件（网关身份头注入、模型目录拉取）
// 复用同一套请求构造逻辑。
func (p *TencentCodeBuddyProvider) Client() *TencentCodeBuddyClient {
	if p == nil {
		return nil
	}
	return p.client
}

// Refresh 调用官方刷新接口获取新 access_token 并返回更新后的 credentials。
// 保留原有字段，仅覆盖 token 相关字段。
//
// 并发安全：由 TokenRefreshService 的分布式锁保证同一账号不会被并发刷新；
// 本方法与 Provider 自身都不持有可变状态。
func (p *TencentCodeBuddyProvider) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if p == nil || p.client == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED",
			"tencent codebuddy provider is not configured")
	}
	if account == nil {
		return nil, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_NIL_ACCOUNT", "account is required")
	}
	updated, err := p.client.RefreshToken(ctx, account)
	if err != nil {
		return nil, err
	}
	return updated.Apply(account.Credentials), nil
}

// ChatCompletion 把一次已符合上游契约的 Chat Completions 请求转发到腾讯。
// 返回原始响应，由调用方负责读取或做 SSE 流式转发。
func (p *TencentCodeBuddyProvider) ChatCompletion(ctx context.Context, account *Account, body []byte, stream bool) (*http.Response, error) {
	if p == nil || p.client == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED",
			"tencent codebuddy provider is not configured")
	}
	if account == nil {
		return nil, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_NIL_ACCOUNT", "account is required")
	}
	return p.client.ChatCompletion(ctx, account, body, stream)
}

// StartAuthSession 申请一次设备授权会话（向导式获取凭据的第一步）。
// 不绑定任何账号：上游 /plugin/auth/state 是公开端点。
//
// product / region 决定授权链接落在哪个站点（CodeBuddy/WorkBuddy × 大陆/国际）。
// 归一化由 ResolveTencentCodeBuddyEndpoint 内部完成，未知取值回落大陆 CodeBuddy。
func (p *TencentCodeBuddyProvider) StartAuthSession(ctx context.Context, product, region string) (TencentCodeBuddyAuthSession, error) {
	if p == nil || p.client == nil {
		return TencentCodeBuddyAuthSession{}, infraerrors.New(http.StatusInternalServerError,
			"TENCENT_CODEBUDDY_NOT_CONFIGURED", "tencent codebuddy provider is not configured")
	}
	return p.client.StartAuthSession(ctx, product, region)
}

// PollAuthSession 轮询授权结果；未完成时返回 ErrTencentCodeBuddyAuthPending。
// product / region 必须与 StartAuthSession 一致，否则会去错误的站点取凭据。
func (p *TencentCodeBuddyProvider) PollAuthSession(ctx context.Context, state, product, region string) (TencentCodeBuddyAuthResult, error) {
	if p == nil || p.client == nil {
		return TencentCodeBuddyAuthResult{}, infraerrors.New(http.StatusInternalServerError,
			"TENCENT_CODEBUDDY_NOT_CONFIGURED", "tencent codebuddy provider is not configured")
	}
	return p.client.PollAuthSession(ctx, state, product, region)
}

// FetchModelIDs 从腾讯接口拉取最新模型目录，失败或为空时回退静态列表。
// 兼容两种响应形态：OpenAI 风格 {"data":[{"id":...}]} 与
// CodeBuddy 风格 {"code":0,"data":[{"modelId"/"id":...}]}。
func (p *TencentCodeBuddyProvider) FetchModelIDs(ctx context.Context, account *Account) ([]string, error) {
	fallback := DefaultTencentCodeBuddyModelIDs()
	if account != nil {
		fallback = DefaultTencentCodeBuddyModelIDsForEndpoint(account.GetTencentCodeBuddyEndpoint())
	}
	if p == nil || p.client == nil || !p.client.configured() {
		return fallback, nil
	}
	models, err := p.client.FetchModels(ctx, account)
	if err != nil {
		return fallback, err
	}
	if len(models) == 0 {
		return fallback, nil
	}
	return models, nil
}

// parseTencentCodeBuddyModelIDsForProduct 解析对应产品的上游模型目录。国际版 WorkBuddy
// 的 data.models 是客户端模型选择器使用的实时目录；大陆 CodeBuddy 则以 cli agent
// 的 models 为可用清单，不能把两种产品的目录语义混为一谈。
//
// 上游真实形态（见 tencentCodeBuddyModelsPath 注释）：
//
//	{"code":0,"data":{"models":[{"id":"...","disabled":false}],
//	                   "agents":[{"name":"cli","models":["id", ...]}]}}
//
// CLI agent 的 models 是可用清单，models[] 提供 disabled 元信息。为了让实现对
// 上游形态变化更耐受，同时接受 OpenAI 风格 {"data":[{"id":...}]} 与纯字符串数组。
func parseTencentCodeBuddyModelIDsForProduct(body []byte, product string) []string {
	var payload struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data) == 0 {
		return nil
	}

	// 1) 数组形态：OpenAI 风格 / 纯字符串数组。
	var items []any
	if err := json.Unmarshal(payload.Data, &items); err == nil && len(items) > 0 {
		if ids := tencentCodeBuddyModelIDsFromItems(items); len(ids) > 0 {
			return ids
		}
	}

	// 2) 对象形态：上游 {models, agents}。
	var envelope struct {
		Models []struct {
			ID       string `json:"id"`
			Disabled bool   `json:"disabled"`
		} `json:"models"`
		Agents []struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(payload.Data, &envelope); err != nil {
		return nil
	}

	known := make(map[string]struct{}, len(envelope.Models))
	disabled := make(map[string]struct{}, len(envelope.Models))
	for _, model := range envelope.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		known[id] = struct{}{}
		if model.Disabled {
			disabled[id] = struct{}{}
		}
	}

	var candidates []string
	if NormalizeTencentCodeBuddyProduct(product) == TencentCodeBuddyProductWorkBuddy && len(known) > 0 {
		// WorkBuddy 的 data.models 是实际提供给国际版客户端的模型目录。即使同一
		// 响应附带了较窄的 cli agent.models，也不能用后者过滤掉客户端可选模型。
		for id := range known {
			candidates = append(candidates, id)
		}
		sort.Strings(candidates)
	} else {
		for _, agent := range envelope.Agents {
			if strings.EqualFold(strings.TrimSpace(agent.Name), "cli") {
				candidates = append(candidates, agent.Models...)
			}
		}
		// 没有 cli agent 时退化为"全部已知模型"，好过直接回落静态列表。
		if len(candidates) == 0 {
			candidates = make([]string, 0, len(known))
			for id := range known {
				candidates = append(candidates, id)
			}
			sort.Strings(candidates)
		}
	}

	seen := make(map[string]struct{}, len(candidates))
	ids := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, bad := disabled[id]; bad {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// tencentCodeBuddyModelIDsFromItems 处理数组形态的模型条目（字符串或对象）。
func tencentCodeBuddyModelIDsFromItems(items []any) []string {
	seen := make(map[string]struct{}, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		var id string
		switch typed := item.(type) {
		case string:
			id = strings.TrimSpace(typed)
		case map[string]any:
			for _, key := range []string{"id", "modelId", "model"} {
				if candidate := strings.TrimSpace(credentialString(typed, key)); candidate != "" {
					id = candidate
					break
				}
			}
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}
