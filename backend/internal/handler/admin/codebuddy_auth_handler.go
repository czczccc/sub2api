package admin

import (
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CodeBuddyAuthHandler 负责 CodeBuddy（腾讯代码助手）的**设备授权登录**流程。
//
// 目的是让管理员不必手工抄 access_token，而是像 OpenAI 账号那样走引导：
//
//	POST /admin/accounts/codebuddy/auth/state  → {state, auth_url}
//	（用户在浏览器打开 auth_url 完成登录）
//	GET  /admin/accounts/codebuddy/auth/poll?state=... → pending | ready(+credentials)
//
// 上游协议见 service.TencentCodeBuddyClient 的 StartAuthSession / PollAuthSession。
// 流程无状态：state 由前端保管并回传，服务端不落任何会话。
type CodeBuddyAuthHandler struct {
	provider *service.TencentCodeBuddyProvider
}

// NewCodeBuddyAuthHandler 构造 handler。
func NewCodeBuddyAuthHandler(provider *service.TencentCodeBuddyProvider) *CodeBuddyAuthHandler {
	return &CodeBuddyAuthHandler{provider: provider}
}

// Start 生成授权链接。
func (h *CodeBuddyAuthHandler) Start(c *gin.Context) {
	session, err := h.provider.StartAuthSession(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"state":    session.State,
		"auth_url": session.AuthURL,
	})
}

// Poll 轮询授权结果。
//
// 用户尚未完成登录是**正常中间态**，因此返回 200 + status=pending，让前端继续轮询
// 而不是弹错误；只有真正的传输/解析失败才走错误响应。
func (h *CodeBuddyAuthHandler) Poll(c *gin.Context) {
	result, err := h.provider.PollAuthSession(c.Request.Context(), c.Query("state"))
	if err != nil {
		if infraerrors.Reason(err) == "TENCENT_CODEBUDDY_AUTH_PENDING" {
			response.Success(c, gin.H{"status": "pending"})
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	credential := result.Credential
	credentials := gin.H{
		"access_token":  credential.AccessToken,
		"refresh_token": credential.RefreshToken,
		"uid":           credential.UserID,
		"enterprise_id": credential.EnterpriseID,
	}
	// domain 只在非空时返回：留空让后端使用默认值，避免把默认值当成显式配置。
	if credential.Domain != "" {
		credentials["domain"] = credential.Domain
	}
	response.Success(c, gin.H{
		"status":      "ready",
		"credentials": credentials,
		"nickname":    result.Nickname,
	})
}
