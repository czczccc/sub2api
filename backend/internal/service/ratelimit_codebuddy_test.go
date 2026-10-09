//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func codeBuddyRateLimitAccount() *Account {
	return &Account{
		ID:       42,
		Platform: PlatformTencentCodeBuddy,
		Type:     AccountTypeAPIKey,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "at",
			"refresh_token": "rt",
			"uid":           "uid-1",
		},
	}
}

func handleCodeBuddyError(t *testing.T, account *Account, status int, body string, model ...string) (*commandCodeRateLimitRepo, bool) {
	t.Helper()
	repo := &commandCodeRateLimitRepo{}
	disable := NewRateLimitService(repo, nil, &config.Config{}, nil, nil).HandleUpstreamError(
		context.Background(), account, status, http.Header{}, []byte(body), model...)
	return repo, disable
}

// 积分耗尽不再永久置错误，而是暂停到次日北京时间 04:00。
func TestHandleUpstreamError_CodeBuddyCreditsExhaustedPausesUntil4AM(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"402":       {http.StatusPaymentRequired, `{"code":14001,"msg":"积分不足"}`},
		"429 14018": {http.StatusTooManyRequests, `{"code":14018,"msg":"credits exhausted"}`},
		"403 文案":    {http.StatusForbidden, `{"code":1,"msg":"余额不足，请充值"}`},
	} {
		t.Run(name, func(t *testing.T) {
			repo, disable := handleCodeBuddyError(t, codeBuddyRateLimitAccount(), tc.status, tc.body)
			require.True(t, disable)
			require.Zero(t, repo.setErrorCalls)
			require.Equal(t, 1, repo.tempCalls)
			require.True(t, strings.HasPrefix(repo.lastTempReason, tencentCodeBuddyCreditsExhaustedReason), repo.lastTempReason)
			local := repo.tempUntil.In(tencentCodeBuddyResetLoc)
			require.Equal(t, 4, local.Hour())
			require.Zero(t, local.Minute())
			require.True(t, repo.tempUntil.After(time.Now()))
			require.True(t, repo.tempUntil.Before(time.Now().Add(24*time.Hour+time.Minute)))
		})
	}
}

func TestTencentCodeBuddyNextCreditsReset(t *testing.T) {
	before := time.Date(2026, 10, 9, 3, 0, 0, 0, tencentCodeBuddyResetLoc)
	require.Equal(t, time.Date(2026, 10, 9, 4, 0, 0, 0, tencentCodeBuddyResetLoc), tencentCodeBuddyNextCreditsReset(before))
	after := time.Date(2026, 10, 9, 21, 0, 0, 0, tencentCodeBuddyResetLoc)
	require.Equal(t, time.Date(2026, 10, 10, 4, 0, 0, 0, tencentCodeBuddyResetLoc), tencentCodeBuddyNextCreditsReset(after))
}

// 6004 是单模型用量超限：只限该模型，冷却到文案里的重置时间，账号本身不停调。
func TestHandleUpstreamError_CodeBuddy6004LimitsOnlyModel(t *testing.T) {
	reset := time.Now().In(tencentCodeBuddyResetLoc).Add(3 * time.Hour).Truncate(time.Second)
	body := `{"code":6004,"msg":"模型用量超限，将在 ` + reset.Format("2006-01-02 15:04:05") + ` 重置"}`
	repo, disable := handleCodeBuddyError(t, codeBuddyRateLimitAccount(), http.StatusTooManyRequests, body, "glm-5.2")

	require.False(t, disable)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.tempCalls)
	require.Contains(t, repo.modelLimits, "glm-5.2")
	require.True(t, repo.modelLimits["glm-5.2"].Equal(reset))
}

// 账号级 429 带重置文案：冷却到该时间，而不是秒级兜底。
func TestHandleUpstreamError_CodeBuddy429UsesResetText(t *testing.T) {
	reset := time.Now().In(tencentCodeBuddyResetLoc).Add(2 * time.Hour).Truncate(time.Second)
	body := `{"code":11140,"msg":"rate limited, will reset at ` + reset.Format("2006-01-02 15:04:05") + ` UTC+8"}`
	repo, _ := handleCodeBuddyError(t, codeBuddyRateLimitAccount(), http.StatusTooManyRequests, body, "glm-5.2")

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.True(t, repo.lastRateLimitedAt.Equal(reset))
	require.Empty(t, repo.modelLimits)
}

func TestHandleUpstreamError_CodeBuddySessionDeadAndBanSetError(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"12153":           {http.StatusUnauthorized, `{"code":12153,"msg":"Offline user session not found"}`},
		"request illegal": {http.StatusForbidden, `{"code":11140,"msg":"request illegal"}`},
	} {
		t.Run(name, func(t *testing.T) {
			repo, disable := handleCodeBuddyError(t, codeBuddyRateLimitAccount(), tc.status, tc.body)
			require.True(t, disable)
			require.Equal(t, 1, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
		})
	}
}

// 401 但还有 refresh_token：暂停并等后台刷新，不永久置错误。
func TestHandleUpstreamError_CodeBuddy401PausesForRefresh(t *testing.T) {
	account := codeBuddyRateLimitAccount()
	repo, disable := handleCodeBuddyError(t, account, http.StatusUnauthorized, `{"code":401,"msg":"token expired"}`)
	require.True(t, disable)
	require.Zero(t, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.True(t, strings.HasPrefix(repo.lastTempReason, tencentCodeBuddyTokenInvalidReason))

	// 暂停原因让刷新器强制刷新，即使令牌看起来没过期。
	until := time.Now().Add(5 * time.Minute)
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = repo.lastTempReason
	require.True(t, NewTencentCodeBuddyTokenRefresher(nil).NeedsRefresh(account, 30*time.Minute))

	// 没有 refresh_token 的 401 交回默认逻辑（置错误）。
	noRT := codeBuddyRateLimitAccount()
	delete(noRT.Credentials, "refresh_token")
	repo, _ = handleCodeBuddyError(t, noRT, http.StatusUnauthorized, `{"code":401,"msg":"token expired"}`)
	require.Equal(t, 1, repo.setErrorCalls)
}

func TestHandleUpstreamError_CodeBuddy403Variants(t *testing.T) {
	// WAF 拦截页（无业务信封）：短暂停调。
	repo, disable := handleCodeBuddyError(t, codeBuddyRateLimitAccount(), http.StatusForbidden, `<html>403 Forbidden</html>`)
	require.True(t, disable)
	require.Zero(t, repo.setErrorCalls)
	require.Equal(t, tencentCodeBuddyWAFReason, repo.lastTempReason)
	require.WithinDuration(t, time.Now().Add(tencentCodeBuddyWAFCooldown), repo.tempUntil, 5*time.Second)

	// 内容审核误拦：不罚账号。
	repo, disable = handleCodeBuddyError(t, codeBuddyRateLimitAccount(), http.StatusForbidden,
		`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`)
	require.False(t, disable)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
}

// 11102：该账号没有这个模型，只让该账号避开它。
func TestHandleUpstreamError_CodeBuddyModelNotAvailable(t *testing.T) {
	repo, disable := handleCodeBuddyError(t, codeBuddyRateLimitAccount(), http.StatusBadRequest,
		`{"code":11102,"msg":"service info not found"}`, "kimi-k2")
	require.False(t, disable)
	require.Contains(t, repo.modelLimits, "kimi-k2")
	require.WithinDuration(t, time.Now().Add(tencentCodeBuddyModelBlockedCooldown), repo.modelLimits["kimi-k2"], 5*time.Second)
}
