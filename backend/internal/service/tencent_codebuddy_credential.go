package service

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// TencentCodeBuddyCredential 是 CodeBuddy / WorkBuddy 账号凭据的强类型视图。
//
// 存储位置仍是 accounts.credentials(JSONB)——不新增表、不新增列、不绕过既有账号
// 系统。所有 TencentCodeBuddy 业务代码必须经由本结构读写，不再直接下标访问
// credentials["access_token"] 之类的键，避免键名漂移、类型断言与归一化逻辑散落
// 到各处。
//
// 与 NormalizeTencentCodeBuddyCredentials 的分工（两者是同一份键契约的两端）：
//   - Normalize... 是**写入路径**的唯一入口：校验 + 原地归一化（含丢弃 base_url）；
//   - Parse...     是**读取路径**的唯一入口：把 JSONB 映射为结构体并做同样的归一化，
//     但不写回、不因缺 access_token 而失败（是否致命由调用方判断）。
type TencentCodeBuddyCredential struct {
	AccessToken  string
	RefreshToken string
	UserID       string
	EnterpriseID string
	Product      string
	Region       string
	// Domain 是 X-Domain 头的值。它是账号级字段（官方登录态里叫 auth.domain），
	// 不是纯粹的 region 常量：上游刷新可能回传新的 domain，此时以凭据为准。
	// 为空时回落到 region 默认值。
	Domain    string
	ExpiresAt *time.Time
	// IssuedAt 是 access_token（JWT）的签发时间，仅用于判断"今天是否已保活刷新"。
	// 令牌不是 JWT 或没有 iat 时为 nil。
	IssuedAt *time.Time
}

// ParseTencentCodeBuddyCredential 从 accounts.credentials 解析强类型凭据。
// 永不返回错误：缺失字段落到零值，product / region 走与写入路径相同的归一化，
// 因此解析结果一定落在受支持的枚举内；Domain 为空时补 region 默认值。
func ParseTencentCodeBuddyCredential(credentials map[string]any) TencentCodeBuddyCredential {
	product := NormalizeTencentCodeBuddyProduct(credentialString(credentials, tencentCodeBuddyCredProduct))
	region := NormalizeTencentCodeBuddyRegion(credentialString(credentials, tencentCodeBuddyCredRegion))
	domain := strings.TrimSpace(credentialString(credentials, tencentCodeBuddyCredDomain))
	if domain == "" {
		domain = ResolveTencentCodeBuddyEndpoint(product, region).Domain
	}
	accessToken := strings.TrimSpace(credentialString(credentials, tencentCodeBuddyCredAccessToken))
	issuedAt, jwtExpiresAt := tencentCodeBuddyJWTTimes(accessToken)
	expiresAt := credentialTime(credentials, tencentCodeBuddyCredExpiresAt)
	if expiresAt == nil {
		// 扫码登录与编辑表单都没有回传 expires_at，存量账号普遍缺这个字段；
		// 从 JWT 的 exp 兜底，否则这类账号永远不会进入刷新窗口。
		expiresAt = jwtExpiresAt
	}
	return TencentCodeBuddyCredential{
		AccessToken:  accessToken,
		RefreshToken: strings.TrimSpace(credentialString(credentials, tencentCodeBuddyCredRefreshToken)),
		UserID:       strings.TrimSpace(credentialString(credentials, tencentCodeBuddyCredUserID)),
		EnterpriseID: strings.TrimSpace(credentialString(credentials, tencentCodeBuddyCredEnterpriseID)),
		Product:      product,
		Region:       region,
		Domain:       domain,
		ExpiresAt:    expiresAt,
		IssuedAt:     issuedAt,
	}
}

// tencentCodeBuddyJWTTimes 不验签地读取 access_token（JWT）的 iat / exp。
// 只用于刷新调度，不用于鉴权；令牌不是 JWT 或字段缺失时对应返回 nil。
func tencentCodeBuddyJWTTimes(token string) (issuedAt, expiresAt *time.Time) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, nil
	}
	var claims struct {
		IssuedAt  json.Number `json:"iat"`
		ExpiresAt json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, nil
	}
	return jwtNumericDate(claims.IssuedAt), jwtNumericDate(claims.ExpiresAt)
}

func jwtNumericDate(n json.Number) *time.Time {
	sec, err := n.Float64()
	if err != nil || sec <= 0 {
		return nil
	}
	t := time.Unix(int64(sec), 0).UTC()
	return &t
}

