package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// WorkBuddy / CodeBuddy 每日签到积分。
//
// 上游契约取自 88lin/workbuddy-auto-signin（从 WorkBuddy 桌面端逆向）：
//   - 查询：POST {host}/v2/billing/meter/checkin-activity-status
//     → {active, today_checked_in, streak_days, total_credits, ...}（可能包在 data 里）
//   - 领取：POST {host}/v2/billing/meter/daily-checkin
//     → {credit, ...}；今日已签时返回 null，或 HTTP 400 + code 10001 / msg 含"已签"
//   - 鉴权与推理完全一致：Bearer access_token + X-User-Id / X-Enterprise-Id /
//     X-Tenant-Id / X-Domain，即账号已存的凭据，不需要任何额外的 cookie。
//
// host 取舍：大陆 WorkBuddy 桌面端登录态里的 auth.endpoint 是 copilot.tencent.com
// （X-Domain 才是 www.workbuddy.cn），签到脚本也默认打这个 host，因此大陆站统一走
// copilot.tencent.com；国际站沿用账号所属站点的 host。
const (
	tencentCodeBuddyCheckinStatusPath = "/v2/billing/meter/checkin-activity-status"
	tencentCodeBuddyCheckinClaimPath  = "/v2/billing/meter/daily-checkin"
	// 签到、积分等 billing 接口沿用官方桌面端的单段 UA，避免风控差异。
	tencentCodeBuddyCheckinUserAgent = tencentCodeBuddyBillingUserAgent
	tencentCodeBuddyMaxCheckinBytes  = 64 * 1024
	// 上游"今日已签"的业务码。
	tencentCodeBuddyCheckinAlreadyCode = 10001
)

// 签到结果分类，写入账号 extra.codebuddy_checkin_result。
const (
	TencentCodeBuddyCheckinClaimed  = "claimed"  // 本次领取成功
	TencentCodeBuddyCheckinAlready  = "already"  // 今日已签过
	TencentCodeBuddyCheckinInactive = "inactive" // 签到活动未开启
	TencentCodeBuddyCheckinFailed   = "failed"   // 鉴权失败、上游异常或网络错误
)

// 账号 extra 中的签到键。codebuddy_auto_checkin 为 false 时该账号不参与自动签到；
// 其余键由签到任务写入，供后台展示。
const (
	tencentCodeBuddyExtraAutoCheckin    = "codebuddy_auto_checkin"
	tencentCodeBuddyExtraCheckinAt      = "codebuddy_checkin_at"
	tencentCodeBuddyExtraCheckinDate    = "codebuddy_checkin_date"
	tencentCodeBuddyExtraCheckinResult  = "codebuddy_checkin_result"
	tencentCodeBuddyExtraCheckinMessage = "codebuddy_checkin_message"
	tencentCodeBuddyExtraCheckinStreak  = "codebuddy_checkin_streak_days"
	tencentCodeBuddyExtraCheckinTotal   = "codebuddy_checkin_total_credits"
)

// TencentCodeBuddyCheckinResult 是一次签到的结果。
type TencentCodeBuddyCheckinResult struct {
	Result       string
	Message      string
	Credit       any
	StreakDays   any
	TotalCredits any
}

// tencentCodeBuddyCheckinHost 返回签到接口所在的 host（不含 /v2）。
func tencentCodeBuddyCheckinHost(cred TencentCodeBuddyCredential) string {
	endpoint := cred.Endpoint()
	if endpoint.Region == TencentCodeBuddyRegionChina {
		return tencentCodeBuddyAPIHost
	}
	return strings.TrimRight(endpoint.Host, "/")
}

