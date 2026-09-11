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

// NeedsRefresh 基于 expires_at 判断是否在刷新窗口内。
// 读取走强类型凭据（ParseTencentCodeBuddyCredential），与写入路径共用同一份键契约；
// expires_at 缺失时不刷新（与既有无过期信息不主动续期的策略一致）。
func (r *TencentCodeBuddyTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	return account.TencentCodeBuddyCredential().NeedsRefresh(time.Now(), refreshWindow)
}

// Refresh 调用官方刷新接口，返回更新后的 credentials（保留原有字段）。
func (r *TencentCodeBuddyTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.provider == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED", "tencent codebuddy provider is not configured")
	}
	return r.provider.Refresh(ctx, account)
}
