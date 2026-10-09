package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tencentCodeBuddyAuthMux 按路径模拟设备授权流的三个上游端点。
func tencentCodeBuddyAuthMux(tokenStatus int, tokenPayload, accountPayload string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			jsonHandler(http.StatusOK,
				`{"code":0,"msg":"OK","data":{"state":"st-1","authUrl":"https://copilot.tencent.com/login?platform=CLI&state=st-1"}}`)(w, r)
		case "/v2/plugin/auth/token":
			jsonHandler(tokenStatus, tokenPayload)(w, r)
		case "/v2/plugin/login/account":
			jsonHandler(http.StatusOK, accountPayload)(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}

func TestTencentCodeBuddyStartAuthSession(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, tencentCodeBuddyAuthMux(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at"}}`, `{"code":0,"data":{}}`))
	client := NewTencentCodeBuddyClient(upstream)

	session, err := client.StartAuthSession(context.Background(), "", "")
	require.NoError(t, err)
	require.Equal(t, "st-1", session.State)
	require.Contains(t, session.AuthURL, "state=st-1")

	requests := upstream.requests()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodPost, requests[0].Method)
	require.Equal(t, "https://copilot.tencent.com/v2/plugin/auth/state?platform=CLI", requests[0].URL)
	require.Equal(t, "WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1", requests[0].Header.Get("User-Agent"))
	require.Equal(t, "{}", requests[0].Body)
}

func TestTencentCodeBuddyStartAuthSession_RejectsIncompleteResponse(t *testing.T) {
	// 业务码非 0
	upstream := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{"code":500,"msg":"boom"}`))
	_, err := NewTencentCodeBuddyClient(upstream).StartAuthSession(context.Background(), "", "")
	requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_AUTH_START_REJECTED")

	// code=0 但缺 authUrl
	upstream2 := newTencentCodeBuddyTestUpstream(t, jsonHandler(http.StatusOK, `{"code":0,"data":{"state":"st-1"}}`))
	_, err = NewTencentCodeBuddyClient(upstream2).StartAuthSession(context.Background(), "", "")
	requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_AUTH_STATE_INCOMPLETE")

	// 无出网能力
	_, err = NewTencentCodeBuddyClient(nil).StartAuthSession(context.Background(), "", "")
	requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_NOT_CONFIGURED")
}

func TestTencentCodeBuddyPollAuthSession_Pending(t *testing.T) {
	// 实测上游用 11217 "login ing..." 表示尚未完成登录。
	upstream := newTencentCodeBuddyTestUpstream(t, tencentCodeBuddyAuthMux(http.StatusOK,
		`{"code":11217,"msg":"11217:login ing..."}`, `{"code":0,"data":{}}`))
	client := NewTencentCodeBuddyClient(upstream)

	_, err := client.PollAuthSession(context.Background(), "st-1", "", "")
	require.ErrorIs(t, err, ErrTencentCodeBuddyAuthPending)
}

func TestTencentCodeBuddyPollAuthSession_Ready(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, tencentCodeBuddyAuthMux(http.StatusOK,
		`{"code":0,"msg":"OK","data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600,"domain":"www.codebuddy.cn"}}`,
		`{"code":0,"data":{"uid":"u-9","enterpriseId":"e-9","nickname":"测试账号"}}`))
	client := NewTencentCodeBuddyClient(upstream)

	result, err := client.PollAuthSession(context.Background(), "st-1", "", "")
	require.NoError(t, err)

	cred := result.Credential
	require.Equal(t, "at-1", cred.AccessToken)
	require.Equal(t, "rt-1", cred.RefreshToken)
	require.Equal(t, "u-9", cred.UserID)
	require.Equal(t, "e-9", cred.EnterpriseID)
	require.Equal(t, "www.codebuddy.cn", cred.Domain)
	require.Equal(t, TencentCodeBuddyProductCodeBuddy, cred.Product)
	require.Equal(t, TencentCodeBuddyRegionChina, cred.Region)
	require.Equal(t, "测试账号", result.Nickname)
	require.NotNil(t, cred.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(time.Hour), *cred.ExpiresAt, 2*time.Minute)

	requests := upstream.requests()
	require.Len(t, requests, 2)
	require.Equal(t, "https://copilot.tencent.com/v2/plugin/auth/token?state=st-1", requests[0].URL)
	require.Equal(t, "https://copilot.tencent.com/v2/plugin/login/account?state=st-1", requests[1].URL)
	// 账号信息接口带 Bearer。
	require.Equal(t, "Bearer at-1", requests[1].Header.Get("Authorization"))
	// 授权类请求不绑定账号：不携带 X-User-Id 等身份头。
	require.Empty(t, requests[0].Header.Get("X-User-Id"))
}