// Checkin 查询签到状态，未签才领取。接口幂等：已签时只产生一次查询请求。
func (c *TencentCodeBuddyClient) Checkin(ctx context.Context, account *Account) (TencentCodeBuddyCheckinResult, error) {
	if err := c.requireAccount(account); err != nil {
		return TencentCodeBuddyCheckinResult{}, err
	}
	cred := account.TencentCodeBuddyCredential()
	if !cred.HasAccessToken() {
		return TencentCodeBuddyCheckinResult{}, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN",
			"credentials.access_token is required")
	}
	host := tencentCodeBuddyCheckinHost(cred)

	statusCode, status, err := c.checkinCall(ctx, account, cred, host+tencentCodeBuddyCheckinStatusPath)
	if err != nil {
		return TencentCodeBuddyCheckinResult{}, err
	}
	if tencentCodeBuddyTokenExpiredStatus(statusCode) {
		return tencentCodeBuddyCheckinAuthFailure(statusCode), nil
	}
	if statusCode < 200 || statusCode >= 300 {
		return TencentCodeBuddyCheckinResult{Result: TencentCodeBuddyCheckinFailed,
			Message: fmt.Sprintf("签到状态接口返回异常（HTTP %d）", statusCode)}, nil
	}
	if active, ok := tencentCodeBuddyDig(status, "active").(bool); ok && !active {
		msg := "签到活动未开启"
		if name, _ := tencentCodeBuddyDig(status, "activity_name").(string); name != "" {
			msg += "（" + name + "）"
		}
		return TencentCodeBuddyCheckinResult{Result: TencentCodeBuddyCheckinInactive, Message: msg}, nil
	}
	if tencentCodeBuddyTruthy(tencentCodeBuddyDig(status, "today_checked_in")) {
		return tencentCodeBuddyCheckinAlready(status, "今日已签过"), nil
	}

	claimCode, claim, err := c.checkinCall(ctx, account, cred, host+tencentCodeBuddyCheckinClaimPath)
	if err != nil {
		return TencentCodeBuddyCheckinResult{}, err
	}
	// 认证或权限拒绝先于"已签"判断。
	if tencentCodeBuddyTokenExpiredStatus(claimCode) {
		return tencentCodeBuddyCheckinAuthFailure(claimCode), nil
	}
	if tencentCodeBuddyCheckinAlreadyClaimed(claim) {
		return tencentCodeBuddyCheckinAlready(c.refreshCheckinStatus(ctx, account, cred, host, status),
			"今日已签过（服务端判定已领取）"), nil
	}
	if credit := tencentCodeBuddyDig(claim, "credit"); credit != nil {
		fresh := c.refreshCheckinStatus(ctx, account, cred, host, status)
		result := TencentCodeBuddyCheckinResult{
			Result:       TencentCodeBuddyCheckinClaimed,
			Credit:       credit,
			StreakDays:   tencentCodeBuddyDig(fresh, "streak_days"),
			TotalCredits: tencentCodeBuddyDig(fresh, "total_credits"),
		}
		result.Message = "成功领取 " + tencentCodeBuddyFormatNumber(credit) + " 积分" + tencentCodeBuddyCheckinSummary(result)
		return result, nil
	}
	msg := ""
	if m, ok := claim.(map[string]any); ok {
		msg, _ = m["msg"].(string)
	}
	if msg == "" {
		raw, _ := json.Marshal(claim)
		msg = truncateString(string(raw), 200)
	}
	return TencentCodeBuddyCheckinResult{Result: TencentCodeBuddyCheckinFailed,
		Message: fmt.Sprintf("领取失败：%s（HTTP %d）", msg, claimCode)}, nil
}

// checkinCall 发出一次签到相关的 POST，返回 HTTP 状态码与解析后的 JSON（null 解析为 nil）。
func (c *TencentCodeBuddyClient) checkinCall(ctx context.Context, account *Account, cred TencentCodeBuddyCredential, url string) (int, any, error) {
	resp, err := c.do(ctx, account, cred, http.MethodPost, url, nil, false,
		map[string]string{"User-Agent": tencentCodeBuddyCheckinUserAgent})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, tencentCodeBuddyMaxCheckinBytes))
	var parsed any
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			parsed = map[string]any{"raw": truncateString(trimmed, 200)}
		}
	} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 只有 2xx 的空响应（null）才表示"今日已签"；错误响应的空 body 不能被误判。
		parsed = map[string]any{"raw": ""}
	}
	return resp.StatusCode, parsed, nil
}

