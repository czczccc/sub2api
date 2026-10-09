package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// WorkBuddy / CodeBuddy 大陆站剩余积分。
//
// 上游契约取自社区工具（workbuddy-helper / workbuddy2api-hub / workbuddy-switch，均从
// WorkBuddy 桌面端与用户中心逆向），鉴权与签到一致（Bearer + X-User-Id / X-Domain …）：
//   - 积分包明细：POST {host}/v2/billing/meter/get-user-resource
//     body {PageNumber, PageSize, ProductCode:"p_tcaca", Status:[0,3], PackageEndTimeRange*}
//     → data.Response.Data.Accounts[]，每个包带剩余/已用/总量与到期时间；
//   - 积分概览：POST {host}/billing/meter/get-user-resource-summary（不带 /v2），body {}
//     → data.Packages[]，只有容量没有到期时间，明细接口不可用时兜底；
//   - 企业账号：上面两个接口返回空包，改查 POST {host}/billing/meter/get-enterprise-user-usage
//     → data.{credit(本周期已用), limitNum(周期额度), cycleEndTime}。
//
// host 取舍：大陆计费接口挂在用户中心站点（www.codebuddy.cn / www.workbuddy.cn），
// 而不是推理用的 copilot.tencent.com。令牌签发域与 X-Domain 不一致时网关会拒绝，
// 所以先打与账号 X-Domain 一致的站点，失败再换另一个。国际版暂不支持。
const (
	tencentCodeBuddyCreditsResourcePath   = "/v2/billing/meter/get-user-resource"
	tencentCodeBuddyCreditsSummaryPath    = "/billing/meter/get-user-resource-summary"
	tencentCodeBuddyCreditsEnterprisePath = "/billing/meter/get-enterprise-user-usage"
	tencentCodeBuddyCreditsHostCodeBuddy  = "https://www.codebuddy.cn"
	tencentCodeBuddyCreditsProductCode    = "p_tcaca"
	tencentCodeBuddyCreditsPageSize       = 100
	tencentCodeBuddyMaxCreditsBytes       = 512 * 1024
	// 到期时间距今超过该值视为长期有效（上游用 2049 之类的占位值）。
	tencentCodeBuddyCreditsFarFuture = 730 * 24 * time.Hour
	// DeductionEndTime 比 CycleEndTime 晚这么多时，前者是长期占位，改认周期结束。
	tencentCodeBuddyCreditsCycleOverride = 365 * 24 * time.Hour
)

// 账号 extra 中的积分键，由手动刷新与签到任务写入，供后台展示。
const (
	tencentCodeBuddyExtraCreditsRemain    = "codebuddy_credits_remain"
	tencentCodeBuddyExtraCreditsTotal     = "codebuddy_credits_total"
	tencentCodeBuddyExtraCreditsUsed      = "codebuddy_credits_used"
	tencentCodeBuddyExtraCreditsExpireAt  = "codebuddy_credits_expire_at"
	tencentCodeBuddyExtraCreditsCheckedAt = "codebuddy_credits_checked_at"
	tencentCodeBuddyExtraCreditsError     = "codebuddy_credits_error"
)

// TencentCodeBuddyCredits 是一次积分查询的结果。
type TencentCodeBuddyCredits struct {
	Remain float64
	Total  float64
	Used   float64
	// ExpireAt 是仍有剩余的积分包中最早的到期时间；全部长期有效或未知时为 nil。
	ExpireAt *time.Time
}

// tencentCodeBuddyCreditsSupported 报告账号是否支持积分查询（仅大陆站）。
func tencentCodeBuddyCreditsSupported(account *Account) bool {
	return account != nil && account.Platform == PlatformTencentCodeBuddy &&
		account.TencentCodeBuddyCredential().Endpoint().Region == TencentCodeBuddyRegionChina
}