func TestTencentCodeBuddyPollAuthSession_AccountInfoIsBestEffort(t *testing.T) {
	// 账号信息端点失败/无数据时，凭据仍必须可用（uid 留空）。
	upstream := newTencentCodeBuddyTestUpstream(t, tencentCodeBuddyAuthMux(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at-1"}}`, `{"code":403,"msg":"forbidden"}`))
	client := NewTencentCodeBuddyClient(upstream)

	result, err := client.PollAuthSession(context.Background(), "st-1", "", "")
	require.NoError(t, err)
	require.Equal(t, "at-1", result.Credential.AccessToken)
	require.Empty(t, result.Credential.UserID)
	require.Empty(t, result.Nickname)
	// 上游未回传 domain 时留空，交由默认值处理。
	require.Empty(t, result.Credential.Domain)
}

func TestTencentCodeBuddyPollAuthSession_RequiresState(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, tencentCodeBuddyAuthMux(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at"}}`, `{"code":0,"data":{}}`))
	client := NewTencentCodeBuddyClient(upstream)

	_, err := client.PollAuthSession(context.Background(), "   ", "", "")
	requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_AUTH_MISSING_STATE")
	require.Empty(t, upstream.requests(), "缺 state 时不应发起上游请求")
}

func TestTencentCodeBuddyProviderAuthSessionPassthrough(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, tencentCodeBuddyAuthMux(http.StatusOK,
		`{"code":0,"data":{"accessToken":"at-1"}}`, `{"code":0,"data":{"uid":"u-1"}}`))
	provider := NewTencentCodeBuddyProvider(upstream)

	session, err := provider.StartAuthSession(context.Background(), "", "")
	require.NoError(t, err)
	require.Equal(t, "st-1", session.State)

	result, err := provider.PollAuthSession(context.Background(), session.State, "", "")
	require.NoError(t, err)
	require.Equal(t, "at-1", result.Credential.AccessToken)
	require.Equal(t, "u-1", result.Credential.UserID)

	// 未配置的 provider 必须报 NOT_CONFIGURED 而不是 panic。
	_, err = NewTencentCodeBuddyProvider(nil).StartAuthSession(context.Background(), "", "")
	requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_NOT_CONFIGURED")
	_, err = NewTencentCodeBuddyProvider(nil).PollAuthSession(context.Background(), "st-1", "", "")
	requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_NOT_CONFIGURED")
}

// TestTencentCodeBuddyAuthSession_RoutesBySite 验证授权流整体按 product × region 落到正确站点。
// 授权链接由站点自行派生，因此选对 host 就等于选对了登录站点；同时凭据要带上这两个维度，
// 否则后续 chat / 刷新会回落大陆站。
func TestTencentCodeBuddyAuthSession_RoutesBySite(t *testing.T) {
	cases := []struct {
		name        string
		product     string
		region      string
		wantProduct string
		wantRegion  string
		wantState   string
		wantToken   string
		wantAccount string
	}{
		{
			name:        "workbuddy global",
			product:     TencentCodeBuddyProductWorkBuddy,
			region:      TencentCodeBuddyRegionGlobal,
			wantProduct: TencentCodeBuddyProductWorkBuddy,
			wantRegion:  TencentCodeBuddyRegionGlobal,
			wantState:   "https://www.workbuddy.ai/v2/plugin/auth/state?platform=CLI",
			wantToken:   "https://www.workbuddy.ai/v2/plugin/auth/token?state=st-1",
			wantAccount: "https://www.workbuddy.ai/v2/plugin/login/account?state=st-1",
		},
		{
			name:        "codebuddy global",
			product:     TencentCodeBuddyProductCodeBuddy,
			region:      TencentCodeBuddyRegionGlobal,
			wantProduct: TencentCodeBuddyProductCodeBuddy,
			wantRegion:  TencentCodeBuddyRegionGlobal,
			wantState:   "https://www.codebuddy.ai/v2/plugin/auth/state?platform=CLI",
			wantToken:   "https://www.codebuddy.ai/v2/plugin/auth/token?state=st-1",
			wantAccount: "https://www.codebuddy.ai/v2/plugin/login/account?state=st-1",
		},
		{
			name:    "unknown falls back to mainland codebuddy",
			product: "bogus",
			region:  "mars",
			// 回落到默认站点时，凭据里存的必须是归一化后的值，而不是原始输入。
			wantProduct: TencentCodeBuddyProductCodeBuddy,
			wantRegion:  TencentCodeBuddyRegionChina,
			wantState:   "https://copilot.tencent.com/v2/plugin/auth/state?platform=CLI",
			wantToken:   "https://copilot.tencent.com/v2/plugin/auth/token?state=st-1",
			wantAccount: "https://copilot.tencent.com/v2/plugin/login/account?state=st-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newTencentCodeBuddyTestUpstream(t, tencentCodeBuddyAuthMux(http.StatusOK,
				`{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600}}`,
				`{"code":0,"data":{"uid":"u-1","enterpriseId":"e-1","nickname":"n-1"}}`))
			client := NewTencentCodeBuddyClient(upstream)

			_, err := client.StartAuthSession(context.Background(), tc.product, tc.region)
			require.NoError(t, err)
			result, err := client.PollAuthSession(context.Background(), "st-1", tc.product, tc.region)
			require.NoError(t, err)

			// 凭据必须记住站点维度，且与请求维度一致（回落到默认时存归一化值）。
			require.Equal(t, tc.wantProduct, result.Credential.Product)
			require.Equal(t, tc.wantRegion, result.Credential.Region)
			require.Equal(t, "at-1", result.Credential.AccessToken)
			require.Equal(t, "rt-1", result.Credential.RefreshToken)
			require.Equal(t, "u-1", result.Credential.UserID)
			require.Equal(t, "e-1", result.Credential.EnterpriseID)
			require.Equal(t, "n-1", result.Nickname)
			// 上游未回传 domain：留空，由站点默认值接管。
			require.Empty(t, result.Credential.Domain)
			require.Equal(t, ResolveTencentCodeBuddyEndpoint(tc.product, tc.region).Domain, result.Credential.Endpoint().Domain)

			requests := upstream.requests()
			require.Len(t, requests, 3)
			require.Equal(t, tc.wantState, requests[0].URL)
			require.Equal(t, tc.wantToken, requests[1].URL)
			require.Equal(t, tc.wantAccount, requests[2].URL)
			// domain 未显式提供时不应把默认值写进凭据（避免固化）。
			require.NotContains(t, result.Credential.Apply(nil), tencentCodeBuddyCredDomain)
		})
	}
}
