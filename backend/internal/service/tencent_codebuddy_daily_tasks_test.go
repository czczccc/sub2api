package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const growthTestHost = "copilot.tencent.com"

func growthTestAccount() *Account {
	return workBuddyChinaTestAccount(map[string]any{tencentCodeBuddyCredEnterpriseID: ""})
}

func TestTencentCodeBuddyGrowthSupported(t *testing.T) {
	require.True(t, tencentCodeBuddyGrowthSupported(growthTestAccount()))
	require.False(t, tencentCodeBuddyGrowthSupported(workBuddyChinaTestAccount(nil)), "企业账号没有成长体系")
	require.False(t, tencentCodeBuddyGrowthSupported(workBuddyChinaTestAccount(map[string]any{
		tencentCodeBuddyCredEnterpriseID: "",
		tencentCodeBuddyCredRegion:       TencentCodeBuddyRegionGlobal,
	})))
}

func TestTencentCodeBuddyGrowthCall_BusinessError(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(map[string]string{
		growthTestHost + "/activity/growth/streak": `{"code":40001,"msg":"nope"}`,
	}))
	_, err := NewTencentCodeBuddyClient(upstream).GrowthStreak(context.Background(), growthTestAccount())
	var growthErr *TencentCodeBuddyGrowthError
	require.ErrorAs(t, err, &growthErr)
	require.Equal(t, int64(40001), growthErr.Code)
}

func TestTencentCodeBuddyDailyTasks_TravelClaimsOnArrival(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(map[string]string{
		growthTestHost + "/activity/growth/buddy/info":          `{"code":0,"data":{"buddy":{"id":1}}}`,
		growthTestHost + "/activity/growth/buddy/travel/status": `{"code":0,"data":{"state":"arrived","record_id":9}}`,
		growthTestHost + "/activity/growth/buddy/travel/claim":  `{"code":0,"data":{"reward_credit":30}}`,
	}))
	svc := NewTencentCodeBuddyDailyTaskService(nil, NewTencentCodeBuddyClient(upstream), nil)
	message, err := svc.runTravel(context.Background(), growthTestAccount())
	require.NoError(t, err)
	require.Equal(t, "旅行归来，领取 30 积分", message)
	reqs := upstream.requests()
	require.Contains(t, reqs[len(reqs)-1].Body, `"record_id":9`)
}

func TestTencentCodeBuddyDailyTasks_TravelAdoptOnlyAfterChat(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(map[string]string{
		growthTestHost + "/activity/growth/buddy/info": `{"code":0,"data":{"buddy":null}}`,
	}))
	svc := NewTencentCodeBuddyDailyTaskService(nil, NewTencentCodeBuddyClient(upstream), nil)
	message, err := svc.runTravel(context.Background(), growthTestAccount())
	require.NoError(t, err)
	require.Contains(t, message, "还没有猫")
	require.Len(t, upstream.requests(), 1, "当天没有对话时不尝试领养")
}

func TestTencentCodeBuddyDailyTasks_StreakRedeemsAndDraws(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(map[string]string{
		growthTestHost + "/activity/growth/heatmap": `{"code":0,"data":{"cells":[]}}`,
		growthTestHost + "/activity/growth/streak": `{"code":0,"data":{"streak":{"days":8},"redemption_status":{
			"tier_7d_status":"available","tier_14d_status":"locked","tier_28d_status":"locked",
			"tiers":[{"tier":"7d","credit":50},{"tier":"14d","credit":100}]}}}`,
		growthTestHost + "/activity/growth/redeem":          `{"code":0,"data":{}}`,
		growthTestHost + "/activity/growth/lottery/summary": `{"code":0,"data":{"chances":2}}`,
		growthTestHost + "/activity/growth/lottery/draw":    `{"code":0,"data":{}}`,
	}))
	svc := NewTencentCodeBuddyDailyTaskService(nil, NewTencentCodeBuddyClient(upstream), nil)
	message, err := svc.runStreak(context.Background(), growthTestAccount())
	require.NoError(t, err)
	require.Equal(t, "连登 8 天，兑换 7d 档 +50 积分，抽奖 2 次", message)
	redeems := 0
	for _, req := range upstream.requests() {
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL, "/activity/growth/redeem") {
			redeems++
		}
	}
	require.Equal(t, 1, redeems, "锁定档位不兑换")
}

func TestTencentCodeBuddyDailyTasks_UsedToday(t *testing.T) {
	svc := NewTencentCodeBuddyDailyTaskService(nil, nil, nil)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, tencentCodeBuddyResetLoc)
	svc.now = func() time.Time { return now }
	account := growthTestAccount()
	require.False(t, svc.usedToday(account))
	yesterday := now.Add(-2 * time.Hour)
	account.LastUsedAt = &yesterday
	require.False(t, svc.usedToday(account))
	today := now.Add(-30 * time.Minute)
	account.LastUsedAt = &today
	require.True(t, svc.usedToday(account))
}

func TestNormalizeWorkBuddyTaskSchedules(t *testing.T) {
	cfg := normalizeWorkBuddyConfig(WorkBuddyConfig{StreakTask: WorkBuddyTaskSchedule{Hours: []int{22, 7, 7, 30, -1}}})
	require.Equal(t, []int{10}, cfg.TaskSchedule(WorkBuddyTaskActivity).Hours)
	require.Equal(t, []int{7, 22}, cfg.TaskSchedule(WorkBuddyTaskStreak).Hours)
	require.Equal(t, workBuddyDefaultBalanceRefreshMinutes, cfg.BalanceRefreshMinutes)
}

func TestTencentCodeBuddyDailyTasks_RunRejectsUnknownTask(t *testing.T) {
	svc := NewTencentCodeBuddyDailyTaskService(struct{ AccountRepository }{}, NewTencentCodeBuddyClient(nil), nil)
	require.Error(t, svc.RunInBackground("bogus"))
}

func TestActivateWorkBuddyGlobal_SubmitsRegionThenClaimsTrial(t *testing.T) {
	registered := false
	upstream := newTencentCodeBuddyTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/realms/copilot/overseas/user/register":
			require.Equal(t, "uid-1", r.URL.Query().Get("userId"))
			if registered {
				_, _ = w.Write([]byte(`{"code":200,"msg":"register success"}`))
			} else {
				_, _ = w.Write([]byte(`{"code":500,"msg":"region required"}`))
			}
		case "/billing/area/get-country-code":
			_, _ = w.Write([]byte(`{"code":0,"data":"{\"data\":{\"list\":[{\"EnName\":\"Japan\",\"IOS2\":\"JP\",\"Code\":\"81\"},{\"EnName\":\"Singapore\",\"IOS2\":\"SG\",\"Code\":\"65\"}]}}"}`))
		case "/console/login/account":
			registered = true
			_, _ = w.Write([]byte(`{"code":0}`))
		case "/billing/ide/trial":
			_, _ = w.Write([]byte(`{"code":14051,"msg":"already"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	account := tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductWorkBuddy,
		tencentCodeBuddyCredRegion:  TencentCodeBuddyRegionGlobal,
	})
	message, err := NewTencentCodeBuddyClient(upstream).ActivateWorkBuddyGlobal(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "已补注册地区 SG 并激活", message)
	var submitted string
	for _, req := range upstream.requests() {
		require.True(t, strings.HasPrefix(req.URL, "https://www.workbuddy.ai/"))
		if strings.HasSuffix(req.URL, "/console/login/account") {
			submitted = req.Body
		}
	}
	require.Contains(t, submitted, `"countryName":["SG"]`)
}
