package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// ===== 测试基础设施：mock HTTP server =====

// tencentCodeBuddyCapturedRequest 记录一次被拦截的上游请求。
// URL/Host 是**改写前**的真实上游地址，用于断言 endpoint 解析结果。
type tencentCodeBuddyCapturedRequest struct {
	Method string
	URL    string
	Host   string
	Header http.Header
	Body   string
}

// tencentCodeBuddyTestUpstream 是 HTTPUpstream 的真实实现：把请求真正发到
// httptest.Server（mock HTTP server），同时记录改写前的 URL 与全部请求头，
// 从而同时验证「endpoint/头/体构造正确」与「真实 HTTP 往返」。
type tencentCodeBuddyTestUpstream struct {
	server *httptest.Server

	mu       sync.Mutex
	captured []tencentCodeBuddyCapturedRequest
}

func newTencentCodeBuddyTestUpstream(t *testing.T, handler http.HandlerFunc) *tencentCodeBuddyTestUpstream {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &tencentCodeBuddyTestUpstream{server: server}
}

func (u *tencentCodeBuddyTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.roundTrip(req)
}

func (u *tencentCodeBuddyTestUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.roundTrip(req)
}

func (u *tencentCodeBuddyTestUpstream) roundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}

	u.mu.Lock()
	u.captured = append(u.captured, tencentCodeBuddyCapturedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Host:   req.URL.Host,
		Header: req.Header.Clone(),
		Body:   string(body),
	})
	u.mu.Unlock()

	// 指向本地 mock server，保留 path/query 与全部请求头。
	req.URL.Scheme = "http"
	req.URL.Host = u.server.Listener.Addr().String()
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}
	return http.DefaultClient.Do(req)
}

func (u *tencentCodeBuddyTestUpstream) requests() []tencentCodeBuddyCapturedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]tencentCodeBuddyCapturedRequest(nil), u.captured...)
}

// tencentCodeBuddyTestAccount 构造一个具备完整凭据的 codebuddy 账号。
func tencentCodeBuddyTestAccount(overrides map[string]any) *Account {
	credentials := map[string]any{
		tencentCodeBuddyCredAccessToken:  "at-test",
		tencentCodeBuddyCredRefreshToken: "rt-test",
		tencentCodeBuddyCredProduct:      TencentCodeBuddyProductCodeBuddy,
		tencentCodeBuddyCredRegion:       TencentCodeBuddyRegionChina,
		tencentCodeBuddyCredUserID:       "uid-1",
		tencentCodeBuddyCredEnterpriseID: "ent-1",
	}
	for key, value := range overrides {
		credentials[key] = value
	}
	return &Account{
		ID:          42,
		Platform:    PlatformTencentCodeBuddy,
		Type:        AccountTypeAPIKey,
		Credentials: credentials,
		Concurrency: 3,
	}
}

func requireTencentCodeBuddyReason(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, infraerrors.Reason(err))
}

func jsonHandler(status int, payload string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}
}

// ===== 1. Credential 解析 =====

func TestParseTencentCodeBuddyCredential(t *testing.T) {
	cred := ParseTencentCodeBuddyCredential(map[string]any{
		"access_token":  "  at  ",
		"refresh_token": " rt ",
		"uid":           " u1 ",
		"enterprise_id": " e1 ",
		// 国际版取值是合法枚举，必须原样保留（不再收敛到大陆版）。
		"product":    "WORKBUDDY",
		"region":     "GLOBAL",
		"expires_at": "2030-01-02T03:04:05Z",
	})
	require.Equal(t, "at", cred.AccessToken)
	require.Equal(t, "rt", cred.RefreshToken)
	require.Equal(t, "u1", cred.UserID)
	require.Equal(t, "e1", cred.EnterpriseID)
	require.Equal(t, TencentCodeBuddyProductWorkBuddy, cred.Product)
	require.Equal(t, TencentCodeBuddyRegionGlobal, cred.Region)
	require.True(t, cred.HasAccessToken())
	require.NotNil(t, cred.ExpiresAt)
	require.Equal(t, time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), *cred.ExpiresAt)

	// 缺失字段落到零值 + 归一化默认（大陆 CodeBuddy），且永不报错。
	empty := ParseTencentCodeBuddyCredential(nil)
	require.False(t, empty.HasAccessToken())
	require.Nil(t, empty.ExpiresAt)
	require.Equal(t, TencentCodeBuddyProductCodeBuddy, empty.Product)
	require.Equal(t, TencentCodeBuddyRegionChina, empty.Region)
	// 非法取值同样回落到默认。
	invalid := ParseTencentCodeBuddyCredential(map[string]any{"product": "bogus", "region": "mars"})
	require.Equal(t, TencentCodeBuddyProductCodeBuddy, invalid.Product)
	require.Equal(t, TencentCodeBuddyRegionChina, invalid.Region)
	require.Nil(t, ParseTencentCodeBuddyCredential(map[string]any{"expires_at": "not-a-time"}).ExpiresAt)

	// Unix 毫秒时间戳。
	require.NotNil(t, ParseTencentCodeBuddyCredential(map[string]any{"expires_at": "1893456000000"}).ExpiresAt)
	// Unix 秒时间戳。
	require.NotNil(t, ParseTencentCodeBuddyCredential(map[string]any{"expires_at": "1893456000"}).ExpiresAt)
}

