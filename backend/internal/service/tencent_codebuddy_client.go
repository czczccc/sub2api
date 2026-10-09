package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// TencentCodeBuddyClient 是腾讯 CodeBuddy / WorkBuddy 上游的专用 HTTP 客户端。
//
// 职责边界（对应「不要把腾讯协议转换代码写进 Gateway」）：
//   - 在官方 endpoint 上构造请求——base_url 只能来自内部 product×region 矩阵；
//   - 集中生成鉴权与身份头（Authorization / X-User-Id / X-Enterprise-Id /
//     X-Tenant-Id / X-Domain / User-Agent）；
//   - 注入 access_token 并复用账号代理；
//   - 解析官方 {code,msg,data} 响应封套与错误码。
//
// 出网统一走 HTTPUpstream，因此代理绑定、TLS 指纹、账号级并发计数与其它平台完全
// 一致，不引入第二条出网通道。
type TencentCodeBuddyClient struct {
	httpUpstream HTTPUpstream
	timeout      time.Duration
}

// NewTencentCodeBuddyClient 构造客户端。httpUpstream 允许为 nil：此时仅能用于构造
// 请求头（网关侧身份头注入），所有出网方法会返回 NOT_CONFIGURED。
func NewTencentCodeBuddyClient(httpUpstream HTTPUpstream) *TencentCodeBuddyClient {
	return &TencentCodeBuddyClient{httpUpstream: httpUpstream, timeout: tencentCodeBuddyDefaultTimeout}
}

// newTencentCodeBuddyHeaderBuilder 返回只用于构造请求头的无出网客户端。
func newTencentCodeBuddyHeaderBuilder() *TencentCodeBuddyClient {
	return NewTencentCodeBuddyClient(nil)
}

// configured 报告客户端是否具备出网能力。
func (c *TencentCodeBuddyClient) configured() bool {
	return c != nil && c.httpUpstream != nil
}

// requireAccount 校验账号入参。必须在读取凭据之前调用，否则 nil 账号会被
// 报成"缺令牌"而不是"缺账号"，掩盖调用方的参数错误。
func (c *TencentCodeBuddyClient) requireAccount(account *Account) error {
	if account == nil {
		return infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_NIL_ACCOUNT", "account is required")
	}
	return nil
}

// ===== Header 构造（全模块唯一实现） =====

// ApplyIdentityHeaders 写入产品身份头。Authorization 不含在内：转发用
// access_token、刷新还要带 X-Refresh-Token，两者的语义不同，由调用方显式选择。
func (c *TencentCodeBuddyClient) ApplyIdentityHeaders(h http.Header, cred TencentCodeBuddyCredential) {
	if h == nil {
		return
	}
	h.Set("User-Agent", tencentCodeBuddyUserAgent)
	h.Set("X-Domain", cred.Endpoint().Domain)
	if cred.UserID != "" {
		h.Set("X-User-Id", cred.UserID)
	}
	if cred.EnterpriseID != "" {
		h.Set("X-Enterprise-Id", cred.EnterpriseID)
		h.Set("X-Tenant-Id", cred.EnterpriseID)
	}
}

// ApplyBearer 写入 Authorization: Bearer <access_token>。空令牌不写头，避免发出
// "Bearer " 这种上游必然拒绝的请求。
func (c *TencentCodeBuddyClient) ApplyBearer(h http.Header, cred TencentCodeBuddyCredential) {
	if h == nil || !cred.HasAccessToken() {
		return
	}
	h.Set("Authorization", "Bearer "+cred.AccessToken)
}

// ===== 出网 =====

