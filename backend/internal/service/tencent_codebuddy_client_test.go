package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
		// 历史遗留的国际版取值必须被收敛到大陆版：当前只接入 CodeBuddy 大陆版。
		"product":    "WORKBUDDY",
		"region":     "GLOBAL",
		"expires_at": "2030-01-02T03:04:05Z",
	})
	require.Equal(t, "at", cred.AccessToken)
	require.Equal(t, "rt", cred.RefreshToken)
	require.Equal(t, "u1", cred.UserID)
	require.Equal(t, "e1", cred.EnterpriseID)
	require.Equal(t, TencentCodeBuddyProductCodeBuddy, cred.Product)
	require.Equal(t, TencentCodeBuddyRegionChina, cred.Region)
	require.True(t, cred.HasAccessToken())
	require.NotNil(t, cred.ExpiresAt)
	require.Equal(t, time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), *cred.ExpiresAt)

	// 缺失字段落到零值 + 归一化默认，且永不报错。
	empty := ParseTencentCodeBuddyCredential(nil)
	require.False(t, empty.HasAccessToken())
	require.Nil(t, empty.ExpiresAt)
	require.Equal(t, TencentCodeBuddyProductCodeBuddy, empty.Product)
	require.Equal(t, TencentCodeBuddyRegionChina, empty.Region)
	require.Nil(t, ParseTencentCodeBuddyCredential(map[string]any{"expires_at": "not-a-time"}).ExpiresAt)

	// Unix 毫秒时间戳。
	require.NotNil(t, ParseTencentCodeBuddyCredential(map[string]any{"expires_at": "1893456000000"}).ExpiresAt)
	// Unix 秒时间戳。
	require.NotNil(t, ParseTencentCodeBuddyCredential(map[string]any{"expires_at": "1893456000"}).ExpiresAt)
}

func TestTencentCodeBuddyCredentialDomainResolution(t *testing.T) {
	// 未显式提供 domain 时使用固定的大陆默认值（与参考实现 DEFAULT_DOMAIN 一致）。
	plain := ParseTencentCodeBuddyCredential(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductCodeBuddy,
		tencentCodeBuddyCredRegion:  TencentCodeBuddyRegionChina,
	})
	require.Equal(t, tencentCodeBuddyDomain, plain.Domain)
	require.Equal(t, "www.codebuddy.cn", plain.Domain)
	require.Equal(t, tencentCodeBuddyDomain, plain.Endpoint().Domain)

	// 账号级 domain 覆盖默认值（对应官方登录态的 auth.domain 语义）。
	custom := ParseTencentCodeBuddyCredential(map[string]any{
		tencentCodeBuddyCredDomain: "  tenant.example.com  ",
	})
	require.Equal(t, "tenant.example.com", custom.Domain)
	require.Equal(t, "tenant.example.com", custom.Endpoint().Domain)

	// 非默认 domain 落盘；默认值不落盘，避免把默认值当成显式配置固化。
	require.Equal(t, "tenant.example.com", custom.Apply(nil)[tencentCodeBuddyCredDomain])
	require.NotContains(t, plain.Apply(nil), tencentCodeBuddyCredDomain)
}

