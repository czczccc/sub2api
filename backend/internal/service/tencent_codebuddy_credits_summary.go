package service

import (
	"sort"
	"strings"
	"time"
)

// 分组积分汇总：把分组内 WorkBuddy 账号已落库的剩余积分（extra.codebuddy_credits_*）
// 加总，并给出 DeepSeek Flash 的积分倍率，供后台折算"还能用多少 tokens"。
//
// 只读 extra，不打上游：积分由签到任务 / 余额定时刷新 / 手动刷新写入，汇总看到的是
// 各账号最近一次查询的结果（CheckedAt 可见）。token 折算参数在 WorkBuddyConfig 里，
// 由前端结合 DeepSeek 官方价格计算，后端不做估算。

// TencentCodeBuddyCreditsSummary 是一组 WorkBuddy 账号的积分汇总。
type TencentCodeBuddyCreditsSummary struct {
	// AccountCount 是参与汇总的 CodeBuddy/WorkBuddy 账号数（含不支持查询的国际版）。
	AccountCount int `json:"account_count"`
	// ReportedCount 是已有积分数据的账号数；只有它们计入 Remain / Total / Used。
	ReportedCount int `json:"reported_count"`
	// UnsupportedCount 是不支持积分查询的账号数（国际版）。
	UnsupportedCount int `json:"unsupported_count"`
	// ErrorCount 是最近一次查询失败的账号数（可能仍保留上一次成功的数值）。
	ErrorCount int `json:"error_count"`

	Remain float64 `json:"remain"`
	Total  float64 `json:"total"`
	Used   float64 `json:"used"`
	// EarliestExpireAt 是各账号中最早到期的积分包时间（RFC3339）；都长期有效时为空。
	EarliestExpireAt string `json:"earliest_expire_at,omitempty"`
	// OldestCheckedAt 是参与汇总的数据里最旧的一次查询时间，用来提示数据新鲜度。
	OldestCheckedAt string `json:"oldest_checked_at,omitempty"`

	// DeepSeekFlash 是从上游模型目录快照里读到的 DeepSeek Flash 积分倍率；没读到时为 nil。
	DeepSeekFlash *TencentCodeBuddyModelCreditRate `json:"deepseek_flash,omitempty"`

	Accounts []TencentCodeBuddyAccountCredits `json:"accounts"`
}

// TencentCodeBuddyModelCreditRate 是某个模型的积分倍率及其来源。
type TencentCodeBuddyModelCreditRate struct {
	ModelID    string  `json:"model_id"`
	Multiplier float64 `json:"multiplier"`
	// SyncedAt 是倍率所在快照的同步时间。
	SyncedAt string `json:"synced_at,omitempty"`
}