// tencentCodeBuddyCreditsHosts 返回按优先级排列的计费 host：与 X-Domain 一致的站点在前。
func tencentCodeBuddyCreditsHosts(cred TencentCodeBuddyCredential) []string {
	domain := strings.ToLower(strings.TrimSpace(cred.Endpoint().Domain))
	if strings.HasSuffix(domain, "workbuddy.cn") {
		return []string{tencentWorkBuddyAPIHostChina, tencentCodeBuddyCreditsHostCodeBuddy}
	}
	return []string{tencentCodeBuddyCreditsHostCodeBuddy, tencentWorkBuddyAPIHostChina}
}

// QueryCredits 查询账号剩余积分。host 级失败（HTTP 错误或业务码非 0）会换下一个站点，
// 全部失败时返回首选站点的错误。
func (c *TencentCodeBuddyClient) QueryCredits(ctx context.Context, account *Account) (TencentCodeBuddyCredits, error) {
	if err := c.requireAccount(account); err != nil {
		return TencentCodeBuddyCredits{}, err
	}
	cred := account.TencentCodeBuddyCredential()
	if !cred.HasAccessToken() {
		return TencentCodeBuddyCredits{}, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN",
			"credentials.access_token is required")
	}
	if cred.Endpoint().Region != TencentCodeBuddyRegionChina {
		return TencentCodeBuddyCredits{}, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_CREDITS_UNSUPPORTED",
			"暂不支持国际版账号的积分查询")
	}
	var firstErr error
	for _, host := range tencentCodeBuddyCreditsHosts(cred) {
		credits, err := c.queryCreditsAt(ctx, account, cred, host)
		if err == nil {
			return credits, nil
		}
		if ctx.Err() != nil {
			return TencentCodeBuddyCredits{}, err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return TencentCodeBuddyCredits{}, firstErr
}

// queryCreditsAt 在一个站点上依次尝试明细 → 概览 → 企业额度。只要有一个接口正常
// 应答就算查询成功（即使没有任何积分包，结果为 0）。
func (c *TencentCodeBuddyClient) queryCreditsAt(ctx context.Context, account *Account, cred TencentCodeBuddyCredential, host string) (TencentCodeBuddyCredits, error) {
	now := time.Now().In(tencentCodeBuddyCheckinLocation)
	answered := false
	var firstErr error
	note := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	resource, err := c.creditsCall(ctx, account, cred, host, tencentCodeBuddyCreditsResourcePath, map[string]any{
		"PageNumber":               1,
		"PageSize":                 tencentCodeBuddyCreditsPageSize,
		"ProductCode":              tencentCodeBuddyCreditsProductCode,
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format("2006-01-02 15:04:05"),
		"PackageEndTimeRangeEnd":   now.AddDate(100, 0, 0).Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		note(err)
	} else {
		answered = true
		if packages := tencentCodeBuddyCreditsList(resource, "Accounts"); len(packages) > 0 {
			return tencentCodeBuddySumCreditPackages(packages, now), nil
		}
	}

	summary, err := c.creditsCall(ctx, account, cred, host, tencentCodeBuddyCreditsSummaryPath, map[string]any{})
	if err != nil {
		note(err)
	} else {
		answered = true
		if packages := tencentCodeBuddyCreditsList(summary, "Packages"); len(packages) > 0 {
			return tencentCodeBuddySumCreditPackages(packages, now), nil
		}
	}

	if cred.EnterpriseID != "" {
		usage, err := c.creditsCall(ctx, account, cred, host, tencentCodeBuddyCreditsEnterprisePath, map[string]any{})
		if err != nil {
			note(err)
		} else {
			answered = true
			if credits, ok := tencentCodeBuddyEnterpriseCredits(usage); ok {
				return credits, nil
			}
		}
	}

	if answered {
		return TencentCodeBuddyCredits{}, nil
	}
	return TencentCodeBuddyCredits{}, firstErr
}

// creditsCall 发出一次计费 POST。非 2xx 或业务码非 0 都作为错误返回，便于换站点重试。
func (c *TencentCodeBuddyClient) creditsCall(ctx context.Context, account *Account, cred TencentCodeBuddyCredential, host, path string, payload map[string]any) (any, error) {
	body, _ := json.Marshal(payload)
	headers := map[string]string{
		"User-Agent":          tencentCodeBuddyCheckinUserAgent,
		"Accept":              "application/json, text/plain, */*",
		"Origin":              host,
		"Referer":             host + "/profile/plans-usage",
		"X-Client-Platform":   "web",
		"X-Requested-With":    "XMLHttpRequest",
		"X-CodeBuddy-Request": "1",
		"X-Product":           "SaaS",
	}
	if cred.EnterpriseID == "" {
		headers["X-No-Enterprise-Id"] = "1"
	}
	resp, err := c.do(ctx, account, cred, http.MethodPost, host+path, body, false, headers)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, tencentCodeBuddyMaxCreditsBytes))

	var parsed any
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil, infraerrors.Newf(http.StatusBadGateway, "TENCENT_CODEBUDDY_CREDITS_BAD_RESPONSE",
				"积分接口返回了无法解析的内容（%s）", truncateString(trimmed, 120))
		}
	}
	msg := tencentCodeBuddyCreditsMessage(parsed)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "TENCENT_CODEBUDDY_CREDITS_UPSTREAM_ERROR",
			"积分接口返回异常（%s HTTP %d）：%s", host, resp.StatusCode, msg)
	}
	if m, ok := parsed.(map[string]any); ok {
		if code, ok := tencentCodeBuddyCreditsNumber(m["code"]); ok && code != 0 {
			return nil, infraerrors.Newf(http.StatusBadGateway, "TENCENT_CODEBUDDY_CREDITS_UPSTREAM_ERROR",
				"积分接口返回错误（%s code %d）：%s", host, int64(code), msg)
		}
	}
	return parsed, nil
}

