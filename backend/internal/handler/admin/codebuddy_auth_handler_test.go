package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// codeBuddyAuthTestUpstream 把请求真正发到 httptest.Server，并记录改写前的 URL，
// 用于断言 handler 是否把 product / region 正确透传到上游站点。
type codeBuddyAuthTestUpstream struct {
	server *httptest.Server

	mu       sync.Mutex
	captured []string
}

func newCodeBuddyAuthTestUpstream(t *testing.T, handler http.HandlerFunc) *codeBuddyAuthTestUpstream {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &codeBuddyAuthTestUpstream{server: server}
}

func (u *codeBuddyAuthTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.roundTrip(req)
}

func (u *codeBuddyAuthTestUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.roundTrip(req)
}

func (u *codeBuddyAuthTestUpstream) roundTrip(req *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.captured = append(u.captured, req.URL.String())
	u.mu.Unlock()

	req.URL.Scheme = "http"
	req.URL.Host = u.server.Listener.Addr().String()
	return http.DefaultClient.Do(req)
}

func (u *codeBuddyAuthTestUpstream) requests() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.captured...)
}

// codeBuddyAuthMux 模拟设备授权流的三个端点；token 端点按 wantPending 决定是否未完成。
func codeBuddyAuthMux(t *testing.T, statePath, tokenPath, accountPath string, pending bool) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case statePath:
			_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":{"state":"st-1","authUrl":"https://example.com/login?state=st-1"}}`)
		case tokenPath:
			if pending {
				_, _ = io.WriteString(w, `{"code":11217,"msg":"login ing...","data":null}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600}}`)
		case accountPath:
			_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":{"uid":"u-1","enterpriseId":"e-1","nickname":"n-1"}}`)
		default:
			http.NotFound(w, r)
		}
	}
}

func newCodeBuddyAuthHandler(t *testing.T, upstream http.HandlerFunc) *CodeBuddyAuthHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewCodeBuddyAuthHandler(service.NewTencentCodeBuddyProvider(newCodeBuddyAuthTestUpstream(t, upstream)), nil)
}

// TestCodeBuddyAuthHandler_PollPendingIsSuccess 锁定「用户未登录」是正常中间态：
// 必须是 200 + status=pending，而不是 4xx——否则前端会把等待中的轮询当成错误。
func TestCodeBuddyAuthHandler_PollPendingIsSuccess(t *testing.T) {
	h := newCodeBuddyAuthHandler(t, codeBuddyAuthMux(t, "/v2/plugin/auth/state", "/v2/plugin/auth/token", "/v2/plugin/login/account", true))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?state=st-1", nil)
	h.Poll(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Code int `json:"code"`
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "pending", body.Data.Status)
}

// TestCodeBuddyAuthHandler_StartAndPollRouteBySite 验证 handler 把 product / region
// 透传到站点解析：国际版 WorkBuddy 的授权与取凭据都必须打到 workbuddy.ai，
// 且返回的凭据带有该站点维度供前端持久化。
func TestCodeBuddyAuthHandler_StartAndPollRouteBySite(t *testing.T) {
	upstream := newCodeBuddyAuthTestUpstream(t, codeBuddyAuthMux(t,
		"/v2/plugin/auth/state", "/v2/plugin/auth/token", "/v2/plugin/login/account", false))
	gin.SetMode(gin.TestMode)
	h := NewCodeBuddyAuthHandler(service.NewTencentCodeBuddyProvider(upstream), nil)

	// 站点：WorkBuddy 国际版。
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/?product=workbuddy&region=global", bytes.NewReader([]byte("{}")))
	h.Start(c)
	require.Equal(t, http.StatusOK, w.Code)

	var startBody struct {
		Data struct {
			State   string `json:"state"`
			AuthURL string `json:"auth_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &startBody))
	require.Equal(t, "st-1", startBody.Data.State)
	require.NotEmpty(t, startBody.Data.AuthURL)

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/?state=st-1&product=workbuddy&region=global", nil)
	h.Poll(c2)
	require.Equal(t, http.StatusOK, w2.Code)

	var pollBody struct {
		Data struct {
			Status      string         `json:"status"`
			Nickname    string         `json:"nickname"`
			Credentials map[string]any `json:"credentials"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &pollBody))
	require.Equal(t, "ready", pollBody.Data.Status)
	require.Equal(t, "n-1", pollBody.Data.Nickname)
	require.Equal(t, "at-1", pollBody.Data.Credentials["access_token"])
	require.Equal(t, "rt-1", pollBody.Data.Credentials["refresh_token"])
	require.Equal(t, "u-1", pollBody.Data.Credentials["uid"])
	require.Equal(t, "e-1", pollBody.Data.Credentials["enterprise_id"])
	// 站点维度由服务端写入：前端据此建账号，后续 chat / 刷新才知道该打哪个站。
	require.Equal(t, "workbuddy", pollBody.Data.Credentials["product"])
	require.Equal(t, "global", pollBody.Data.Credentials["region"])
	// domain 未显式返回时不下发，避免把默认值固化。
	require.NotContains(t, pollBody.Data.Credentials, "domain")

	// 上游请求都应打到 workbuddy.ai；国际版登录完成后顺带查询注册激活状态。
	require.Equal(t, []string{
		"https://www.workbuddy.ai/v2/plugin/auth/state?platform=CLI",
		"https://www.workbuddy.ai/v2/plugin/auth/token?state=st-1",
		"https://www.workbuddy.ai/v2/plugin/login/account?state=st-1",
		"https://www.workbuddy.ai/auth/realms/copilot/overseas/user/register?userId=u-1",
	}, upstream.requests())
}

// TestCodeBuddyAuthHandler_UnknownSiteFallsBack 非法取值不报错，回落大陆 CodeBuddy。
func TestCodeBuddyAuthHandler_UnknownSiteFallsBack(t *testing.T) {
	upstream := newCodeBuddyAuthTestUpstream(t, codeBuddyAuthMux(t,
		"/v2/plugin/auth/state", "/v2/plugin/auth/token", "/v2/plugin/login/account", false))
	gin.SetMode(gin.TestMode)
	h := NewCodeBuddyAuthHandler(service.NewTencentCodeBuddyProvider(upstream), nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?state=st-1&product=bogus&region=mars", nil)
	h.Poll(c)
	require.Equal(t, http.StatusOK, w.Code)

	var pollBody struct {
		Data struct {
			Credentials map[string]any `json:"credentials"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pollBody))
	// 凭据里存的是归一化后的值，不是原始输入。
	require.Equal(t, "codebuddy", pollBody.Data.Credentials["product"])
	require.Equal(t, "china", pollBody.Data.Credentials["region"])

	require.Equal(t, "https://copilot.tencent.com/v2/plugin/auth/token?state=st-1", upstream.requests()[0])
}