func TestTencentCodeBuddyCredentialApplyPreservesOtherKeys(t *testing.T) {
	cred := ParseTencentCodeBuddyCredential(map[string]any{
		"access_token": "at-new",
		// 历史国际版取值：应被收敛到大陆版。
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
	require.Equal(t, TencentCodeBuddyProductCodeBuddy, out[tencentCodeBuddyCredProduct])
	require.Equal(t, TencentCodeBuddyRegionChina, out[tencentCodeBuddyCredRegion])
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

	future := now.Add(2 * time.Hour)
	require.False(t, TencentCodeBuddyCredential{ExpiresAt: &future}.NeedsRefresh(now, window))

	soon := now.Add(10 * time.Minute)
	require.True(t, TencentCodeBuddyCredential{ExpiresAt: &soon}.NeedsRefresh(now, window))

	past := now.Add(-time.Hour)
	require.True(t, TencentCodeBuddyCredential{ExpiresAt: &past}.NeedsRefresh(now, window))
}

// ===== 2. Endpoint 选择 =====

func TestResolveTencentCodeBuddyEndpoint_IsFixedToMainlandCodeBuddy(t *testing.T) {
	// 当前只接入大陆版：任何输入都解析到同一个 host + 同一个 X-Domain。
	// host 依据：参考实现 codebuddy2openai 硬编码 copilot.tencent.com；
	// workbuddy2api 有断言 "bases must be CN regardless of domain"。
	cases := [][2]string{
		{TencentCodeBuddyProductCodeBuddy, TencentCodeBuddyRegionChina},
		{"bogus", "mars"},           // 非法输入收敛
		{" WorkBuddy ", " GLOBAL "}, // 历史国际版取值同样收敛
		{"", ""},                    // 缺省
	}
	for _, tc := range cases {
		endpoint := ResolveTencentCodeBuddyEndpoint(tc[0], tc[1])
		require.Equal(t, "https://copilot.tencent.com/v2", endpoint.BaseURL, "input=%q/%q", tc[0], tc[1])
		require.Equal(t, "www.codebuddy.cn", endpoint.Domain, "input=%q/%q", tc[0], tc[1])
		require.Equal(t, TencentCodeBuddyProductCodeBuddy, endpoint.Product)
		require.Equal(t, TencentCodeBuddyRegionChina, endpoint.Region)
	}

	// 受支持枚举只有大陆版一项。
	require.Equal(t, []string{TencentCodeBuddyProductCodeBuddy}, TencentCodeBuddyProducts())
	require.Equal(t, []string{TencentCodeBuddyRegionChina}, TencentCodeBuddyRegions())
	require.True(t, IsTencentCodeBuddyProduct("codebuddy"))
	require.False(t, IsTencentCodeBuddyProduct("workbuddy"))
	require.True(t, IsTencentCodeBuddyRegion("china"))
	require.False(t, IsTencentCodeBuddyRegion("global"))

	// 账号无法通过 credentials.base_url 覆盖 endpoint。
	account := tencentCodeBuddyTestAccount(map[string]any{"base_url": "https://evil.example.com"})
	require.Equal(t, tencentCodeBuddyAPIRoot, account.TencentCodeBuddyBaseURL())
	require.Equal(t, tencentCodeBuddyAPIRoot, account.TencentCodeBuddyCredential().Endpoint().BaseURL)
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
	require.Equal(t, tencentCodeBuddyUserAgent, requests[0].Header.Get("User-Agent"))
	require.Equal(t, tencentCodeBuddyDomain, requests[0].Header.Get("X-Domain"))
	require.Equal(t, "uid-1", requests[0].Header.Get("X-User-Id"))
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
			// 模型目录挂在 host 根，不在 /v2 下。
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

func TestTencentCodeBuddyClientChatCompletion_AlwaysMainlandEndpoint(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{"id":"cmpl-2"}`))
	client := NewTencentCodeBuddyClient(upstream)
	// 即便凭据里带历史国际版取值，也必须打到大陆 endpoint 与 X-Domain。
	account := tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredProduct: "workbuddy",
		tencentCodeBuddyCredRegion:  "global",
	})

	resp, err := client.ChatCompletion(context.Background(), account, []byte(`{"model":"auto"}`), false)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	requests := upstream.requests()
	require.Len(t, requests, 1)
	require.Equal(t, "https://copilot.tencent.com/v2/chat/completions", requests[0].URL)
	require.Equal(t, "www.codebuddy.cn", requests[0].Header.Get("X-Domain"))
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
	require.Equal(t, tencentCodeBuddyUserAgent, header.Get("User-Agent"))
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
