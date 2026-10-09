package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// creditsRoutes 返回按 host+路径分发的 mock 计费上游；未登记的组合返回 404。
func creditsRoutes(routes map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, ok := routes[r.Host+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":404,"msg":"not found"}`)
			return
		}
		_, _ = io.WriteString(w, body)
	}
}

func workBuddyChinaTestAccount(overrides map[string]any) *Account {
	values := map[string]any{tencentCodeBuddyCredProduct: TencentCodeBuddyProductWorkBuddy}
	for key, value := range overrides {
		values[key] = value
	}
	return tencentCodeBuddyTestAccount(values)
}

func TestTencentCodeBuddyQueryCredits_SumsResourcePackages(t *testing.T) {
	soon := time.Now().In(tencentCodeBuddyCheckinLocation).Add(72 * time.Hour).Format("2006-01-02 15:04:05")
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(map[string]string{
		"www.workbuddy.cn" + tencentCodeBuddyCreditsResourcePath: `{"code":0,"data":{"Response":{"Data":{"TotalCount":3,"Accounts":[
			{"PackageName":"免费包","CycleCapacitySizePrecise":"500","CycleCapacityRemainPrecise":"120.5","CycleCapacityUsedPrecise":"379.5","DeductionEndTime":"2049-12-31 23:59:59","CycleEndTime":"` + soon + `"},
			{"PackageName":"签到","CapacitySize":100,"CapacityRemain":100,"DeductionEndTime":"2049-12-31 23:59:59"},
			{"PackageName":"用完","CycleCapacitySize":50,"CycleCapacityRemain":0,"CycleCapacityUsed":50,"CycleEndTime":"2026-10-10 00:00:00"}
		]}}}}`,
	}))

	credits, err := NewTencentCodeBuddyClient(upstream).QueryCredits(context.Background(), workBuddyChinaTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, 220.5, credits.Remain)
	require.Equal(t, 650.0, credits.Total)
	require.Equal(t, 429.5, credits.Used)
	// 免费包的抵扣截止是 2049 占位，改认周期结束；用完的包不参与到期判断。
	require.NotNil(t, credits.ExpireAt)
	require.Equal(t, soon, credits.ExpireAt.In(tencentCodeBuddyCheckinLocation).Format("2006-01-02 15:04:05"))

	reqs := upstream.requests()
	require.Len(t, reqs, 1)
	require.Equal(t, "Bearer at-test", reqs[0].Header.Get("Authorization"))
	require.Equal(t, tencentWorkBuddyDomainChina, reqs[0].Header.Get("X-Domain"))
	require.Equal(t, "https://www.workbuddy.cn", reqs[0].Header.Get("Origin"))
	require.Contains(t, reqs[0].Body, `"ProductCode":"p_tcaca"`)
}

func TestTencentCodeBuddyQueryCredits_FallsBackToSummary(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(map[string]string{
		"www.workbuddy.cn" + tencentCodeBuddyCreditsResourcePath: `{"code":0,"data":{"Response":{"Data":{"TotalCount":0,"Accounts":null}}}}`,
		"www.workbuddy.cn" + tencentCodeBuddyCreditsSummaryPath:  `{"code":0,"data":{"Packages":[{"PackageCode":"a","CycleTotalCapacity":300,"CycleRemainCapacity":"75.25","CycleUsedCapacity":224.75}]}}`,
	}))

	credits, err := NewTencentCodeBuddyClient(upstream).QueryCredits(context.Background(), workBuddyChinaTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, TencentCodeBuddyCredits{Remain: 75.25, Total: 300, Used: 224.75}, credits)
}

func TestTencentCodeBuddyQueryCredits_EnterpriseUsage(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(map[string]string{
		"www.workbuddy.cn" + tencentCodeBuddyCreditsResourcePath:   `{"code":0,"data":{"Response":{"Data":{"TotalCount":0}}}}`,
		"www.workbuddy.cn" + tencentCodeBuddyCreditsSummaryPath:    `{"code":0,"data":{"Packages":[]}}`,
		"www.workbuddy.cn" + tencentCodeBuddyCreditsEnterprisePath: `{"code":0,"data":{"credit":8206.04,"limitNum":20000,"cycleEndTime":"2026-11-01 00:00:00"}}`,
	}))

	credits, err := NewTencentCodeBuddyClient(upstream).QueryCredits(context.Background(), workBuddyChinaTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, TencentCodeBuddyCredits{Remain: 11793.96, Total: 20000, Used: 8206.04}, credits)
}

func TestTencentCodeBuddyQueryCredits_FallsBackToOtherHost(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(map[string]string{
		"www.codebuddy.cn" + tencentCodeBuddyCreditsResourcePath: `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CapacitySize":10,"CapacityRemain":4}]}}}}`,
	}))

	credits, err := NewTencentCodeBuddyClient(upstream).QueryCredits(context.Background(), workBuddyChinaTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, 4.0, credits.Remain)
	require.Equal(t, 6.0, credits.Used)
	require.Equal(t, "www.codebuddy.cn", upstream.requests()[len(upstream.requests())-1].Host)
}

func TestTencentCodeBuddyQueryCredits_ReportsPrimaryHostError(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":11001,"msg":"token expired"}`)
	})

	_, err := NewTencentCodeBuddyClient(upstream).QueryCredits(context.Background(), workBuddyChinaTestAccount(nil))
	require.Error(t, err)
	require.Contains(t, tencentCodeBuddyCreditsErrorText(err), "www.workbuddy.cn HTTP 401")
	require.Contains(t, tencentCodeBuddyCreditsErrorText(err), "token expired")
}

func TestTencentCodeBuddyQueryCredits_RejectsGlobal(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, creditsRoutes(nil))
	_, err := NewTencentCodeBuddyClient(upstream).QueryCredits(context.Background(), workBuddyIntlTestAccount())
	requireTencentCodeBuddyReason(t, err, "TENCENT_CODEBUDDY_CREDITS_UNSUPPORTED")
	require.Empty(t, upstream.requests())
}

type creditsTestClient struct {
	credits TencentCodeBuddyCredits
	err     error
}

func (c creditsTestClient) QueryCredits(_ context.Context, _ *Account) (TencentCodeBuddyCredits, error) {
	return c.credits, c.err
}

func TestRefreshTencentCodeBuddyCredits_KeepsValuesOnFailure(t *testing.T) {
	account := workBuddyChinaTestAccount(nil)
	repo := &checkinTestRepo{updates: map[int64]map[string]any{}}

	updates, err := refreshTencentCodeBuddyCredits(context.Background(), repo, creditsTestClient{
		credits: TencentCodeBuddyCredits{Remain: 1, Total: 2, Used: 1},
	}, account)
	require.NoError(t, err)
	require.Equal(t, 1.0, updates[tencentCodeBuddyExtraCreditsRemain])
	require.Equal(t, "", updates[tencentCodeBuddyExtraCreditsError])

	_, err = refreshTencentCodeBuddyCredits(context.Background(), repo, creditsTestClient{
		err: http.ErrHandlerTimeout,
	}, account)
	require.Error(t, err)
	saved := repo.updates[account.ID]
	require.Equal(t, 1.0, saved[tencentCodeBuddyExtraCreditsRemain])
	require.NotEmpty(t, saved[tencentCodeBuddyExtraCreditsError])
}
