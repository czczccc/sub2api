package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// checkinRoutes 返回按路径分发的 mock 上游：status 与 claim 两个接口各自给定响应。
func checkinRoutes(statusCode int, status string, claimCode int, claim string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case tencentCodeBuddyCheckinStatusPath:
			w.WriteHeader(statusCode)
			_, _ = io.WriteString(w, status)
		case tencentCodeBuddyCheckinClaimPath:
			w.WriteHeader(claimCode)
			_, _ = io.WriteString(w, claim)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestTencentCodeBuddyCheckin_ClaimsWhenNotCheckedIn(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, checkinRoutes(
		http.StatusOK, `{"code":0,"data":{"active":true,"today_checked_in":false,"streak_days":7,"total_credits":700}}`,
		http.StatusOK, `{"code":0,"data":{"credit":100}}`,
	))
	account := tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductWorkBuddy,
	})

	result, err := NewTencentCodeBuddyClient(upstream).Checkin(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, TencentCodeBuddyCheckinClaimed, result.Result)
	require.Equal(t, "成功领取 100 积分（连续 7 天，累计 700 积分）", result.Message)

	reqs := upstream.requests()
	require.Len(t, reqs, 3) // 查询 → 领取 → 复查
	// 大陆站签到统一走 copilot.tencent.com，X-Domain 仍是账号所属站点。
	require.Equal(t, "copilot.tencent.com", reqs[0].Host)
	require.Equal(t, http.MethodPost, reqs[1].Method)
	require.Contains(t, reqs[1].URL, tencentCodeBuddyCheckinClaimPath)
	require.Equal(t, "Bearer at-test", reqs[1].Header.Get("Authorization"))
	require.Equal(t, "uid-1", reqs[1].Header.Get("X-User-Id"))
	require.Equal(t, "ent-1", reqs[1].Header.Get("X-Enterprise-Id"))
	require.Equal(t, tencentWorkBuddyDomainChina, reqs[1].Header.Get("X-Domain"))
	require.Equal(t, tencentCodeBuddyCheckinUserAgent, reqs[1].Header.Get("User-Agent"))
}

func TestTencentCodeBuddyCheckin_AlreadyCheckedInSkipsClaim(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, checkinRoutes(
		http.StatusOK, `{"active":true,"today_checked_in":true,"streak_days":3}`,
		http.StatusOK, `{"credit":100}`,
	))
	result, err := NewTencentCodeBuddyClient(upstream).Checkin(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, TencentCodeBuddyCheckinAlready, result.Result)
	require.Equal(t, "今日已签过（连续 3 天）", result.Message)
	require.Len(t, upstream.requests(), 1)
}

func TestTencentCodeBuddyCheckin_ServerSaysAlreadyClaimed(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, checkinRoutes(
		http.StatusOK, `{"active":true,"today_checked_in":false}`,
		http.StatusBadRequest, `{"code":10001,"msg":"今日已签到"}`,
	))
	result, err := NewTencentCodeBuddyClient(upstream).Checkin(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, TencentCodeBuddyCheckinAlready, result.Result)
}

func TestTencentCodeBuddyCheckin_InactiveAndAuthFailures(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, checkinRoutes(
		http.StatusOK, `{"data":{"active":false,"activity_name":"春季签到"}}`, http.StatusOK, `{}`,
	))
	result, err := NewTencentCodeBuddyClient(upstream).Checkin(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, TencentCodeBuddyCheckinInactive, result.Result)
	require.Equal(t, "签到活动未开启（春季签到）", result.Message)

	upstream = newTencentCodeBuddyTestUpstream(t, checkinRoutes(http.StatusUnauthorized, ``, http.StatusOK, `{}`))
	result, err = NewTencentCodeBuddyClient(upstream).Checkin(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, TencentCodeBuddyCheckinFailed, result.Result)
	require.Contains(t, result.Message, "HTTP 401")

	// 领取接口出错时，空 body 不能被当成"今日已签"。
	upstream = newTencentCodeBuddyTestUpstream(t, checkinRoutes(
		http.StatusOK, `{"active":true,"today_checked_in":false}`, http.StatusInternalServerError, ``,
	))
	result, err = NewTencentCodeBuddyClient(upstream).Checkin(context.Background(), tencentCodeBuddyTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, TencentCodeBuddyCheckinFailed, result.Result)
}

