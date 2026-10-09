package service

import (
	"context"
	"net/http"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// TencentCodeBuddyTokenRefresher 实现 TokenRefresher，负责 CodeBuddy / WorkBuddy
// access_token 的续期。上游凭据为短期令牌（含 expires_at），过期后必须刷新，
// 否则转发会 401。
type TencentCodeBuddyTokenRefresher struct {
	provider *TencentCodeBuddyProvider
}

// NewTencentCodeBuddyTokenRefresher 构造刷新器。
func NewTencentCodeBuddyTokenRefresher(provider *TencentCodeBuddyProvider) *TencentCodeBuddyTokenRefresher {
	return &TencentCodeBuddyTokenRefresher{provider: provider}
}

// CacheKey 返回用于分布式锁的缓存键。
func (r *TencentCodeBuddyTokenRefresher) CacheKey(account *Account) string {
	if account == nil {
		return "token_refresh:tencent_codebuddy:0"
	}
	return "token_refresh:tencent_codebuddy:" + strconv.FormatInt(account.ID, 10)
}

// CanRefresh 报告是否处理该账号。
func (r *TencentCodeBuddyTokenRefresher) CanRefresh(account *Account) bool {
	return account != nil && account.IsTencentCodeBuddy()
}

// NeedsRefresh 判断是否该刷新令牌。
// 读取走强类型凭据（ParseTencentCodeBuddyCredential），与写入路径共用同一份键契约；
// 过期时间优先取 expires_at，缺失时取 JWT exp；令牌签发满一天也会刷新（保活）。
func (r *TencentCodeBuddyTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	now := time.Now()
	// 转发时遇到 401 会暂停账号并标记原因，此时不论令牌看起来是否过期都要刷新。
	if tencentCodeBuddyNeedsForcedRefresh(account, now) {
		return true
	}
	return account.TencentCodeBuddyCredential().NeedsRefresh(now, refreshWindow)
}

// Refresh 调用官方刷新接口，返回更新后的 credentials（保留原有字段）。
func (r *TencentCodeBuddyTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.provider == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED", "tencent codebuddy provider is not configured")
	}
	return r.provider.Refresh(ctx, account)
}
