package service

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// CodeBuddy / WorkBuddy 上游错误的响应式处理。错误码与文案取自 workbuddy2api 的
// 实测分类（internal/upstream/client.go Classify / server/handler.go applyErrorPolicy）：
//
//   - 12153 / "Offline user session not found"：登录会话失效，只能重新扫码 → 置错误；
//   - "request illegal"（11140 的授权风控文案）：账号被上游封禁 → 置错误；
//     11140 也承载模型级限流文案，所以只认文案不认码；
//   - 402，或 429 + code 14018：积分耗尽 → 暂停到次日 04:00（北京时间），签到后恢复；
//   - "trial not activated"（14017）：账号注册未完成 → 短暂停调，补完注册可自愈；
//   - 429 + code 6004：单个模型用量超限 → 只限该模型，冷却到文案里的重置时间；
//   - 其它 429 带"将在 … 重置"：整个账号冷却到该时间；
//   - 400 + 11102（该账号无此模型）：只让该账号避开这个模型；
//   - 403 无业务信封：WAF 拦截页，短暂停调；
//   - 403 + 11128 等内容拦截：请求内容的问题，不罚账号；
//   - 401：令牌失效但还有 refresh_token 时暂停并触发后台刷新，而不是直接置错误。
//
// 未识别的错误交回默认逻辑。

const (
	tencentCodeBuddyCreditsExhaustedReason = "codebuddy_credits_exhausted"
	tencentCodeBuddyTrialReason            = "codebuddy_trial_not_activated"
	tencentCodeBuddyWAFReason              = "codebuddy_waf_block"
	tencentCodeBuddyModelLimitReason       = "codebuddy_model_rate_limit"
	tencentCodeBuddyModelBlockedReason     = "codebuddy_model_not_available"
	// tencentCodeBuddyTokenInvalidReason 是 401 暂停的原因前缀，刷新器据此强制刷新。
	tencentCodeBuddyTokenInvalidReason = "codebuddy_token_invalid"

	tencentCodeBuddyTrialCooldown        = 10 * time.Minute
	tencentCodeBuddyWAFCooldown          = time.Minute
	tencentCodeBuddyTokenInvalidCooldown = 10 * time.Minute
	tencentCodeBuddyModelBlockedCooldown = time.Hour
	// tencentCodeBuddyMaxResetCooldown 是从文案解析出的重置时间上限，防脏数据把账号停用过久。
	tencentCodeBuddyMaxResetCooldown = 8 * 24 * time.Hour
	// tencentCodeBuddyCreditsResetHour 是积分耗尽后恢复调度的整点（北京时间），签到在此之后。
	tencentCodeBuddyCreditsResetHour = 4
)

// tencentCodeBuddyResetLoc 是上游重置文案的固定时区（UTC+8，与服务器时区无关）。
var tencentCodeBuddyResetLoc = time.FixedZone("UTC+8", 8*60*60)

var (
	tencentCodeBuddyResetCN = regexp.MustCompile(`将在\s*(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\s*重置`)
	tencentCodeBuddyResetEN = regexp.MustCompile(`(?i)reset at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})`)
)

var tencentCodeBuddySessionDeadMarkers = []string{"offline user session not found"}

var tencentCodeBuddyContentBlockedMarkers = []string{
	"blocked by security policy",
	"unapproved channel",
	"illegal api invocation",
}

var tencentCodeBuddyCreditsMarkers = []string{
	"insufficient credit", "not enough credit", "credits exhausted",
	"积分不足", "额度不足", "余额不足", "积分用完", "额度用尽", "没有积分",
}

// tencentCodeBuddyBusinessCode 读取 {"code":...} 信封的业务码（顶层或 error 子对象）。
func tencentCodeBuddyBusinessCode(body []byte) string {
	for _, path := range []string{"code", "error.code"} {
		if v := gjson.GetBytes(body, path); v.Exists() {
			return strings.TrimSpace(v.String())
		}
	}
	return ""
}

