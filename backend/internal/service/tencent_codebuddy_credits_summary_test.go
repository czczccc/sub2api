package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestParseTencentCodeBuddyCreditMultiplier(t *testing.T) {
	cases := map[string]float64{
		`{"credits":0.79}`:            0.79,
		`{"credits":"x0.79 credits"}`: 0.79,
		`{"credits":"x0.03"}`:         0.03,
		`{"credits":"1.5x"}`:          1.5,
		`{"credits":{"base":1}}`:      0,
		`{"creditMultiplier":"0.2"}`:  0.2,
		`{"credits":"free"}`:          0,
		`{}`:                          0,
	}
	for body, want := range cases {
		require.InDelta(t, want, parseTencentCodeBuddyCreditMultiplier(gjson.Parse(body)), 1e-9, body)
	}
}

func TestParseTencentCodeBuddyModelCapabilities_KeepsCreditOnlyModels(t *testing.T) {
	caps := parseTencentCodeBuddyModelCapabilities([]byte(`{"code":0,"data":{"models":[{"id":"deepseek-v4.1-flash","credits":"x0.2"},{"id":"hy3"}]}}`))
	require.InDelta(t, 0.2, caps["deepseek-v4.1-flash"].CreditMultiplier, 1e-9)
	_, hasHY3 := caps["hy3"]
	require.False(t, hasHY3)
}

func TestBuildTencentCodeBuddyCreditsSummary(t *testing.T) {
	withCredits := func(id int64, groups []int64, extra map[string]any) Account {
		account := *tencentCodeBuddyTestAccount(nil)
		account.ID = id
		account.GroupIDs = groups
		account.Extra = extra
		return account
	}
	a := withCredits(1, []int64{7}, map[string]any{
		tencentCodeBuddyExtraCreditsRemain:    100.0,
		tencentCodeBuddyExtraCreditsTotal:     500.0,
		tencentCodeBuddyExtraCreditsUsed:      400.0,
		tencentCodeBuddyExtraCreditsExpireAt:  "2026-11-01T00:00:00Z",
		tencentCodeBuddyExtraCreditsCheckedAt: "2026-10-18T01:00:00Z",
		TencentCodeBuddyModelCapabilitiesExtraKey: map[string]any{
			"synced_at": "2026-10-18T00:00:00Z",
			"models": map[string]any{
				"deepseek-v4-flash":   map[string]any{"credit_multiplier": 0.5},
				"deepseek-v4.1-flash": map[string]any{"credit_multiplier": 0.25},
				"glm-5.3":             map[string]any{"credit_multiplier": 0.79},
			},
		},
	})
	b := withCredits(2, []int64{7}, map[string]any{
		tencentCodeBuddyExtraCreditsRemain:    "50.5",
		tencentCodeBuddyExtraCreditsExpireAt:  "2026-10-25T00:00:00Z",
		tencentCodeBuddyExtraCreditsCheckedAt: "2026-10-17T01:00:00Z",
		tencentCodeBuddyExtraCreditsError:     "timeout",
	})
	noData := withCredits(3, []int64{7}, nil)
	otherGroup := withCredits(4, []int64{8}, map[string]any{tencentCodeBuddyExtraCreditsRemain: 999.0})
	intl := *tencentCodeBuddyTestAccount(map[string]any{tencentCodeBuddyCredRegion: "global"})
	intl.ID = 5
	intl.GroupIDs = []int64{7}
	notCodeBuddy := Account{ID: 6, Platform: PlatformOpenAI, GroupIDs: []int64{7}}

	summary := BuildTencentCodeBuddyCreditsSummary([]Account{a, b, noData, otherGroup, intl, notCodeBuddy}, 7)
	require.Equal(t, 4, summary.AccountCount)
	require.Equal(t, 2, summary.ReportedCount)
	require.Equal(t, 1, summary.ErrorCount)
	require.Equal(t, 1, summary.UnsupportedCount)
	require.InDelta(t, 150.5, summary.Remain, 1e-9)
	require.InDelta(t, 550.5, summary.Total, 1e-9, "没有 total 的账号按剩余计入")
	require.InDelta(t, 400, summary.Used, 1e-9)
	require.Equal(t, "2026-10-25T00:00:00Z", summary.EarliestExpireAt)
	require.Equal(t, "2026-10-17T01:00:00Z", summary.OldestCheckedAt)
	require.NotNil(t, summary.DeepSeekFlash)
	require.Equal(t, "deepseek-v4.1-flash", summary.DeepSeekFlash.ModelID)
	require.InDelta(t, 0.25, summary.DeepSeekFlash.Multiplier, 1e-9)
	require.Equal(t, int64(1), summary.Accounts[0].ID, "按剩余积分降序")

	all := BuildTencentCodeBuddyCreditsSummary([]Account{a, otherGroup}, 0)
	require.Equal(t, 2, all.AccountCount)
	require.InDelta(t, 1099, all.Remain, 1e-9)
}

func TestNormalizeWorkBuddyConfig_CreditDefaults(t *testing.T) {
	cfg := normalizeWorkBuddyConfig(WorkBuddyConfig{DeepSeekFlashMultiplier: -1})
	require.Equal(t, workBuddyDefaultCreditTokensPerCredit, cfg.CreditTokensPerCredit)
	require.Zero(t, cfg.DeepSeekFlashMultiplier)
	require.NotNil(t, cfg.CreditOutputPercent)
	require.Equal(t, 20, *cfg.CreditOutputPercent)

	zero, over := 0, 150
	require.Equal(t, 0, *normalizeWorkBuddyConfig(WorkBuddyConfig{CreditOutputPercent: &zero}).CreditOutputPercent)
	require.Equal(t, 100, *normalizeWorkBuddyConfig(WorkBuddyConfig{CreditOutputPercent: &over}).CreditOutputPercent)
}