func TestTencentCodeBuddyCredentialDomainResolution(t *testing.T) {
	// 未显式提供 domain 时使用该站点的默认值。
	plain := ParseTencentCodeBuddyCredential(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductCodeBuddy,
		tencentCodeBuddyCredRegion:  TencentCodeBuddyRegionChina,
	})
	require.Equal(t, tencentCodeBuddyDomain, plain.Domain)
	require.Equal(t, "www.codebuddy.cn", plain.Domain)
	require.Equal(t, tencentCodeBuddyDomain, plain.Endpoint().Domain)

	// 国际版 WorkBuddy 回落到它自己的默认域，而不是大陆域。
	intl := ParseTencentCodeBuddyCredential(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductWorkBuddy,
		tencentCodeBuddyCredRegion:  TencentCodeBuddyRegionGlobal,
	})
	require.Equal(t, "www.workbuddy.ai", intl.Domain)
	require.Equal(t, tencentWorkBuddyAPIHostIntl, intl.Endpoint().Host)

	// 账号级 domain 覆盖默认值（对应官方登录态的 auth.domain 语义）。
	custom := ParseTencentCodeBuddyCredential(map[string]any{
		tencentCodeBuddyCredDomain: "  tenant.example.com  ",
	})
	require.Equal(t, "tenant.example.com", custom.Domain)
	require.Equal(t, "tenant.example.com", custom.Endpoint().Domain)

	// 非默认 domain 落盘；默认值不落盘，避免把默认值当成显式配置固化。
	require.Equal(t, "tenant.example.com", custom.Apply(nil)[tencentCodeBuddyCredDomain])
	require.NotContains(t, plain.Apply(nil), tencentCodeBuddyCredDomain)
	require.NotContains(t, intl.Apply(nil), tencentCodeBuddyCredDomain)
}

func TestTencentCodeBuddyCredentialApplyPreservesOtherKeys(t *testing.T) {
	cred := ParseTencentCodeBuddyCredential(map[string]any{
		"access_token": "at-new",
		// 国际版取值应被保留。
		"product":    "workbuddy",
		"region":     "global",
		"expires_at": "2030-01-02T03:04:05Z",
	})
	base := map[string]any{
		"model_mapping": map[string]any{"glm-5.2": "glm-5.2"},
		"base_url":      "https://stale.example.com",
	}
	out := cred.Apply(base)

	require.Equal(t, "at-new", out[tencentCodeBuddyCredAccessToken])
	require.Equal(t, TencentCodeBuddyProductWorkBuddy, out[tencentCodeBuddyCredProduct])
	require.Equal(t, TencentCodeBuddyRegionGlobal, out[tencentCodeBuddyCredRegion])
	require.Equal(t, "2030-01-02T03:04:05Z", out[tencentCodeBuddyCredExpiresAt])
	require.Contains(t, out, "model_mapping")
	// Apply 只覆盖 token 相关字段，不负责清理 base_url（那是写入路径归一化的职责）。
	require.Contains(t, out, "base_url")
	// base 不被就地修改。
	require.NotContains(t, base, tencentCodeBuddyCredAccessToken)

	// 无过期时间时删除该键，避免把旧过期时间留在凭据里。
	noExpiry := ParseTencentCodeBuddyCredential(map[string]any{"access_token": "x"}).
		Apply(map[string]any{"expires_at": "1999-01-01T00:00:00Z"})
	require.NotContains(t, noExpiry, tencentCodeBuddyCredExpiresAt)
}

func TestTencentCodeBuddyCredentialNeedsRefresh(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	window := 30 * time.Minute

	// 无 expires_at 不主动刷新。
	require.False(t, TencentCodeBuddyCredential{}.NeedsRefresh(now, window))

	future := now.Add(30 * 24 * time.Hour)
	require.False(t, TencentCodeBuddyCredential{ExpiresAt: &future}.NeedsRefresh(now, window))

	// 60 天令牌：窗口至少提前 7 天，全局 30 分钟窗口不够用。
	soon := now.Add(6 * 24 * time.Hour)
	require.True(t, TencentCodeBuddyCredential{ExpiresAt: &soon}.NeedsRefresh(now, window))

	// 保活：签发满一天就刷新；不满一天不刷新。
	issuedYesterday := now.Add(-25 * time.Hour)
	require.True(t, TencentCodeBuddyCredential{ExpiresAt: &future, IssuedAt: &issuedYesterday}.NeedsRefresh(now, window))
	issuedToday := now.Add(-time.Hour)
	require.False(t, TencentCodeBuddyCredential{ExpiresAt: &future, IssuedAt: &issuedToday}.NeedsRefresh(now, window))

	past := now.Add(-time.Hour)
	require.True(t, TencentCodeBuddyCredential{ExpiresAt: &past}.NeedsRefresh(now, window))
}

func TestParseTencentCodeBuddyCredentialJWTFallback(t *testing.T) {
	iat := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	exp := iat.Add(60 * 24 * time.Hour)
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iat":%d,"exp":%d,"sub":"u"}`, iat.Unix(), exp.Unix())))
	token := "eyJhbGciOiJIUzI1NiJ9." + payload + ".sig"

	cred := ParseTencentCodeBuddyCredential(map[string]any{"access_token": token})
	require.NotNil(t, cred.ExpiresAt)
	require.True(t, cred.ExpiresAt.Equal(exp))
	require.NotNil(t, cred.IssuedAt)
	require.True(t, cred.IssuedAt.Equal(iat))

	// 显式 expires_at 优先于 JWT exp。
	explicit := iat.Add(time.Hour)
	cred = ParseTencentCodeBuddyCredential(map[string]any{"access_token": token, "expires_at": explicit.Format(time.RFC3339)})
	require.True(t, cred.ExpiresAt.Equal(explicit))

	// 非 JWT 令牌：两者都为空。
	cred = ParseTencentCodeBuddyCredential(map[string]any{"access_token": "opaque-token"})
	require.Nil(t, cred.ExpiresAt)
	require.Nil(t, cred.IssuedAt)
}