// refreshCheckinStatus 领取后重新查询一次状态以拿到最新的连签与累计积分；失败时沿用旧状态。
func (c *TencentCodeBuddyClient) refreshCheckinStatus(ctx context.Context, account *Account, cred TencentCodeBuddyCredential, host string, fallback any) any {
	code, body, err := c.checkinCall(ctx, account, cred, host+tencentCodeBuddyCheckinStatusPath)
	if err != nil || code < 200 || code >= 300 {
		return fallback
	}
	if _, ok := body.(map[string]any); !ok {
		return fallback
	}
	return body
}

func tencentCodeBuddyCheckinAuthFailure(code int) TencentCodeBuddyCheckinResult {
	if code == http.StatusUnauthorized {
		return TencentCodeBuddyCheckinResult{Result: TencentCodeBuddyCheckinFailed,
			Message: "服务端拒绝认证（HTTP 401），请检查账号令牌是否有效"}
	}
	return TencentCodeBuddyCheckinResult{Result: TencentCodeBuddyCheckinFailed,
		Message: "服务端拒绝此操作（HTTP 403），请检查账号权限或活动条件"}
}

func tencentCodeBuddyCheckinAlready(status any, prefix string) TencentCodeBuddyCheckinResult {
	result := TencentCodeBuddyCheckinResult{
		Result:       TencentCodeBuddyCheckinAlready,
		StreakDays:   tencentCodeBuddyDig(status, "streak_days"),
		TotalCredits: tencentCodeBuddyDig(status, "total_credits"),
	}
	result.Message = prefix + tencentCodeBuddyCheckinSummary(result)
	return result
}

// tencentCodeBuddyCheckinSummary 生成"（连续 N 天，累计 M 积分）"后缀；字段缺失时省略。
func tencentCodeBuddyCheckinSummary(r TencentCodeBuddyCheckinResult) string {
	var parts []string
	if r.StreakDays != nil {
		parts = append(parts, "连续 "+tencentCodeBuddyFormatNumber(r.StreakDays)+" 天")
	}
	if r.TotalCredits != nil {
		parts = append(parts, "累计 "+tencentCodeBuddyFormatNumber(r.TotalCredits)+" 积分")
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, "，") + "）"
}

// tencentCodeBuddyCheckinAlreadyClaimed 判断领取接口是否表示"今日已签"（null 或 code 10001 / msg 含"已签"）。
func tencentCodeBuddyCheckinAlreadyClaimed(body any) bool {
	if body == nil {
		return true
	}
	m, ok := body.(map[string]any)
	if !ok {
		return false
	}
	if code, ok := m["code"].(float64); ok && int(code) == tencentCodeBuddyCheckinAlreadyCode {
		return true
	}
	msg, _ := m["msg"].(string)
	return strings.Contains(msg, "已签")
}

// tencentCodeBuddyDig 在可能被 data/result 包裹的响应里查找字段（与签到脚本的 dig 一致）。
func tencentCodeBuddyDig(obj any, key string) any {
	m, ok := obj.(map[string]any)
	if !ok {
		return nil
	}
	if v, ok := m[key]; ok && v != nil {
		return v
	}
	for _, wrapper := range []string{"data", "result", "resp", "response"} {
		if inner, ok := m[wrapper].(map[string]any); ok {
			if v := tencentCodeBuddyDig(inner, key); v != nil {
				return v
			}
		}
	}
	return nil
}

func tencentCodeBuddyTruthy(v any) bool {
	switch typed := v.(type) {
	case bool:
		return typed
	case float64:
		return typed == 1
	default:
		return false
	}
}

// tencentCodeBuddyFormatNumber 把 JSON 数字格式化为不带多余小数的字符串。
func tencentCodeBuddyFormatNumber(v any) string {
	if f, ok := v.(float64); ok {
		if f == float64(int64(f)) {
			return fmt.Sprintf("%d", int64(f))
		}
		return fmt.Sprintf("%g", f)
	}
	return fmt.Sprint(v)
}

// ===== 周期任务 =====

// tencentCodeBuddyCheckinLocation 是签到日界所在的时区（腾讯签到按北京时间换日）。
var tencentCodeBuddyCheckinLocation = time.FixedZone("CST", 8*3600)