func TestTencentCodeBuddyCheckin_GlobalUsesSiteHost(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, checkinRoutes(
		http.StatusOK, `{"active":true,"today_checked_in":true}`, http.StatusOK, `{}`,
	))
	_, err := NewTencentCodeBuddyClient(upstream).Checkin(context.Background(), workBuddyIntlTestAccount())
	require.NoError(t, err)
	require.Equal(t, "www.workbuddy.ai", upstream.requests()[0].Host)
}

func TestTencentCodeBuddyCheckinDue(t *testing.T) {
	const today = "2026-10-09"
	active := func(extra map[string]any) *Account {
		account := tencentCodeBuddyTestAccount(nil)
		account.Status = StatusActive
		account.Extra = extra
		return account
	}

	require.True(t, tencentCodeBuddyCheckinDue(active(nil), today))
	require.False(t, tencentCodeBuddyCheckinDue(active(map[string]any{tencentCodeBuddyExtraAutoCheckin: false}), today))
	require.False(t, tencentCodeBuddyCheckinDue(active(map[string]any{
		tencentCodeBuddyExtraCheckinDate: today, tencentCodeBuddyExtraCheckinResult: TencentCodeBuddyCheckinClaimed,
	}), today))
	// 今天失败过的账号、昨天签过的账号都要再试。
	require.True(t, tencentCodeBuddyCheckinDue(active(map[string]any{
		tencentCodeBuddyExtraCheckinDate: today, tencentCodeBuddyExtraCheckinResult: TencentCodeBuddyCheckinFailed,
	}), today))
	require.True(t, tencentCodeBuddyCheckinDue(active(map[string]any{
		tencentCodeBuddyExtraCheckinDate: "2026-10-08", tencentCodeBuddyExtraCheckinResult: TencentCodeBuddyCheckinAlready,
	}), today))

	disabled := active(nil)
	disabled.Status = StatusDisabled
	require.False(t, tencentCodeBuddyCheckinDue(disabled, today))
}

// checkinTestRepo 只实现签到任务用到的两个方法。
type checkinTestRepo struct {
	AccountRepository
	accounts []Account
	updates  map[int64]map[string]any
}

func (r *checkinTestRepo) ListByPlatform(_ context.Context, _ string) ([]Account, error) {
	return r.accounts, nil
}

func (r *checkinTestRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.updates[id] = updates
	return nil
}

type checkinTestClient struct{ calls int }

func (c *checkinTestClient) Checkin(_ context.Context, _ *Account) (TencentCodeBuddyCheckinResult, error) {
	c.calls++
	return TencentCodeBuddyCheckinResult{Result: TencentCodeBuddyCheckinClaimed, Message: "ok", StreakDays: float64(2)}, nil
}

func TestTencentCodeBuddyCheckinService_RunOnceRecordsResult(t *testing.T) {
	due := *tencentCodeBuddyTestAccount(nil)
	due.Status = StatusActive
	optedOut := due
	optedOut.ID = 43
	optedOut.Extra = map[string]any{tencentCodeBuddyExtraAutoCheckin: false}

	repo := &checkinTestRepo{accounts: []Account{due, optedOut}, updates: map[int64]map[string]any{}}
	client := &checkinTestClient{}
	svc := &TencentCodeBuddyCheckinService{
		accountRepo: repo,
		client:      client,
		now:         func() time.Time { return time.Date(2026, 10, 8, 17, 0, 0, 0, time.UTC) },
	}
	svc.runOnce()

	require.Equal(t, 1, client.calls)
	saved := repo.updates[due.ID]
	// UTC 17:00 已是北京时间次日。
	require.Equal(t, "2026-10-09", saved[tencentCodeBuddyExtraCheckinDate])
	require.Equal(t, TencentCodeBuddyCheckinClaimed, saved[tencentCodeBuddyExtraCheckinResult])
	require.Equal(t, float64(2), saved[tencentCodeBuddyExtraCheckinStreak])
	require.NotContains(t, repo.updates, optedOut.ID)
}