// ===== 2. Endpoint 选择 =====

func TestResolveTencentCodeBuddyEndpoint_SiteMatrix(t *testing.T) {
	// product × region 四个组合各自解析到独立站点，均经实测 reachable。
	// 参考实现历史只支持大陆 CodeBuddy；国际版（尤其 workbuddy.ai）是本仓库新增能力。
	cases := []struct {
		product string
		region  string
		baseURL string
		host    string
		domain  string
	}{
		{TencentCodeBuddyProductCodeBuddy, TencentCodeBuddyRegionChina, "https://copilot.tencent.com/v2", "https://copilot.tencent.com", "www.codebuddy.cn"},
		{TencentCodeBuddyProductCodeBuddy, TencentCodeBuddyRegionGlobal, "https://www.codebuddy.ai/v2", "https://www.codebuddy.ai", "www.codebuddy.ai"},
		{TencentCodeBuddyProductWorkBuddy, TencentCodeBuddyRegionChina, "https://www.workbuddy.cn/v2", "https://www.workbuddy.cn", "www.workbuddy.cn"},
		{TencentCodeBuddyProductWorkBuddy, TencentCodeBuddyRegionGlobal, "https://www.workbuddy.ai/v2", "https://www.workbuddy.ai", "www.workbuddy.ai"},
	}
	for _, tc := range cases {
		endpoint := ResolveTencentCodeBuddyEndpoint(tc.product, tc.region)
		require.Equal(t, tc.baseURL, endpoint.BaseURL, "input=%q/%q", tc.product, tc.region)
		require.Equal(t, tc.host, endpoint.Host, "input=%q/%q", tc.product, tc.region)
		require.Equal(t, tc.domain, endpoint.Domain, "input=%q/%q", tc.product, tc.region)
		require.Equal(t, tc.product, endpoint.Product)
		require.Equal(t, tc.region, endpoint.Region)
	}

	// 大小写与空白容忍：归一化后再查表。
	normalized := ResolveTencentCodeBuddyEndpoint(" WorkBuddy ", " GLOBAL ")
	require.Equal(t, "https://www.workbuddy.ai/v2", normalized.BaseURL)
	require.Equal(t, TencentCodeBuddyProductWorkBuddy, normalized.Product)
	require.Equal(t, TencentCodeBuddyRegionGlobal, normalized.Region)

	// 非法/缺省输入回落到大陆 CodeBuddy（保持存量账号语义不变）。
	for _, tc := range [][2]string{{"bogus", "mars"}, {"", ""}} {
		endpoint := ResolveTencentCodeBuddyEndpoint(tc[0], tc[1])
		require.Equal(t, "https://copilot.tencent.com/v2", endpoint.BaseURL, "input=%q/%q", tc[0], tc[1])
		require.Equal(t, "www.codebuddy.cn", endpoint.Domain, "input=%q/%q", tc[0], tc[1])
		require.Equal(t, TencentCodeBuddyProductCodeBuddy, endpoint.Product)
		require.Equal(t, TencentCodeBuddyRegionChina, endpoint.Region)
	}

	// 受支持枚举：product 两项、region 两项。
	require.Equal(t, []string{TencentCodeBuddyProductCodeBuddy, TencentCodeBuddyProductWorkBuddy}, TencentCodeBuddyProducts())
	require.Equal(t, []string{TencentCodeBuddyRegionChina, TencentCodeBuddyRegionGlobal}, TencentCodeBuddyRegions())
	require.True(t, IsTencentCodeBuddyProduct("codebuddy"))
	require.True(t, IsTencentCodeBuddyProduct("workbuddy"))
	require.False(t, IsTencentCodeBuddyProduct("bogus"))
	require.True(t, IsTencentCodeBuddyRegion("china"))
	require.True(t, IsTencentCodeBuddyRegion("global"))
	require.False(t, IsTencentCodeBuddyRegion("mars"))

	// 账号无法通过 credentials.base_url 覆盖 endpoint：国际版账号仍落在自己的站点。
	account := tencentCodeBuddyTestAccount(map[string]any{
		"base_url": "https://evil.example.com",
		"product":  TencentCodeBuddyProductWorkBuddy,
		"region":   TencentCodeBuddyRegionGlobal,
	})
	require.Equal(t, "https://www.workbuddy.ai/v2", account.TencentCodeBuddyBaseURL())
	require.Equal(t, "https://www.workbuddy.ai/v2", account.TencentCodeBuddyCredential().Endpoint().BaseURL)
	// 默认账号仍是大陆 CodeBuddy。
	require.Equal(t, tencentCodeBuddyAPIRoot, tencentCodeBuddyTestAccount(nil).TencentCodeBuddyBaseURL())
}

// ===== 3. Token 刷新 =====