// tencentCodeBuddyHasEnvelope 报告 body 是否带上游业务信封。WAF 拦截页是 HTML、
// 空体或纯文本，没有 code / msg 字段。
func tencentCodeBuddyHasEnvelope(body []byte) bool {
	return gjson.GetBytes(body, "code").Exists() || gjson.GetBytes(body, "msg").Exists() ||
		gjson.GetBytes(body, "error").Exists()
}

func tencentCodeBuddyContainsAny(lower string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// tencentCodeBuddyResetAt 解析"将在 YYYY-MM-DD HH:MM:SS 重置"（UTC+8）。
// 已过去或超过上限的时间视为无效。
func tencentCodeBuddyResetAt(body []byte, now time.Time) *time.Time {
	m := tencentCodeBuddyResetCN.FindSubmatch(body)
	if m == nil {
		m = tencentCodeBuddyResetEN.FindSubmatch(body)
	}
	if m == nil {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", string(m[1]), tencentCodeBuddyResetLoc)
	if err != nil || !t.After(now) || t.Sub(now) > tencentCodeBuddyMaxResetCooldown {
		return nil
	}
	return &t
}

// tencentCodeBuddyNextCreditsReset 返回下一个北京时间 04:00。
func tencentCodeBuddyNextCreditsReset(now time.Time) time.Time {
	local := now.In(tencentCodeBuddyResetLoc)
	next := time.Date(local.Year(), local.Month(), local.Day(), tencentCodeBuddyCreditsResetHour, 0, 0, 0, tencentCodeBuddyResetLoc)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func tencentCodeBuddyReason(prefix, upstreamMsg string) string {
	if msg := strings.TrimSpace(upstreamMsg); msg != "" {
		return prefix + ": " + msg
	}
	return prefix
}

// handleTencentCodeBuddyError 按 CodeBuddy 错误码分类处理。handled=false 时交回默认逻辑。
func (s *RateLimitService) handleTencentCodeBuddyError(
	ctx context.Context,
	account *Account,
	statusCode int,
	responseBody []byte,
	upstreamMsg string,
) (handled, shouldDisable bool) {
	now := time.Now()
	lower := strings.ToLower(string(responseBody))
	code := tencentCodeBuddyBusinessCode(responseBody)

	if code == strconv.Itoa(tencentCodeBuddySessionDeadCode) || tencentCodeBuddyContainsAny(lower, tencentCodeBuddySessionDeadMarkers) {
		s.handleAuthError(ctx, account, tencentCodeBuddyReason("WorkBuddy 登录会话已失效，需要重新扫码登录", upstreamMsg))
		return true, true
	}
	if strings.Contains(lower, "request illegal") {
		s.handleAuthError(ctx, account, tencentCodeBuddyReason("账号被上游拒绝（request illegal），需要重新登录", upstreamMsg))
		return true, true
	}
	if statusCode == http.StatusPaymentRequired ||
		(statusCode == http.StatusTooManyRequests && code == "14018") ||
		(statusCode != http.StatusTooManyRequests && tencentCodeBuddyContainsAny(lower, tencentCodeBuddyCreditsMarkers)) {
		until := tencentCodeBuddyNextCreditsReset(now)
		return true, s.setTencentCodeBuddyTempUnschedulable(ctx, account, until,
			tencentCodeBuddyReason(tencentCodeBuddyCreditsExhaustedReason, upstreamMsg))
	}
	if strings.Contains(lower, "trial not activated") || strings.Contains(lower, "trial version is not yet activated") {
		return true, s.setTencentCodeBuddyTempUnschedulable(ctx, account, now.Add(tencentCodeBuddyTrialCooldown),
			tencentCodeBuddyReason(tencentCodeBuddyTrialReason, upstreamMsg))
	}

	switch statusCode {
	case http.StatusUnauthorized:
		if strings.TrimSpace(account.TencentCodeBuddyCredential().RefreshToken) == "" {
			return false, false
		}
		return true, s.setTencentCodeBuddyTempUnschedulable(ctx, account, now.Add(tencentCodeBuddyTokenInvalidCooldown),
			tencentCodeBuddyReason(tencentCodeBuddyTokenInvalidReason, upstreamMsg))
	case http.StatusTooManyRequests:
		reset := tencentCodeBuddyResetAt(responseBody, now)
		if code == "6004" {
			if modelKey := modelRateLimitKeyForUpstreamModelNotFound(ctx, account, tempUnschedulableModel(ctx, nil)); modelKey != "" {
				until := reset
				if until == nil {
					fallback, ok := s.get429FallbackCooldown(ctx, account)
					if !ok || fallback <= 0 {
						fallback = time.Duration(defaultRateLimit429CooldownSeconds) * time.Second
					}
					next := now.Add(fallback)
					until = &next
				}
				if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, modelKey, *until,
					tencentCodeBuddyReason(tencentCodeBuddyModelLimitReason, upstreamMsg)); err != nil {
					slog.Warn("codebuddy_model_rate_limit_set_failed", "account_id", account.ID, "model", modelKey, "error", err)
					return false, false
				}
				slog.Info("codebuddy_model_rate_limited", "account_id", account.ID, "model", modelKey, "reset_at", until.UTC())
				return true, false
			}
		}
		if reset == nil {
			return false, false
		}
		s.notifyAccountSchedulingBlocked(account, *reset, "429")
		if err := s.accountRepo.SetRateLimited(ctx, account.ID, *reset); err != nil {
			slog.Warn("codebuddy_rate_limit_set_failed", "account_id", account.ID, "error", err)
			return false, false
		}
		slog.Info("codebuddy_account_rate_limited", "account_id", account.ID, "reset_at", reset.UTC())
		return true, false
	case http.StatusBadRequest, http.StatusNotFound:
		if code == "11102" || strings.Contains(lower, "service info not found") {
			modelKey := modelRateLimitKeyForUpstreamModelNotFound(ctx, account, tempUnschedulableModel(ctx, nil))
			if modelKey == "" {
				return false, false
			}
			until := now.Add(tencentCodeBuddyModelBlockedCooldown)
			if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, modelKey, until,
				tencentCodeBuddyReason(tencentCodeBuddyModelBlockedReason, upstreamMsg)); err != nil {
				slog.Warn("codebuddy_model_blocked_set_failed", "account_id", account.ID, "model", modelKey, "error", err)
				return false, false
			}
			return true, false
		}
	case http.StatusForbidden:
		if tencentCodeBuddyContainsAny(lower, tencentCodeBuddyContentBlockedMarkers) || code == "11128" {
			// 内容审核误拦：请求内容的问题，换账号也一样，不罚账号。
			return true, false
		}
		if !tencentCodeBuddyHasEnvelope(responseBody) {
			// 同一出口短时间内多个账号被 WAF 拦截时判定为 IP 级拦截，后续请求直接快速失败。
			tencentCodeBuddyWAFIP.Note(tencentCodeBuddyEgressKey(account), account.ID, now)
			return true, s.setTencentCodeBuddyTempUnschedulable(ctx, account, now.Add(tencentCodeBuddyWAFCooldown),
				tencentCodeBuddyWAFReason)
		}
	}
	return false, false
}

func (s *RateLimitService) setTencentCodeBuddyTempUnschedulable(ctx context.Context, account *Account, until time.Time, reason string) bool {
	s.notifyAccountSchedulingBlocked(account, until, strings.SplitN(reason, ":", 2)[0])
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("codebuddy_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
	}
	slog.Info("codebuddy_account_paused", "account_id", account.ID, "until", until.UTC(), "reason", reason)
	return true
}

// tencentCodeBuddyNeedsForcedRefresh 报告账号是否因 401 暂停、正等待后台刷新令牌。
func tencentCodeBuddyNeedsForcedRefresh(account *Account, now time.Time) bool {
	return account != nil && account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) &&
		strings.HasPrefix(account.TempUnschedulableReason, tencentCodeBuddyTokenInvalidReason)
}