// tencentCodeBuddyCancelBody 让"元数据类请求"的超时生命周期跟随响应体关闭。
// 若在 do() 里直接 defer cancel()，函数一返回就会取消请求上下文，调用方随后读取
// Body 会立刻失败。流式请求不设该超时（由网关侧的读写超时负责）。
type tencentCodeBuddyCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *tencentCodeBuddyCancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// do 构造并发送一次上游请求。extra 用于刷新接口这类需要额外头的调用。
func (c *TencentCodeBuddyClient) do(
	ctx context.Context,
	account *Account,
	cred TencentCodeBuddyCredential,
	method, url string,
	body []byte,
	stream bool,
	extra map[string]string,
) (*http.Response, error) {
	if c == nil || c.httpUpstream == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED",
			"tencent codebuddy provider is not configured")
	}
	if account == nil {
		return nil, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_NIL_ACCOUNT", "account is required")
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if !stream && c.timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}

	req, err := http.NewRequestWithContext(callCtx, method, url, bytes.NewReader(body))
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, infraerrors.Newf(http.StatusInternalServerError, "TENCENT_CODEBUDDY_REQUEST_BUILD_FAILED",
			"build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	c.ApplyBearer(req.Header, cred)
	c.ApplyIdentityHeaders(req.Header, cred)
	for key, value := range extra {
		req.Header.Set(key, value)
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := c.httpUpstream.Do(req, proxyURL, account.ID, maxInt(account.Concurrency, 1))
	if err == nil && resp == nil {
		err = infraerrors.New(http.StatusBadGateway, "TENCENT_CODEBUDDY_EMPTY_RESPONSE", "upstream returned no response")
	}
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	if cancel != nil {
		resp.Body = &tencentCodeBuddyCancelBody{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

// ChatCompletion 调用官方推理端点 {base}/chat/completions。
//
// body 必须已是上游契约的 JSON：入站协议差异（/v1/responses、/v1/messages）由既有
// 网关转换链处理完毕，本方法只负责 endpoint、鉴权与出网，不做协议转换。
// stream=true 时不施加客户端超时，交由调用方按 SSE 生命周期管理。
func (c *TencentCodeBuddyClient) ChatCompletion(ctx context.Context, account *Account, body []byte, stream bool) (*http.Response, error) {
	if err := c.requireAccount(account); err != nil {
		return nil, err
	}
	cred := account.TencentCodeBuddyCredential()
	if !cred.HasAccessToken() {
		return nil, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN",
			"credentials.access_token is required")
	}
	url := strings.TrimRight(cred.Endpoint().BaseURL, "/") + tencentCodeBuddyChatCompletionsPath
	return c.do(ctx, account, cred, http.MethodPost, url, body, stream, nil)
}

// FetchModels 调用官方模型目录接口 {base}/models，返回模型 ID 列表。
// 返回空列表且 err == nil 表示上游可用但未给出模型——由调用方决定是否兜底。
func (c *TencentCodeBuddyClient) FetchModels(ctx context.Context, account *Account) ([]string, error) {
	models, _, err := c.fetchModels(ctx, account)
	return models, err
}

// fetchModels 是 FetchModels 的状态码感知版本：额外返回上游 HTTP 状态码（出错时
// 为 0），供需要区分"令牌失效(401/403)"与"上游故障"的调用方使用。
//
// 站点差异：目录接口有两个路径，先打官方客户端使用的 /v2/enterprises/personal/models，
// 失败再回退参考实现的老路径 /console/enterprises/personal/models。国际站
// （www.workbuddy.ai）只有前者可用——后者带令牌恒返回 APISIX 原始 500。
//
// 令牌失效（401/403）不再尝试第二个路径：那说明凭据有问题，换路径也是一样的结果，
// 还会把一次明确的鉴权错误拖成两次往返。
func (c *TencentCodeBuddyClient) fetchModels(ctx context.Context, account *Account) ([]string, int, error) {
	models, _, status, err := c.fetchModelsWithCapabilities(ctx, account)
	return models, status, err
}

// fetchModelsWithCapabilities 与 fetchModels 相同，额外返回目录响应里解析到的模型能力参数
// （上游未给出时为 nil）。
func (c *TencentCodeBuddyClient) fetchModelsWithCapabilities(ctx context.Context, account *Account) ([]string, map[string]TencentCodeBuddyModelCapability, int, error) {
	if err := c.requireAccount(account); err != nil {
		return nil, nil, 0, err
	}
	cred := account.TencentCodeBuddyCredential()
	if !cred.HasAccessToken() {
		return nil, nil, 0, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN",
			"credentials.access_token is required")
	}
	// 模型目录挂在站点根（不是 /v2 之外的某个前缀），因此用 Host（不含 /v2）而非
	// BaseURL；且必须按凭据的 product × region 取站点，否则国际版账号会打到大陆站。
	host := strings.TrimRight(cred.Endpoint().Host, "/")

	models, capabilities, status, err := c.fetchModelsAt(ctx, account, cred, host+tencentCodeBuddyModelsPath)
	if err == nil && len(models) > 0 {
		return models, capabilities, status, nil
	}
	if tencentCodeBuddyTokenExpiredStatus(status) {
		return models, capabilities, status, err
	}

	legacyModels, legacyCapabilities, legacyStatus, legacyErr := c.fetchModelsAt(ctx, account, cred, host+tencentCodeBuddyModelsPathLegacy)
	if legacyErr == nil && len(legacyModels) > 0 {
		return legacyModels, legacyCapabilities, legacyStatus, nil
	}
	if legacyErr != nil {
		// 两个路径都失败：上报兜底路径的错误（含状态码），让同步层能正确分类。
		return legacyModels, legacyCapabilities, legacyStatus, legacyErr
	}
	return models, capabilities, status, err
}

// fetchModelsAt 打一次目录接口并解析。path 必须是 host 根下的绝对路径。
func (c *TencentCodeBuddyClient) fetchModelsAt(
	ctx context.Context,
	account *Account,
	cred TencentCodeBuddyCredential,
	url string,
) ([]string, map[string]TencentCodeBuddyModelCapability, int, error) {
	resp, err := c.do(ctx, account, cred, http.MethodGet, url, nil, false, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, tencentCodeBuddyMaxModelsBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, resp.StatusCode, infraerrors.Newf(http.StatusBadGateway, "TENCENT_CODEBUDDY_MODELS_HTTP_ERROR",
			"fetch models failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseTencentCodeBuddyModelIDsForProduct(body, cred.Endpoint().Product),
		parseTencentCodeBuddyModelCapabilities(body), resp.StatusCode, nil
}

// RefreshToken 调用官方刷新接口换取新的 access_token。
//
// 上游契约（POST {base}/plugin/auth/token/refresh）：
//   - Authorization: Bearer <当前 access_token>（可能已过期，但仍需携带）
//   - X-Refresh-Token: <refresh_token>
//   - X-Auth-Refresh-Source: plugin
//
// 返回的是"刷新后的完整凭据"，保持用户已配置的 product/region/uid/enterprise_id
// 不变；上游未回传 refresh_token 时沿用旧值（不轮换）。
func (c *TencentCodeBuddyClient) RefreshToken(ctx context.Context, account *Account) (TencentCodeBuddyCredential, error) {
	if err := c.requireAccount(account); err != nil {
		return TencentCodeBuddyCredential{}, err
	}
	cred := account.TencentCodeBuddyCredential()
	if cred.RefreshToken == "" {
		return TencentCodeBuddyCredential{}, infraerrors.New(http.StatusBadRequest,
			"TENCENT_CODEBUDDY_MISSING_REFRESH_TOKEN", "credentials.refresh_token is required to refresh")
	}
	url := strings.TrimRight(cred.Endpoint().BaseURL, "/") + tencentCodeBuddyTokenRefreshPath
	extra := map[string]string{
		"X-Refresh-Token":       cred.RefreshToken,
		"X-Auth-Refresh-Source": "plugin",
	}
	resp, err := c.do(ctx, account, cred, http.MethodPost, url, []byte("{}"), false, extra)
	if err != nil {
		return TencentCodeBuddyCredential{}, infraerrors.Newf(http.StatusBadGateway,
			"TENCENT_CODEBUDDY_REFRESH_FAILED", "refresh request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, tencentCodeBuddyMaxRefreshBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TencentCodeBuddyCredential{}, infraerrors.Newf(http.StatusBadGateway,
			"TENCENT_CODEBUDDY_REFRESH_HTTP_ERROR", "refresh failed (HTTP %d): %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed tencentCodeBuddyRefreshResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return TencentCodeBuddyCredential{}, infraerrors.Newf(http.StatusBadGateway,
			"TENCENT_CODEBUDDY_REFRESH_PARSE_FAILED", "parse refresh response: %v", err)
	}
	if parsed.Code != 0 || len(parsed.Data) == 0 {
		msg := strings.TrimSpace(parsed.Msg)
		if msg == "" {
			msg = "unknown error"
		}
		return TencentCodeBuddyCredential{}, infraerrors.Newf(http.StatusBadGateway,
			"TENCENT_CODEBUDDY_REFRESH_REJECTED", "refresh failed: %s", msg)
	}

	updated := cred
	if token := strings.TrimSpace(credentialString(parsed.Data, "accessToken")); token != "" {
		updated.AccessToken = token
	}
	if token := strings.TrimSpace(credentialString(parsed.Data, "refreshToken")); token != "" {
		updated.RefreshToken = token
	}
	// domain 与官方登录态一致：响应给了就用响应，没给就继承旧值（cred 已经是旧值）。
	if domain := strings.TrimSpace(credentialString(parsed.Data, "domain")); domain != "" {
		updated.Domain = domain
	}
	if expiresAt := tencentCodeBuddyExpiresAt(parsed.Data); expiresAt != nil {
		updated.ExpiresAt = expiresAt
	}
	return updated, nil
}

// tencentCodeBuddyRefreshResponse 对齐官方刷新接口的 {code, msg, data} 封套。
type tencentCodeBuddyRefreshResponse struct {
	Code int            `json:"code"`
	Msg  string         `json:"msg"`
	Data map[string]any `json:"data"`
}

// tencentCodeBuddyExpiresAt 解析刷新响应中的过期时间，兼容
// expiresAt（epoch 毫秒或 RFC3339）与 expiresIn（秒）。无法识别时返回 nil，
// 由调用方沿用旧值。
func tencentCodeBuddyExpiresAt(data map[string]any) *time.Time {
	if raw := strings.TrimSpace(credentialString(data, "expiresAt")); raw != "" {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 {
			seconds := ms
			if ms > 1_000_000_000_000 {
				seconds = ms / 1000
			}
			t := time.Unix(seconds, 0).UTC()
			return &t
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	if raw := strings.TrimSpace(credentialString(data, "expiresIn")); raw != "" {
		if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 0 {
			t := time.Now().Add(time.Duration(sec) * time.Second).UTC()
			return &t
		}
	}
	return nil
}

// ===== 设备授权流（向导式获取凭据） =====

// TencentCodeBuddyAuthSession 是一次设备授权流程的会话句柄。
// 前端拿到 AuthURL 让用户去浏览器登录，再用 State 轮询结果。
type TencentCodeBuddyAuthSession struct {
	State   string
	AuthURL string
}

// TencentCodeBuddyAuthResult 是授权成功后的凭据与账号展示信息。
type TencentCodeBuddyAuthResult struct {
	Credential TencentCodeBuddyCredential
	Nickname   string
}

// ErrTencentCodeBuddyAuthPending 表示用户尚未在浏览器完成授权。
var ErrTencentCodeBuddyAuthPending = infraerrors.New(http.StatusConflict, "TENCENT_CODEBUDDY_AUTH_PENDING",
	"authorization is not completed yet")

// tencentCodeBuddyPublicEnvelope 是授权类接口的 {code,msg,data} 信封。
type tencentCodeBuddyPublicEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// publicJSON 发送**不绑定账号**的授权类请求：无代理、无账号并发计数。
//
// 上游把"尚未完成登录"编码成业务 code != 0，所以这里不把非 0 业务码当传输错误，
// 而是把信封原样交给调用方按语义判断。
func (c *TencentCodeBuddyClient) publicJSON(
	ctx context.Context,
	method, url string,
	body []byte,
	extra map[string]string,
) (tencentCodeBuddyPublicEnvelope, error) {
	var envelope tencentCodeBuddyPublicEnvelope
	if c == nil || c.httpUpstream == nil {
		return envelope, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED",
			"tencent codebuddy provider is not configured")
	}
	// 整个请求（含读体）都在本函数内完成，因此可以安全地 defer cancel。
	callCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(callCtx, method, url, bytes.NewReader(body))
	if err != nil {
		return envelope, infraerrors.Newf(http.StatusInternalServerError, "TENCENT_CODEBUDDY_REQUEST_BUILD_FAILED",
			"build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", tencentCodeBuddyUserAgent)
	for key, value := range extra {
		req.Header.Set(key, value)
	}
	resp, err := c.httpUpstream.Do(req, "", 0, 1)
	if err != nil {
		return envelope, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, tencentCodeBuddyMaxModelsBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return envelope, infraerrors.Newf(http.StatusBadGateway, "TENCENT_CODEBUDDY_AUTH_HTTP_ERROR",
			"auth request failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return envelope, infraerrors.Newf(http.StatusBadGateway, "TENCENT_CODEBUDDY_AUTH_PARSE_FAILED",
			"parse auth response: %v", err)
	}
	return envelope, nil
}

// StartAuthSession 申请一次设备授权：POST {root}/plugin/auth/state?platform=CLI。
// 返回的 AuthURL 需由用户在浏览器打开并完成登录，State 用于后续轮询。
//
// product / region 决定请求落到哪个站点（见 ResolveTencentCodeBuddyEndpoint）。
// 上游返回的 authUrl 由该站点自身派生，因此选对站点就不会串站。
func (c *TencentCodeBuddyClient) StartAuthSession(ctx context.Context, product, region string) (TencentCodeBuddyAuthSession, error) {
	endpoint := ResolveTencentCodeBuddyEndpoint(product, region)
	url := endpoint.BaseURL + tencentCodeBuddyAuthStatePath + "?platform=" + tencentCodeBuddyAuthPlatform
	envelope, err := c.publicJSON(ctx, http.MethodPost, url, []byte("{}"), nil)
	if err != nil {
		return TencentCodeBuddyAuthSession{}, err
	}
	if envelope.Code != 0 {
		return TencentCodeBuddyAuthSession{}, infraerrors.Newf(http.StatusBadGateway,
			"TENCENT_CODEBUDDY_AUTH_START_REJECTED", "start authorization failed: %s", envelope.Msg)
	}
	var payload struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(envelope.Data, &payload); err != nil {
		return TencentCodeBuddyAuthSession{}, infraerrors.Newf(http.StatusBadGateway,
			"TENCENT_CODEBUDDY_AUTH_PARSE_FAILED", "parse auth state: %v", err)
	}
	state := strings.TrimSpace(payload.State)
	authURL := strings.TrimSpace(payload.AuthURL)
	if state == "" || authURL == "" {
		return TencentCodeBuddyAuthSession{}, infraerrors.New(http.StatusBadGateway,
			"TENCENT_CODEBUDDY_AUTH_STATE_INCOMPLETE", "auth state response missing state or authUrl")
	}
	return TencentCodeBuddyAuthSession{State: state, AuthURL: authURL}, nil
}

// PollAuthSession 轮询授权结果；用户未完成授权时返回 ErrTencentCodeBuddyAuthPending。
//
// 上游契约：/plugin/auth/token?state= 是权威登录状态端点，未完成时业务 code != 0
// （实测 code=11217 "login ing..."）；完成后 code=0 + token bundle。
// uid / enterpriseId / nickname 来自 /plugin/login/account?state=（best-effort，失败不影响凭据可用）。
//
// product / region 必须与 StartAuthSession 一致：轮询与换取凭据打的是同一个站点，
// 且写回的凭据要带上这两个维度，否则账号会落到默认站点。
func (c *TencentCodeBuddyClient) PollAuthSession(ctx context.Context, state, product, region string) (TencentCodeBuddyAuthResult, error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return TencentCodeBuddyAuthResult{}, infraerrors.New(http.StatusBadRequest,
			"TENCENT_CODEBUDDY_AUTH_MISSING_STATE", "state is required")
	}
	escaped := url.QueryEscape(state)
	endpoint := ResolveTencentCodeBuddyEndpoint(product, region)

	envelope, err := c.publicJSON(ctx, http.MethodGet,
		endpoint.BaseURL+tencentCodeBuddyAuthTokenPath+"?state="+escaped, nil, nil)
	if err != nil {
		return TencentCodeBuddyAuthResult{}, err
	}
	if envelope.Code != 0 {
		return TencentCodeBuddyAuthResult{}, ErrTencentCodeBuddyAuthPending
	}

	var token struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(envelope.Data, &token); err != nil {
		return TencentCodeBuddyAuthResult{}, ErrTencentCodeBuddyAuthPending
	}
	accessToken := strings.TrimSpace(token.AccessToken)
	if accessToken == "" {
		return TencentCodeBuddyAuthResult{}, ErrTencentCodeBuddyAuthPending
	}

	result := TencentCodeBuddyAuthResult{
		Credential: TencentCodeBuddyCredential{
			AccessToken:  accessToken,
			RefreshToken: strings.TrimSpace(token.RefreshToken),
			Product:      endpoint.Product,
			Region:       endpoint.Region,
		},
	}
	if token.ExpiresIn > 0 {
		expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UTC()
		result.Credential.ExpiresAt = &expiresAt
	}
	if domain := strings.TrimSpace(token.Domain); domain != "" {
		result.Credential.Domain = domain
	}

	accountEnvelope, acctErr := c.publicJSON(ctx, http.MethodGet,
		endpoint.BaseURL+tencentCodeBuddyLoginAccountPath+"?state="+escaped, nil,
		map[string]string{"Authorization": "Bearer " + accessToken})
	if acctErr == nil && accountEnvelope.Code == 0 {
		var account struct {
			UID          string `json:"uid"`
			EnterpriseID string `json:"enterpriseId"`
			Nickname     string `json:"nickname"`
		}
		if json.Unmarshal(accountEnvelope.Data, &account) == nil {
			result.Credential.UserID = strings.TrimSpace(account.UID)
			result.Credential.EnterpriseID = strings.TrimSpace(account.EnterpriseID)
			result.Nickname = strings.TrimSpace(account.Nickname)
		}
	}
	return result, nil
}