func TestTencentCodeBuddyClientRefreshToken_Success(t *testing.T) {
	var refreshHeader, sourceHeader string
	upstream := newTencentCodeBuddyTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		refreshHeader = r.Header.Get("X-Refresh-Token")
		sourceHeader = r.Header.Get("X-Auth-Refresh-Source")
		jsonHandler(http.StatusOK, `{"code":0,"msg":"ok","data":{"accessToken":"at-new","refreshToken":"rt-new","expiresIn":"3600"}}`)(w, r)
	})
	client := NewTencentCodeBuddyClient(upstream)

	updated, err := client.RefreshToken(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, "at-new", updated.AccessToken)
	require.Equal(t, "rt-new", updated.RefreshToken)
	require.NotNil(t, updated.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(time.Hour), *updated.ExpiresAt, 2*time.Minute)
	// 用户配置的维度与身份保持不变。
	require.Equal(t, "uid-1", updated.UserID)
	require.Equal(t, "ent-1", updated.EnterpriseID)
	require.Equal(t, TencentCodeBuddyProductCodeBuddy, updated.Product)
	require.Equal(t, TencentCodeBuddyRegionChina, updated.Region)

	requests := upstream.requests()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodPost, requests[0].Method)
	require.Equal(t, tencentCodeBuddyAPIRoot+tencentCodeBuddyTokenRefreshPath, requests[0].URL)
	require.Equal(t, "Bearer at-test", requests[0].Header.Get("Authorization"))
	require.Equal(t, "rt-test", refreshHeader)
	require.Equal(t, "plugin", sourceHeader)
	// 身份头由客户端集中注入（与转发路径共用同一实现）。
	require.Equal(t, "WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1", requests[0].Header.Get("User-Agent"))
	require.Equal(t, tencentCodeBuddyDomain, requests[0].Header.Get("X-Domain"))
	require.Equal(t, "uid-1", requests[0].Header.Get("X-User-Id"))
	// 刷新不是对话，不带用量归属头。
	require.Empty(t, requests[0].Header.Get("X-Agent-Purpose"))
	require.Equal(t, "ent-1", requests[0].Header.Get("X-Enterprise-Id"))
	require.Equal(t, "ent-1", requests[0].Header.Get("X-Tenant-Id"))
}

func TestTencentCodeBuddyClientRefreshToken_EpochMillisecondExpiry(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at-ms","expiresAt":"1893456000000"}}`))
	client := NewTencentCodeBuddyClient(upstream)

	updated, err := client.RefreshToken(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, "at-ms", updated.AccessToken)
	require.NotNil(t, updated.ExpiresAt)
	require.Equal(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), *updated.ExpiresAt)
	// 上游未回传 refresh_token 时沿用旧值（不轮换）。
	require.Equal(t, "rt-test", updated.RefreshToken)
}

func TestTencentCodeBuddyClientRefreshToken_Errors(t *testing.T) {
	t.Run("missing refresh token", func(t *testing.T) {
		client := NewTencentCodeBuddyClient(newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{}`)))
		account := tencentCodeBuddyTestAccount(map[string]any{tencentCodeBuddyCredRefreshToken: ""})
		_, err := client.RefreshToken(context.Background(), account)
		requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_MISSING_REFRESH_TOKEN")
	})

	t.Run("business rejection", func(t *testing.T) {
		upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{"code":40101,"msg":"refresh token expired"}`))
		client := NewTencentCodeBuddyClient(upstream)
		_, err := client.RefreshToken(context.Background(), tencentCodeBuddyTestAccount(nil))
		requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_REFRESH_REJECTED")
		require.Contains(t, err.Error(), "refresh token expired")
	})

	t.Run("http error", func(t *testing.T) {
		upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusInternalServerError, `boom`))
		client := NewTencentCodeBuddyClient(upstream)
		_, err := client.RefreshToken(context.Background(), tencentCodeBuddyTestAccount(nil))
		requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_REFRESH_HTTP_ERROR")
	})

	t.Run("malformed body", func(t *testing.T) {
		upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{not json`))
		client := NewTencentCodeBuddyClient(upstream)
		_, err := client.RefreshToken(context.Background(), tencentCodeBuddyTestAccount(nil))
		requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_REFRESH_PARSE_FAILED")
	})
}

func TestTencentCodeBuddyClientRefreshToken_InheritsRotatedDomain(t *testing.T) {
	// 上游回传新 domain -> 以响应为准（官方登录态同语义）。
	rotated := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at-new","domain":"tenant.example.com"}}`))
	updated, err := NewTencentCodeBuddyClient(rotated).RefreshToken(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, "tenant.example.com", updated.Domain)
	require.Equal(t, "tenant.example.com", updated.Endpoint().Domain)

	// 上游未回传 domain -> 继承账号既有值。
	inherited := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at-2"}}`))
	updated2, err := NewTencentCodeBuddyClient(inherited).RefreshToken(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, tencentCodeBuddyDomain, updated2.Domain)
}

func TestTencentCodeBuddyProviderRefreshToken_PersistsRotatedDomain(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at-new","domain":"tenant.example.com"}}`))
	provider := NewTencentCodeBuddyProvider(upstream)

	credentials, err := provider.Refresh(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, "tenant.example.com", credentials[tencentCodeBuddyCredDomain])
}

func TestTencentCodeBuddyProviderRefreshPreservesCredentialMap(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at-new","expiresIn":"600"}}`))
	provider := NewTencentCodeBuddyProvider(upstream)
	account := tencentCodeBuddyTestAccount(map[string]any{"model_mapping": map[string]any{"glm-5.2": "glm-5.2"}})

	updated, err := provider.Refresh(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "at-new", updated[tencentCodeBuddyCredAccessToken])
	require.Equal(t, "rt-test", updated[tencentCodeBuddyCredRefreshToken])
	require.Contains(t, updated, "model_mapping")
	require.Contains(t, updated, tencentCodeBuddyCredExpiresAt)

	// 刷新器接口语义：CanRefresh / NeedsRefresh 只对 codebuddy 生效。
	refresher := NewTencentCodeBuddyTokenRefresher(provider)
	require.True(t, refresher.CanRefresh(account))
	require.False(t, refresher.CanRefresh(&Account{Platform: PlatformOpenAI}))
	require.False(t, refresher.CanRefresh(nil))
	require.Equal(t, "token_refresh:tencent_codebuddy:42", refresher.CacheKey(account))
	require.False(t, refresher.NeedsRefresh(account, time.Hour), "无 expires_at 时不主动刷新")
	soonAccount := tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
	})
	require.True(t, refresher.NeedsRefresh(soonAccount, time.Hour))
}

