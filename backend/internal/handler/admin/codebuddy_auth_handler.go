package admin

import (
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CodeBuddyAuthHandler 负责 CodeBuddy / WorkBuddy（腾讯代码助手）的**设备授权登录**流程。
//
// 目的是让管理员不必手工抄 access_token，而是像 OpenAI 账号那样走引导：
//
//	POST /admin/accounts/codebuddy/auth/state?product=&region=  → {state, auth_url}
//	（用户在浏览器打开 auth_url 完成登录）
//	GET  /admin/accounts/codebuddy/auth/poll?state=&product=&region= → pending | ready(+credentials)
//
// 上游协议见 service.TencentCodeBuddyClient 的 StartAuthSession / PollAuthSession。
// 流程无状态：state 由前端保管并回传，服务端不落任何会话。
//
// product × region 决定站点（CodeBuddy/WorkBuddy × 大陆/国际）。两个接口必须收到
// **同一组**取值：轮询要去同一个站点取凭据，且写回的 credentials 要带上这两个维度。
// 取值非法时由 service 层归一化回落到大陆 CodeBuddy，不会报错。
//
// 另提供账号剩余积分的手动刷新：
//
//	POST /admin/accounts/:id/codebuddy/credits/refresh → 写回 extra 的积分字段
type CodeBuddyAuthHandler struct {
	provider    *service.TencentCodeBuddyProvider
	accountRepo service.AccountRepository
}

// NewCodeBuddyAuthHandler 构造 handler。
func NewCodeBuddyAuthHandler(provider *service.TencentCodeBuddyProvider, accountRepo service.AccountRepository) *CodeBuddyAuthHandler {
	return &CodeBuddyAuthHandler{provider: provider, accountRepo: accountRepo}
}

// RefreshCredits 查询账号剩余积分并写回 extra。
//
// 上游查询失败也会把错误写进 extra（codebuddy_credits_error），因此这里仍返回 200 +
// 写入的字段，让前端照常展示错误原因；只有账号不存在、平台不对或落库失败才走错误响应。
func (h *CodeBuddyAuthHandler) RefreshCredits(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	account, err := h.accountRepo.GetByID(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updates, err := service.RefreshTencentCodeBuddyCredits(c.Request.Context(), h.accountRepo, h.provider.Client(), account)
	if updates == nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"extra": updates})
}

// ModelCapabilities 列出 CodeBuddy / WorkBuddy 模型的生效能力参数（上游快照 / 内置表 / 默认）。
//
// GET /admin/accounts/codebuddy/model-capabilities
func (h *CodeBuddyAuthHandler) ModelCapabilities(c *gin.Context) {
	accounts, err := h.accountRepo.ListByPlatform(c.Request.Context(), service.PlatformTencentCodeBuddy)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.BuildTencentCodeBuddyModelCapabilityReport(accounts))
}

// RefreshModelCapabilities 从上游重新拉取所有账号的模型能力快照，返回刷新后的报告。
//
// POST /admin/accounts/codebuddy/model-capabilities/refresh
func (h *CodeBuddyAuthHandler) RefreshModelCapabilities(c *gin.Context) {
	ctx := c.Request.Context()
	refreshed, failed, err := service.RefreshTencentCodeBuddyModelCapabilities(ctx, h.accountRepo, h.provider.Client())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	accounts, err := h.accountRepo.ListByPlatform(ctx, service.PlatformTencentCodeBuddy)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"refreshed": refreshed,
		"failed":    failed,
		"report":    service.BuildTencentCodeBuddyModelCapabilityReport(accounts),
	})
}

// Start 生成授权链接。
func (h *CodeBuddyAuthHandler) Start(c *gin.Context) {
	session, err := h.provider.StartAuthSession(c.Request.Context(), c.Query("product"), c.Query("region"))
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
	result, err := h.provider.PollAuthSession(c.Request.Context(), c.Query("state"), c.Query("product"), c.Query("region"))
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
		// product / region 由服务端按请求维度写入，前端不参与推导：
		// 账号必须记住自己属于哪个站点，刷新与模型同步才能打到对的上游。
		"product": credential.Product,
		"region":  credential.Region,
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