// tencentCodeBuddyCheckinner 抽象签到调用（*TencentCodeBuddyClient 实现，测试可替换）。
type tencentCodeBuddyCheckinner interface {
	Checkin(ctx context.Context, account *Account) (TencentCodeBuddyCheckinResult, error)
	tencentCodeBuddyCreditsQuerier
}

// TencentCodeBuddyCheckinService 周期性为 CodeBuddy / WorkBuddy 账号领取每日签到积分。
//
// 复用 CNProviderBalanceCheckService 的 Start/Stop/runOnce + ticker 骨架。每轮：
//   - 跳过非 active、缺 access_token、extra.codebuddy_auto_checkin=false 的账号；
//   - 今天（北京时间）已记录 claimed/already 的账号不再请求；
//   - 其余账号先查状态、未签才领，结果写回 extra 供后台展示；
//   - 签到之后为所有 active 的大陆站账号刷新一次剩余积分（不受自动签到开关影响）；
//   - 最后为所有 active 账号刷新一次模型能力快照。
//
// 多实例部署时可能重复查询，但上游接口幂等，不会重复领取。
type TencentCodeBuddyCheckinService struct {
	accountRepo AccountRepository
	client      tencentCodeBuddyCheckinner
	interval    time.Duration
	now         func() time.Time
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

// NewTencentCodeBuddyCheckinService 构造签到服务。interval <= 0 时 Start() 不启动。
func NewTencentCodeBuddyCheckinService(accountRepo AccountRepository, provider *TencentCodeBuddyProvider, interval time.Duration) *TencentCodeBuddyCheckinService {
	svc := &TencentCodeBuddyCheckinService{
		accountRepo: accountRepo,
		interval:    interval,
		now:         time.Now,
		stopCh:      make(chan struct{}),
	}
	if client := provider.Client(); client != nil {
		svc.client = client
	}
	return svc
}

// tencentCodeBuddyCheckinStartDelay 是启动后首次签到前的等待，避开进程启动峰。
const tencentCodeBuddyCheckinStartDelay = 2 * time.Minute

func (s *TencentCodeBuddyCheckinService) Start() {
	if s == nil || s.accountRepo == nil || s.client == nil || s.interval <= 0 {
		return
	}
	log.Printf("[CodeBuddyCheckin] started (interval=%s)", s.interval)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case <-time.After(tencentCodeBuddyCheckinStartDelay):
			s.runOnce()
		case <-s.stopCh:
			return
		}
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *TencentCodeBuddyCheckinService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *TencentCodeBuddyCheckinService) runOnce() {
	accounts, err := s.accountRepo.ListByPlatform(context.Background(), PlatformTencentCodeBuddy)
	if err != nil {
		log.Printf("[CodeBuddyCheckin] list accounts failed: %v", err)
		return
	}
	today := s.now().In(tencentCodeBuddyCheckinLocation).Format("2006-01-02")
	claimed, failed := 0, 0
	for i := range accounts {
		account := &accounts[i]
		if !tencentCodeBuddyCheckinDue(account, today) {
			continue
		}
		switch s.checkinOne(account, today) {
		case TencentCodeBuddyCheckinClaimed:
			claimed++
		case TencentCodeBuddyCheckinFailed:
			failed++
		}
	}
	if claimed > 0 || failed > 0 {
		log.Printf("[CodeBuddyCheckin] claimed=%d failed=%d", claimed, failed)
	}
	s.refreshCredits(accounts)
	s.refreshModelCapabilities(accounts)
}

// refreshModelCapabilities 为 active 账号刷新模型能力快照（上下文/输出上限/识图）；
// 失败只记日志。客户端不支持目录拉取（测试替身）时跳过。
func (s *TencentCodeBuddyCheckinService) refreshModelCapabilities(accounts []Account) {
	fetcher, ok := s.client.(tencentCodeBuddyCatalogFetcher)
	if !ok {
		return
	}
	failed := 0
	for i := range accounts {
		account := &accounts[i]
		if !account.IsActive() || !account.TencentCodeBuddyCredential().HasAccessToken() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		if err := refreshTencentCodeBuddyModelCapabilities(ctx, s.accountRepo, fetcher, account); err != nil {
			failed++
			log.Printf("[CodeBuddyCheckin] refresh model capabilities for account %d: %v", account.ID, err)
		}
		cancel()
	}
	if failed > 0 {
		log.Printf("[CodeBuddyCheckin] model capabilities refresh failed=%d", failed)
	}
}