// ===== 4. Model List =====

func TestTencentCodeBuddyClientFetchModels(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    []string
	}{
		{
			// 上游真实形态：cli agent 的 models 是可用清单，models[] 带 disabled。
			name: "upstream envelope",
			payload: `{"code":0,"data":{
				"models":[{"id":"glm-5.2"},{"id":"glm-5.1"},{"id":"legacy-1","disabled":true}],
				"agents":[{"name":"cli","models":["glm-5.2","glm-5.1","legacy-1"]},{"name":"ide","models":["other"]}]}}`,
			want: []string{"glm-5.2", "glm-5.1"},
		},
		{
			name: "upstream envelope without cli agent falls back to all known",
			payload: `{"code":0,"data":{
				"models":[{"id":"glm-5.2"},{"id":"glm-5.1","disabled":true}],
				"agents":[{"name":"ide","models":["glm-5.2"]}]}}`,
			want: []string{"glm-5.2"},
		},
		{
			name:    "openai style",
			payload: `{"object":"list","data":[{"id":"glm-5.2"},{"id":"kimi-k2.7"}]}`,
			want:    []string{"glm-5.2", "kimi-k2.7"},
		},
		{
			name:    "codebuddy style",
			payload: `{"code":0,"data":[{"modelId":"glm-5.2"},{"model":"deepseek-v4-pro"},{"id":"kimi-k2.7"}]}`,
			want:    []string{"glm-5.2", "deepseek-v4-pro", "kimi-k2.7"},
		},
		{
			name:    "string items",
			payload: `{"data":["glm-5.2","auto"]}`,
			want:    []string{"glm-5.2", "auto"},
		},
		{
			name:    "blank ignored",
			payload: `{"data":[{"id":"  "},{"id":"glm-5.1"}]}`,
			want:    []string{"glm-5.1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, tc.payload))
			client := NewTencentCodeBuddyClient(upstream)

			models, err := client.FetchModels(context.Background(), tencentCodeBuddyTestAccount(nil))
			require.NoError(t, err)
			require.Equal(t, tc.want, models)

			requests := upstream.requests()
			require.Len(t, requests, 1)
			require.Equal(t, http.MethodGet, requests[0].Method)
			// 模型目录挂在 host 根，不在 /v2 下；且 host 由账号的 product × region 决定。
			require.Equal(t, tencentCodeBuddyAPIHost+tencentCodeBuddyModelsPath, requests[0].URL)
			require.Equal(t, "Bearer at-test", requests[0].Header.Get("Authorization"))
		})
	}
}

func TestTencentCodeBuddyProviderFetchModelIDs_FallsBackToStaticList(t *testing.T) {
	t.Run("upstream unavailable", func(t *testing.T) {
		upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusBadGateway, `nope`))
		provider := NewTencentCodeBuddyProvider(upstream)
		models, err := provider.FetchModelIDs(context.Background(), tencentCodeBuddyTestAccount(nil))
		require.Error(t, err)
		require.Equal(t, DefaultTencentCodeBuddyModelIDs(), models)
	})

	t.Run("upstream returns empty list", func(t *testing.T) {
		upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{"data":[]}`))
		provider := NewTencentCodeBuddyProvider(upstream)
		models, err := provider.FetchModelIDs(context.Background(), tencentCodeBuddyTestAccount(nil))
		require.NoError(t, err)
		require.Equal(t, DefaultTencentCodeBuddyModelIDs(), models)
	})

	t.Run("upstream returns live catalog", func(t *testing.T) {
		upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{"data":[{"id":"glm-9.9"}]}`))
		provider := NewTencentCodeBuddyProvider(upstream)
		models, err := provider.FetchModelIDs(context.Background(), tencentCodeBuddyTestAccount(nil))
		require.NoError(t, err)
		require.Equal(t, []string{"glm-9.9"}, models)
	})

	t.Run("provider without upstream falls back", func(t *testing.T) {
		models, err := NewTencentCodeBuddyProvider(nil).FetchModelIDs(context.Background(), tencentCodeBuddyTestAccount(nil))
		require.NoError(t, err)
		require.Equal(t, DefaultTencentCodeBuddyModelIDs(), models)
	})
}

// ===== 5. Chat Completion 请求转换 =====

func TestTencentCodeBuddyClientChatCompletion_RequestContract(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK,
		`{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
	client := NewTencentCodeBuddyClient(upstream)
	account := tencentCodeBuddyTestAccount(nil)
	payload := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"ping"}]}`)

	resp, err := client.ChatCompletion(context.Background(), account, payload, false)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(raw), "cmpl-1")

	requests := upstream.requests()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodPost, requests[0].Method)
	require.Equal(t, tencentCodeBuddyAPIRoot+tencentCodeBuddyChatCompletionsPath, requests[0].URL)
	require.Equal(t, string(payload), requests[0].Body)
	require.Equal(t, "application/json", requests[0].Header.Get("Content-Type"))
	require.Equal(t, "Bearer at-test", requests[0].Header.Get("Authorization"))
	require.Equal(t, tencentCodeBuddyDomain, requests[0].Header.Get("X-Domain"))
	require.Equal(t, "uid-1", requests[0].Header.Get("X-User-Id"))
}