// TencentCodeBuddyAccountCredits 是汇总中的单个账号明细。
type TencentCodeBuddyAccountCredits struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Supported bool     `json:"supported"`
	Remain    *float64 `json:"remain,omitempty"`
	Total     *float64 `json:"total,omitempty"`
	ExpireAt  string   `json:"expire_at,omitempty"`
	CheckedAt string   `json:"checked_at,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// BuildTencentCodeBuddyCreditsSummary 汇总 accounts 中的 CodeBuddy/WorkBuddy 账号；
// groupID > 0 时只统计属于该分组的账号。
func BuildTencentCodeBuddyCreditsSummary(accounts []Account, groupID int64) TencentCodeBuddyCreditsSummary {
	summary := TencentCodeBuddyCreditsSummary{Accounts: []TencentCodeBuddyAccountCredits{}}
	var earliestExpire, oldestChecked time.Time
	for i := range accounts {
		account := &accounts[i]
		if !account.IsTencentCodeBuddy() || (groupID > 0 && !accountInGroup(account, groupID)) {
			continue
		}
		summary.AccountCount++
		row := TencentCodeBuddyAccountCredits{
			ID:        account.ID,
			Name:      account.Name,
			Status:    account.Status,
			Supported: tencentCodeBuddyCreditsSupported(account),
		}
		if rate := tencentCodeBuddyDeepSeekFlashRate(account); rate != nil {
			if summary.DeepSeekFlash == nil || rate.SyncedAt > summary.DeepSeekFlash.SyncedAt {
				summary.DeepSeekFlash = rate
			}
		}
		if !row.Supported {
			summary.UnsupportedCount++
			summary.Accounts = append(summary.Accounts, row)
			continue
		}
		extra := account.Extra
		row.Error = strings.TrimSpace(stringFromExtra(extra, tencentCodeBuddyExtraCreditsError))
		if row.Error != "" {
			summary.ErrorCount++
		}
		row.CheckedAt = stringFromExtra(extra, tencentCodeBuddyExtraCreditsCheckedAt)
		row.ExpireAt = stringFromExtra(extra, tencentCodeBuddyExtraCreditsExpireAt)
		remain, ok := tencentCodeBuddyCreditsNumber(extra[tencentCodeBuddyExtraCreditsRemain])
		if ok {
			row.Remain = &remain
			summary.ReportedCount++
			summary.Remain += remain
			if total, ok := tencentCodeBuddyCreditsNumber(extra[tencentCodeBuddyExtraCreditsTotal]); ok {
				row.Total = &total
				summary.Total += total
			} else {
				summary.Total += remain
			}
			if used, ok := tencentCodeBuddyCreditsNumber(extra[tencentCodeBuddyExtraCreditsUsed]); ok {
				summary.Used += used
			}
			if t, err := time.Parse(time.RFC3339, row.ExpireAt); err == nil && remain > 0 && (earliestExpire.IsZero() || t.Before(earliestExpire)) {
				earliestExpire = t
			}
			if t, err := time.Parse(time.RFC3339, row.CheckedAt); err == nil && (oldestChecked.IsZero() || t.Before(oldestChecked)) {
				oldestChecked = t
			}
		}
		summary.Accounts = append(summary.Accounts, row)
	}
	if !earliestExpire.IsZero() {
		summary.EarliestExpireAt = earliestExpire.UTC().Format(time.RFC3339)
	}
	if !oldestChecked.IsZero() {
		summary.OldestCheckedAt = oldestChecked.UTC().Format(time.RFC3339)
	}
	// 剩余多的排前面，便于一眼看出积分集中在哪些账号。
	sort.SliceStable(summary.Accounts, func(i, j int) bool {
		return creditsValue(summary.Accounts[i].Remain) > creditsValue(summary.Accounts[j].Remain)
	})
	return summary
}

// tencentCodeBuddyDeepSeekFlashRate 从账号的上游模型快照里找 DeepSeek Flash 的积分倍率。
// 目录里可能同时有 deepseek-v4-flash / deepseek-v4.1-flash 等多个版本，取版本号最大的那个。
func tencentCodeBuddyDeepSeekFlashRate(account *Account) *TencentCodeBuddyModelCreditRate {
	snapshot := account.GetTencentCodeBuddyModelCapabilitySnapshot()
	if snapshot == nil {
		return nil
	}
	best := ""
	for id, capability := range snapshot.Models {
		lower := strings.ToLower(id)
		if capability.CreditMultiplier <= 0 || !strings.Contains(lower, "deepseek") || !strings.Contains(lower, "flash") {
			continue
		}
		if best == "" || lower > strings.ToLower(best) {
			best = id
		}
	}
	if best == "" {
		return nil
	}
	return &TencentCodeBuddyModelCreditRate{
		ModelID:    best,
		Multiplier: snapshot.Models[best].CreditMultiplier,
		SyncedAt:   snapshot.SyncedAt,
	}
}

func accountInGroup(account *Account, groupID int64) bool {
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	for _, group := range account.Groups {
		if group != nil && group.ID == groupID {
			return true
		}
	}
	return false
}

func stringFromExtra(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	if s, ok := extra[key].(string); ok {
		return s
	}
	return ""
}

func creditsValue(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}