// Endpoint 返回该凭据对应的官方接入点（由 product + region 内部决定）。
// 当凭据携带账号级 domain 时，用其覆盖矩阵里的区域默认值。
func (c TencentCodeBuddyCredential) Endpoint() TencentCodeBuddyEndpoint {
	endpoint := ResolveTencentCodeBuddyEndpoint(c.Product, c.Region)
	if c.Domain != "" {
		endpoint.Domain = c.Domain
	}
	return endpoint
}

// HasAccessToken 报告凭据是否具备可用的访问令牌。
func (c TencentCodeBuddyCredential) HasAccessToken() bool {
	return c.AccessToken != ""
}

// tencentCodeBuddyKeepaliveInterval 是保活刷新间隔：access_token 签发超过该时长就刷新。
// 参考 workbuddy2api 每天对全部账号刷新一次；上游实测 expiresIn 为 60 天，
// 但 refresh_token 的闲置失效期未知，按天轮换最稳妥。
const tencentCodeBuddyKeepaliveInterval = 24 * time.Hour

// tencentCodeBuddyMinRefreshWindow 是到期前的最小刷新提前量。全局默认窗口只有
// 30 分钟（为 1 小时令牌设计），对 60 天令牌而言一次刷新失败就会直接过期；
// 拉长到 7 天，失败后还有足够多的周期重试。令牌没有 iat 时它是唯一的刷新依据。
const tencentCodeBuddyMinRefreshWindow = 7 * 24 * time.Hour

// NeedsRefresh 报告是否该刷新：进入到期窗口（至少提前 7 天），或令牌已签发满一天。
// 过期时间与签发时间都未知时返回 false：没有依据就不主动刷新。
func (c TencentCodeBuddyCredential) NeedsRefresh(now time.Time, window time.Duration) bool {
	if window < tencentCodeBuddyMinRefreshWindow {
		window = tencentCodeBuddyMinRefreshWindow
	}
	if c.ExpiresAt != nil && c.ExpiresAt.Sub(now) < window {
		return true
	}
	return c.IssuedAt != nil && now.Sub(*c.IssuedAt) >= tencentCodeBuddyKeepaliveInterval
}

// Apply 返回 base 的副本，并把本结构的字段写回。base 中的其它键（model_mapping、
// header_overrides 等）原样保留，符合"刷新只覆盖 token 相关字段"的既有语义。
func (c TencentCodeBuddyCredential) Apply(base map[string]any) map[string]any {
	out := make(map[string]any, len(base)+6)
	for k, v := range base {
		out[k] = v
	}
	out[tencentCodeBuddyCredAccessToken] = c.AccessToken
	out[tencentCodeBuddyCredRefreshToken] = c.RefreshToken
	out[tencentCodeBuddyCredUserID] = c.UserID
	out[tencentCodeBuddyCredEnterpriseID] = c.EnterpriseID
	out[tencentCodeBuddyCredProduct] = NormalizeTencentCodeBuddyProduct(c.Product)
	out[tencentCodeBuddyCredRegion] = NormalizeTencentCodeBuddyRegion(c.Region)
	if c.ExpiresAt != nil {
		out[tencentCodeBuddyCredExpiresAt] = c.ExpiresAt.UTC().Format(time.RFC3339)
	} else {
		delete(out, tencentCodeBuddyCredExpiresAt)
	}
	// domain 是账号级字段，但只有"偏离 region 默认值"时才落盘：把默认值固化下来
	// 会让用户改了 region 之后 X-Domain 不跟随（显式值优先于新 region 的默认）。
	if domain := strings.TrimSpace(c.Domain); domain != "" &&
		domain != ResolveTencentCodeBuddyEndpoint(c.Product, c.Region).Domain {
		out[tencentCodeBuddyCredDomain] = domain
	} else {
		delete(out, tencentCodeBuddyCredDomain)
	}
	return out
}

// credentialTime 读取 JSONB 中的时间字段，兼容 RFC3339 / Unix 秒 / Unix 毫秒。
// 非法或缺失一律返回 nil，由调用方决定如何处理。
func credentialTime(credentials map[string]any, key string) *time.Time {
	raw := strings.TrimSpace(credentialString(credentials, key))
	if raw == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := t.UTC()
		return &utc
	}
	if ts, err := strconv.ParseInt(raw, 10, 64); err == nil && ts > 0 {
		seconds := ts
		if ts > 1_000_000_000_000 { // 毫秒时间戳
			seconds = ts / 1000
		}
		t := time.Unix(seconds, 0).UTC()
		return &t
	}
	return nil
}