func TestTencentCodeBuddyClientChatCompletion_RoutesBySite(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{"id":"cmpl-2"}`))
	client := NewTencentCodeBuddyClient(upstream)
	// 国际版 WorkBuddy 必须打到 workbuddy.ai 站点并使用它自己的 X-Domain。
	account := tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductWorkBuddy,
		tencentCodeBuddyCredRegion:  TencentCodeBuddyRegionGlobal,
	})

	resp, err := client.ChatCompletion(context.Background(), account, []byte(`{"model":"auto"}`), false)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	requests := upstream.requests()
	require.Len(t, requests, 1)
	require.Equal(t, "https://www.workbuddy.ai/v2/chat/completions", requests[0].URL)
	require.Equal(t, "www.workbuddy.ai", requests[0].Header.Get("X-Domain"))

	// 账号级 domain 覆盖只影响 X-Domain，不改变 host。
	overridden := tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductWorkBuddy,
		tencentCodeBuddyCredRegion:  TencentCodeBuddyRegionGlobal,
		tencentCodeBuddyCredDomain:  "tenant.example.com",
	})
	resp2, err := client.ChatCompletion(context.Background(), overridden, []byte(`{"model":"auto"}`), false)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	requests = upstream.requests()
	require.Len(t, requests, 2)
	require.Equal(t, "https://www.workbuddy.ai/v2/chat/completions", requests[1].URL)
	require.Equal(t, "tenant.example.com", requests[1].Header.Get("X-Domain"))

	// 默认账号（无 product/region）仍打到大陆 CodeBuddy。
	resp3, err := client.ChatCompletion(context.Background(), tencentCodeBuddyTestAccount(nil), []byte(`{"model":"auto"}`), false)
	require.NoError(t, err)
	defer func() { _ = resp3.Body.Close() }()

	requests = upstream.requests()
	require.Len(t, requests, 3)
	require.Equal(t, "https://copilot.tencent.com/v2/chat/completions", requests[2].URL)
	require.Equal(t, "www.codebuddy.cn", requests[2].Header.Get("X-Domain"))
}

// ===== 6. Stream 转换 =====

func TestTencentCodeBuddyClientChatCompletion_StreamPassthrough(t *testing.T) {
	const sse = "data: {\"choices\":[{\"delta\":{\"content\":\"h\"}}]}\n\ndata: [DONE]\n\n"
	upstream := newTencentCodeBuddyTestUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, sse)
	})
	client := NewTencentCodeBuddyClient(upstream)

	resp, err := client.ChatCompletion(context.Background(), tencentCodeBuddyTestAccount(nil),
		[]byte(`{"model":"auto","stream":true}`), true)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	require.Equal(t, "text/event-stream", upstream.requests()[0].Header.Get("Accept"))

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, sse, string(raw), "SSE 必须逐字透传，不做重组")
}

func TestTencentCodeBuddyClientTimeoutPolicy(t *testing.T) {
	slowHandler := func(w http.ResponseWriter, _ *http.Request) {
		// 先睡再写头：让"元数据超时"能在响应头到达前生效。
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}

	t.Run("metadata call times out", func(t *testing.T) {
		client := NewTencentCodeBuddyClient(newTencentCodeBuddyTestUpstream(t, slowHandler))
		client.timeout = 30 * time.Millisecond
		_, err := client.ChatCompletion(context.Background(), tencentCodeBuddyTestAccount(nil), []byte(`{}`), false)
		require.Error(t, err, "非流式请求应受客户端元数据超时约束")
	})

	t.Run("stream is not severed by metadata timeout", func(t *testing.T) {
		client := NewTencentCodeBuddyClient(newTencentCodeBuddyTestUpstream(t, slowHandler))
		client.timeout = 30 * time.Millisecond
		resp, err := client.ChatCompletion(context.Background(), tencentCodeBuddyTestAccount(nil), []byte(`{}`), true)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err, "SSE 长连接不应被元数据超时切断")
		require.Contains(t, string(raw), "[DONE]")
	})
}

// ===== 7. 错误处理 =====

func TestTencentCodeBuddyClientErrors(t *testing.T) {
	account := tencentCodeBuddyTestAccount(nil)

	t.Run("nil upstream is not configured", func(t *testing.T) {
		client := NewTencentCodeBuddyClient(nil)
		_, err := client.ChatCompletion(context.Background(), account, []byte(`{}`), false)
		requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_NOT_CONFIGURED")
	})

	t.Run("nil account", func(t *testing.T) {
		client := NewTencentCodeBuddyClient(newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{}`)))
		_, err := client.ChatCompletion(context.Background(), nil, []byte(`{}`), false)
		requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_NIL_ACCOUNT")
	})

	t.Run("missing access token", func(t *testing.T) {
		client := NewTencentCodeBuddyClient(newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{}`)))
		_, err := client.ChatCompletion(context.Background(), tencentCodeBuddyTestAccount(map[string]any{
			tencentCodeBuddyCredAccessToken: "",
		}), []byte(`{}`), false)
		requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN")
	})

	t.Run("upstream error status is passed through to caller", func(t *testing.T) {
		upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusUnauthorized, `{"error":"invalid token"}`))
		client := NewTencentCodeBuddyClient(upstream)
		resp, err := client.ChatCompletion(context.Background(), account, []byte(`{}`), false)
		require.NoError(t, err, "HTTP 错误码由调用方决定处理策略，客户端不吞掉响应")
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("transport failure is surfaced", func(t *testing.T) {
		failing := &tencentCodeBuddyFailingUpstream{err: context.DeadlineExceeded}
		client := NewTencentCodeBuddyClient(failing)
		_, err := client.ChatCompletion(context.Background(), account, []byte(`{}`), false)
		require.Error(t, err)
	})
}

type tencentCodeBuddyFailingUpstream struct{ err error }

func (u *tencentCodeBuddyFailingUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, u.err
}

func (u *tencentCodeBuddyFailingUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	return nil, u.err
}

// ===== 网关侧身份头钩子（唯一实现） =====

func TestApplyTencentCodeBuddyHeaders(t *testing.T) {
	header := http.Header{}
	applyTencentCodeBuddyHeaders(header, tencentCodeBuddyTestAccount(nil))
	require.Equal(t, "WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1", header.Get("User-Agent"))
	require.Equal(t, "1", header.Get("X-CodeBuddy-Request"))
	require.Equal(t, "zh-CN", header.Get("Accept-Language"))
	require.Equal(t, "conversation", header.Get("X-Agent-Purpose"))
	require.Equal(t, "WorkBuddy", header.Get("X-IDE-Name"))
	require.Equal(t, "5.5.4", header.Get("X-IDE-Version"))
	// 设备标识按 uid 稳定派生：同账号恒同值，不同账号不同。
	require.Len(t, header.Get("X-Machine-ID"), 36)
	require.Equal(t, tencentCodeBuddyStableID("uid-1", "machine"), header.Get("X-Machine-ID"))
	require.NotEqual(t, header.Get("X-Machine-ID"), header.Get("X-Session-ID"))
	require.NotEqual(t, tencentCodeBuddyStableID("uid-1", "machine"), tencentCodeBuddyStableID("uid-2", "machine"))
	require.Equal(t, tencentCodeBuddyDomain, header.Get("X-Domain"))
	require.Equal(t, "uid-1", header.Get("X-User-Id"))
	require.Equal(t, "ent-1", header.Get("X-Enterprise-Id"))
	require.Equal(t, "ent-1", header.Get("X-Tenant-Id"))

	// 缺少可选身份字段时不写入空头，避免上游把空值当成显式覆盖。
	sparse := http.Header{}
	applyTencentCodeBuddyHeaders(sparse, tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredUserID:       "",
		tencentCodeBuddyCredEnterpriseID: "",
	}))
	require.Empty(t, sparse.Get("X-User-Id"))
	require.Empty(t, sparse.Get("X-Machine-ID"))
	require.Empty(t, sparse.Get("X-Enterprise-Id"))
	require.Empty(t, sparse.Get("X-Tenant-Id"))

	// nil 安全。
	applyTencentCodeBuddyHeaders(nil, tencentCodeBuddyTestAccount(nil))
	applyTencentCodeBuddyHeaders(http.Header{}, nil)
}

func TestApplyTencentCodeBuddyHeadersPrefersAccountDomain(t *testing.T) {
	header := http.Header{}
	applyTencentCodeBuddyHeaders(header, tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredDomain: "tenant.example.com",
	}))
	require.Equal(t, "tenant.example.com", header.Get("X-Domain"),
		"账号级 domain 必须优先于 region 默认值")
}

func TestTencentCodeBuddyClientApplyBearerSkipsEmptyToken(t *testing.T) {
	client := NewTencentCodeBuddyClient(nil)
	header := http.Header{}
	client.ApplyBearer(header, TencentCodeBuddyCredential{AccessToken: ""})
	require.Empty(t, header.Get("Authorization"))

	client.ApplyBearer(header, TencentCodeBuddyCredential{AccessToken: "at"})
	require.Equal(t, "Bearer at", header.Get("Authorization"))
}

// ===== 7. WorkBuddy 模型目录兜底 =====

// workBuddyIntlTestAccount 是国际版 WorkBuddy 测试账号。
func workBuddyIntlTestAccount() *Account {
	return tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductWorkBuddy,
		tencentCodeBuddyCredRegion:  TencentCodeBuddyRegionGlobal,
	})
}

// TestFetchTencentCodeBuddyUpstreamModels_ReportsFailureWhenBothPathsFail
// 目录接口两个路径都不可用时，必须如实上报为上游故障——不能用一个可能过时/不完整的
// 静态表掩盖，否则用户会看到一份"看起来对但少了模型"的清单。
func TestFetchTencentCodeBuddyUpstreamModels_ReportsFailureWhenBothPathsFail(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(`<html><title>500 Internal Server Error</title></html>`)),
	}}
	svc := &AccountTestService{httpUpstream: upstream}

	_, err := svc.fetchTencentCodeBuddyUpstreamModels(context.Background(), workBuddyIntlTestAccount())
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, upstreamModelSyncStatusCode(err))
	// 两个路径都被尝试过。
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://www.workbuddy.ai"+tencentCodeBuddyModelsPath, upstream.requests[0].URL.String())
	require.Equal(t, "https://www.workbuddy.ai"+tencentCodeBuddyModelsPathLegacy, upstream.requests[1].URL.String())
}

// TestFetchTencentCodeBuddyUpstreamModels_WorkBuddyUsesRealCatalog
// 回归锁：国际版的真实目录来自 /v2/enterprises/personal/models，响应形态是
// data.models[].id（含 credits/name 等元信息），而不是大陆站的 data.agents[].models。
func TestFetchTencentCodeBuddyUpstreamModels_WorkBuddyUsesRealCatalog(t *testing.T) {
	const payload = `{"code":0,"msg":"OK","data":{
		"models":[
			{"id":"default-model","name":"Auto","credits":"x0.79 credits","supportsImages":true},
			{"id":"gpt-5.6-sol","name":"GPT-5.6-Sol","credits":"x3.47"},
			{"id":"gemini-3.5-flash","name":"Gemini-3.5-Flash","credits":"x0.99"},
			{"id":"glm-5.3","name":"GLM-5.3","credits":"x0.79"}
		]}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}}
	svc := &AccountTestService{httpUpstream: upstream}

	models, err := svc.fetchTencentCodeBuddyUpstreamModels(context.Background(), workBuddyIntlTestAccount())
	require.NoError(t, err)
	require.Equal(t, []string{"default-model", "gemini-3.5-flash", "glm-5.3", "gpt-5.6-sol"}, models)
	// 目录里部分模型没带能力字段，会再补拉一次 /v3/config。
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://www.workbuddy.ai"+tencentCodeBuddyProductConfigPath, upstream.requests[1].URL.String())
}

// TestFetchModels_UsesV2PathFirst 锁定真实站点差异的修复：官方客户端用的是
// /v2/enterprises/personal/models，而老代码用的是 /console/...——后者在国际站
// （www.workbuddy.ai）带令牌恒返回 APISIX 原始 500。主路径可用时**不应**再打老路径。
func TestFetchModels_UsesV2PathFirst(t *testing.T) {
	const payload = `{"code":0,"msg":"OK","data":{"models":[{"id":"gpt-6-astra"},{"id":"gemini-3.5-flash"}]}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}}
	client := NewTencentCodeBuddyClient(upstream)

	models, status, err := client.fetchModels(context.Background(), workBuddyIntlTestAccount())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, []string{"gemini-3.5-flash", "gpt-6-astra"}, models)
	// 只打主路径一次，不回退。
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://www.workbuddy.ai/v2/enterprises/personal/models", upstream.requests[0].URL.String())
}

// TestFetchModels_FallsBackToLegacyPath 主路径失败时回退到参考实现的老路径。
func TestFetchModels_FallsBackToLegacyPath(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader(`<html>500</html>`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"agents":[{"name":"cli","models":["auto","glm-5.3"]}]}}`)),
		},
	}}
	client := NewTencentCodeBuddyClient(upstream)

	models, status, err := client.fetchModels(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, []string{"auto", "glm-5.3"}, models)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://copilot.tencent.com"+tencentCodeBuddyModelsPath, upstream.requests[0].URL.String())
	require.Equal(t, "https://copilot.tencent.com"+tencentCodeBuddyModelsPathLegacy, upstream.requests[1].URL.String())
}