// refreshCredits 为 active 且支持积分查询的账号刷新剩余积分；失败只记日志，结果写入 extra。
func (s *TencentCodeBuddyCheckinService) refreshCredits(accounts []Account) {
	failed := 0
	for i := range accounts {
		account := &accounts[i]
		if !account.IsActive() || !account.TencentCodeBuddyCredential().HasAccessToken() || !tencentCodeBuddyCreditsSupported(account) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		if _, err := refreshTencentCodeBuddyCredits(ctx, s.accountRepo, s.client, account); err != nil {
			failed++
			log.Printf("[CodeBuddyCheckin] refresh credits for account %d: %v", account.ID, err)
		}
		cancel()
	}
	if failed > 0 {
		log.Printf("[CodeBuddyCheckin] credits refresh failed=%d", failed)
	}
}

// tencentCodeBuddyCheckinDue 判断账号本轮是否需要签到。
func tencentCodeBuddyCheckinDue(account *Account, today string) bool {
	if account == nil || !account.IsActive() || !account.TencentCodeBuddyCredential().HasAccessToken() {
		return false
	}
	if enabled, ok := account.Extra[tencentCodeBuddyExtraAutoCheckin].(bool); ok && !enabled {
		return false
	}
	if date, _ := account.Extra[tencentCodeBuddyExtraCheckinDate].(string); date == today {
		switch account.Extra[tencentCodeBuddyExtraCheckinResult] {
		case TencentCodeBuddyCheckinClaimed, TencentCodeBuddyCheckinAlready:
			return false
		}
	}
	return true
}

// checkinOne 为单个账号签到并把结果写回 extra，返回结果分类。
func (s *TencentCodeBuddyCheckinService) checkinOne(account *Account, today string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := s.client.Checkin(ctx, account)
	if err != nil {
		result = TencentCodeBuddyCheckinResult{Result: TencentCodeBuddyCheckinFailed,
			Message: truncateString("签到请求失败："+err.Error(), 300)}
	}
	updates := map[string]any{
		tencentCodeBuddyExtraCheckinAt:      s.now().UTC().Format(time.RFC3339),
		tencentCodeBuddyExtraCheckinDate:    today,
		tencentCodeBuddyExtraCheckinResult:  result.Result,
		tencentCodeBuddyExtraCheckinMessage: result.Message,
	}
	if result.StreakDays != nil {
		updates[tencentCodeBuddyExtraCheckinStreak] = result.StreakDays
	}
	if result.TotalCredits != nil {
		updates[tencentCodeBuddyExtraCheckinTotal] = result.TotalCredits
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		log.Printf("[CodeBuddyCheckin] save result for account %d failed: %v", account.ID, err)
	}
	if result.Result == TencentCodeBuddyCheckinFailed {
		log.Printf("[CodeBuddyCheckin] account %d: %s", account.ID, result.Message)
	}
	return result.Result
}

// ProvideTencentCodeBuddyCheckinService 构造并启动每日签到任务。
// 由 gateway.codebuddy.auto_checkin_enabled 控制，周期取 auto_checkin_interval_minutes（默认 180）。
func ProvideTencentCodeBuddyCheckinService(
	accountRepo AccountRepository,
	provider *TencentCodeBuddyProvider,
	cfg *config.Config,
) *TencentCodeBuddyCheckinService {
	minutes := 180
	if cfg != nil && cfg.Gateway.CodeBuddy.AutoCheckinIntervalMinutes > 0 {
		minutes = cfg.Gateway.CodeBuddy.AutoCheckinIntervalMinutes
	}
	svc := NewTencentCodeBuddyCheckinService(accountRepo, provider, time.Duration(minutes)*time.Minute)
	if cfg == nil || cfg.Gateway.CodeBuddy.AutoCheckinEnabled {
		svc.Start()
	}
	return svc
}