// tencentCodeBuddyCreditsDig 在 data / Response / Data 等包裹层里查找字段。
// 计费接口沿用云 API 的大写包裹（data.Response.Data），签到用的 tencentCodeBuddyDig 不认。
func tencentCodeBuddyCreditsDig(obj any, key string) any {
	m, ok := obj.(map[string]any)
	if !ok {
		return nil
	}
	if v, ok := m[key]; ok && v != nil {
		return v
	}
	for _, wrapper := range []string{"data", "Data", "Response", "response", "result"} {
		if v := tencentCodeBuddyCreditsDig(m[wrapper], key); v != nil {
			return v
		}
	}
	return nil
}

// tencentCodeBuddyCreditsMessage 取出上游错误信息（msg / message，可能在包裹层里）。
func tencentCodeBuddyCreditsMessage(body any) string {
	for _, key := range []string{"msg", "message", "Message"} {
		if s, ok := tencentCodeBuddyCreditsDig(body, key).(string); ok && s != "" {
			return truncateString(s, 200)
		}
	}
	return ""
}

// tencentCodeBuddyCreditsList 在 data / data.Response.Data 等包裹层里查找积分包数组。
func tencentCodeBuddyCreditsList(body any, key string) []map[string]any {
	items, _ := tencentCodeBuddyCreditsDig(body, key).([]any)
	if items == nil {
		items, _ = tencentCodeBuddyCreditsDig(body, strings.ToLower(key[:1])+key[1:]).([]any)
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// tencentCodeBuddySumCreditPackages 汇总积分包。各字段优先取周期精确值，缺失时由另两项推算。
func tencentCodeBuddySumCreditPackages(packages []map[string]any, now time.Time) TencentCodeBuddyCredits {
	var result TencentCodeBuddyCredits
	for _, pkg := range packages {
		total, hasTotal := tencentCodeBuddyCreditsFirst(pkg, "CycleCapacitySizePrecise", "CycleCapacitySize",
			"CycleTotalCapacity", "CapacitySizePrecise", "CapacitySize")
		remain, hasRemain := tencentCodeBuddyCreditsFirst(pkg, "CycleCapacityRemainPrecise", "CycleCapacityRemain",
			"CycleRemainCapacity", "CapacityRemainPrecise", "CapacityRemain")
		used, hasUsed := tencentCodeBuddyCreditsFirst(pkg, "CycleCapacityUsedPrecise", "CycleCapacityUsed",
			"CycleUsedCapacity", "CapacityUsedPrecise", "CapacityUsed")
		if !hasTotal && hasRemain && hasUsed {
			total = remain + used
		}
		if !hasRemain {
			remain = math.Max(0, total-used)
		}
		if !hasUsed {
			used = math.Max(0, total-remain)
		}
		result.Total += math.Max(0, total)
		result.Remain += math.Max(0, remain)
		result.Used += math.Max(0, used)

		if remain <= 0 {
			continue
		}
		if expireAt := tencentCodeBuddyCreditPackageExpiry(pkg, now); expireAt != nil {
			if result.ExpireAt == nil || expireAt.Before(*result.ExpireAt) {
				result.ExpireAt = expireAt
			}
		}
	}
	result.Total = tencentCodeBuddyRoundCredits(result.Total)
	result.Remain = tencentCodeBuddyRoundCredits(result.Remain)
	result.Used = tencentCodeBuddyRoundCredits(result.Used)
	return result
}

// tencentCodeBuddyCreditPackageExpiry 解析积分包的到期时间：优先抵扣截止时间，
// 它是长期占位（比周期结束晚一年以上）时改认周期结束；已过期或长期有效返回 nil。
func tencentCodeBuddyCreditPackageExpiry(pkg map[string]any, now time.Time) *time.Time {
	deduction := tencentCodeBuddyCreditsTime(pkg, "DeductionEndTime", "ExpiredTime", "PackageEndTime")
	cycle := tencentCodeBuddyCreditsTime(pkg, "CycleEndTime")
	expireAt := deduction
	if deduction == nil || (cycle != nil && deduction.Sub(*cycle) > tencentCodeBuddyCreditsCycleOverride) {
		expireAt = cycle
	}
	if expireAt == nil || !expireAt.After(now) || expireAt.Sub(now) > tencentCodeBuddyCreditsFarFuture {
		return nil
	}
	return expireAt
}

// tencentCodeBuddyEnterpriseCredits 解析企业额度：剩余 = 周期额度 - 本周期已用。
// 企业额度按周期回满而不是到期作废，因此不给到期时间。
func tencentCodeBuddyEnterpriseCredits(body any) (TencentCodeBuddyCredits, bool) {
	limit, ok := tencentCodeBuddyCreditsNumber(tencentCodeBuddyCreditsDig(body, "limitNum"))
	if !ok || limit <= 0 {
		return TencentCodeBuddyCredits{}, false
	}
	used, _ := tencentCodeBuddyCreditsNumber(tencentCodeBuddyCreditsDig(body, "credit"))
	used = math.Max(0, used)
	return TencentCodeBuddyCredits{
		Total:  tencentCodeBuddyRoundCredits(limit),
		Used:   tencentCodeBuddyRoundCredits(used),
		Remain: tencentCodeBuddyRoundCredits(math.Max(0, limit-used)),
	}, true
}

func tencentCodeBuddyCreditsFirst(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if v, ok := tencentCodeBuddyCreditsNumber(m[key]); ok {
			return v, true
		}
	}
	return 0, false
}

// tencentCodeBuddyCreditsNumber 兼容数字与数字字符串（精确值字段常以字符串返回）。
func tencentCodeBuddyCreditsNumber(v any) (float64, bool) {
	switch typed := v.(type) {
	case float64:
		return typed, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// tencentCodeBuddyCreditsTime 解析北京时间的 "2006-01-02 15:04:05"、RFC3339 或毫秒/秒时间戳。
func tencentCodeBuddyCreditsTime(m map[string]any, keys ...string) *time.Time {
	for _, key := range keys {
		switch typed := m[key].(type) {
		case string:
			s := strings.TrimSpace(typed)
			if s == "" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return &t
			}
			s = strings.Replace(s, "T", " ", 1)
			if len(s) > 19 {
				s = s[:19]
			}
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, tencentCodeBuddyCheckinLocation); err == nil {
				return &t
			}
		case float64:
			if typed <= 0 {
				continue
			}
			sec := typed
			if sec > 1e11 {
				sec /= 1000
			}
			t := time.Unix(int64(sec), 0)
			return &t
		}
	}
	return nil
}

func tencentCodeBuddyRoundCredits(v float64) float64 {
	return math.Round(v*100) / 100
}

// tencentCodeBuddyCreditsExtra 把查询结果转换为 extra 更新；失败时只记录错误与时间，
// 保留上一次成功的数值。
func tencentCodeBuddyCreditsExtra(credits TencentCodeBuddyCredits, queryErr error, now time.Time) map[string]any {
	updates := map[string]any{
		tencentCodeBuddyExtraCreditsCheckedAt: now.UTC().Format(time.RFC3339),
	}
	if queryErr != nil {
		updates[tencentCodeBuddyExtraCreditsError] = truncateString(tencentCodeBuddyCreditsErrorText(queryErr), 300)
		return updates
	}
	updates[tencentCodeBuddyExtraCreditsError] = ""
	updates[tencentCodeBuddyExtraCreditsRemain] = credits.Remain
	updates[tencentCodeBuddyExtraCreditsTotal] = credits.Total
	updates[tencentCodeBuddyExtraCreditsUsed] = credits.Used
	updates[tencentCodeBuddyExtraCreditsExpireAt] = ""
	if credits.ExpireAt != nil {
		updates[tencentCodeBuddyExtraCreditsExpireAt] = credits.ExpireAt.UTC().Format(time.RFC3339)
	}
	return updates
}

func tencentCodeBuddyCreditsErrorText(err error) string {
	if msg := infraerrors.Message(err); msg != "" {
		return msg
	}
	return err.Error()
}

// RefreshTencentCodeBuddyCredits 查询账号积分并写回 extra，返回写入的字段。
// 查询失败时同样写回错误信息，并返回该错误。
func RefreshTencentCodeBuddyCredits(ctx context.Context, repo AccountRepository, client *TencentCodeBuddyClient, account *Account) (map[string]any, error) {
	// 避免把 nil 指针装进接口后通过 nil 判断。
	var querier tencentCodeBuddyCreditsQuerier
	if client != nil {
		querier = client
	}
	return refreshTencentCodeBuddyCredits(ctx, repo, querier, account)
}

// tencentCodeBuddyCreditsQuerier 抽象积分查询（*TencentCodeBuddyClient 实现，测试可替换）。
type tencentCodeBuddyCreditsQuerier interface {
	QueryCredits(ctx context.Context, account *Account) (TencentCodeBuddyCredits, error)
}

func refreshTencentCodeBuddyCredits(ctx context.Context, repo AccountRepository, client tencentCodeBuddyCreditsQuerier, account *Account) (map[string]any, error) {
	if repo == nil || client == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED",
			"tencent codebuddy provider is not configured")
	}
	if account == nil || account.Platform != PlatformTencentCodeBuddy {
		return nil, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_CREDITS_UNSUPPORTED",
			"只有 WorkBuddy / CodeBuddy 账号支持积分查询")
	}
	credits, queryErr := client.QueryCredits(ctx, account)
	updates := tencentCodeBuddyCreditsExtra(credits, queryErr, time.Now())
	if err := repo.UpdateExtra(ctx, account.ID, updates); err != nil {
		return nil, fmt.Errorf("save credits for account %d: %w", account.ID, err)
	}
	return updates, queryErr
}