// TestFetchModels_DoesNotRetryLegacyOnAuthFailure 令牌失效时不再换路径重试：
// 那说明凭据有问题，换路径也是一样的结果，只会把一次明确的鉴权错误拖成两次往返。
func TestFetchModels_DoesNotRetryLegacyOnAuthFailure(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader(`{"code":1001,"msg":"unauthorized"}`)),
	}}
	client := NewTencentCodeBuddyClient(upstream)

	_, status, err := client.fetchModels(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Len(t, upstream.requests, 1, "401 不应触发老路径重试")
}

// TestFetchTencentCodeBuddyUpstreamModels_WorkBuddyStillReportsTokenFailure
// 兜底不得掩盖凭据问题：401/403 说明令牌失效，必须照旧上报。
func TestFetchTencentCodeBuddyUpstreamModels_WorkBuddyStillReportsTokenFailure(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader(`<html>401 Authorization Required</html>`)),
	}}
	svc := &AccountTestService{httpUpstream: upstream}

	_, err := svc.fetchTencentCodeBuddyUpstreamModels(context.Background(), workBuddyIntlTestAccount())
	require.Error(t, err, "令牌失效必须上报，不能被静态目录兜底掩盖")
}

// TestFetchTencentCodeBuddyUpstreamModels_MainlandDoesNotFallBack
// 大陆 CodeBuddy 的目录接口是好的：那里出现 5xx 属于真实故障，必须照旧上报，
// 不能被静态表掩盖。
func TestFetchTencentCodeBuddyUpstreamModels_MainlandDoesNotFallBack(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(`<html>500</html>`)),
	}}
	svc := &AccountTestService{httpUpstream: upstream}

	_, err := svc.fetchTencentCodeBuddyUpstreamModels(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.Error(t, err, "大陆站的真实故障不得被静态目录掩盖")
	// 客户端把上游 HTTP 错误统一包成 502（TENCENT_CODEBUDDY_MODELS_HTTP_ERROR），
	// 同步层据此判定为上游故障。
	require.Equal(t, http.StatusBadGateway, upstreamModelSyncStatusCode(err))
}

// TestFetchTencentCodeBuddyUpstreamModels_WorkBuddyPrefersLiveCatalog
// 兜底只在目录不可用时生效：一旦上游能返回目录，必须用实时结果。
func TestFetchTencentCodeBuddyUpstreamModels_WorkBuddyPrefersLiveCatalog(t *testing.T) {
	const payload = `{"code":0,"msg":"OK","data":{"agents":[{"name":"cli","models":["auto","glm-9.9"]}]}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}}
	svc := &AccountTestService{httpUpstream: upstream}

	models, err := svc.fetchTencentCodeBuddyUpstreamModels(context.Background(), workBuddyIntlTestAccount())
	require.NoError(t, err)
	require.Equal(t, []string{"auto", "glm-9.9"}, models)
}
