package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// WorkBuddy 成长中心（growth）与计费域杂项接口，移植自参考实现 workbuddy2api-panel
// internal/upstream 的 travel.go / streak.go / blackcat.go / report.go / profile.go / tasks.go。
//
// 只用于大陆站个人账号：企业账号没有成长体系（上游一律 403），国际站将下线。
// 三个域名：
//   - growth 域：copilot.tencent.com（不带 /v2 前缀），连登、抽奖、猫猫旅行、成长任务；
//   - billing 域：www.codebuddy.cn，活跃上报 /v2/report、新手礼包、补偿；
//   - web 域：www.workbuddy.cn，账号资料（昵称）。
//
// 响应统一是 {code, msg, data} 信封，code != 0 视为业务错误。

const (
	tencentCodeBuddyGrowthHost  = tencentCodeBuddyAPIHost
	tencentCodeBuddyBillingHost = "https://www.codebuddy.cn"
	tencentCodeBuddyWebHost     = "https://www.workbuddy.cn"

	tencentCodeBuddyGrowthMaxBody = 1 << 20
)

// TencentCodeBuddyGrowthError 是 growth / billing 接口的 HTTP 或业务错误。
type TencentCodeBuddyGrowthError struct {
	Status int
	Code   int64
	Msg    string
}

func (e *TencentCodeBuddyGrowthError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("HTTP %d code=%d %s", e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("HTTP %d %s", e.Status, e.Msg)
}

// tencentCodeBuddyGrowthSupported 判断账号能否使用成长中心：大陆站个人账号。
func tencentCodeBuddyGrowthSupported(account *Account) bool {
	if account == nil || account.Platform != PlatformTencentCodeBuddy {
		return false
	}
	cred := account.TencentCodeBuddyCredential()
	return cred.HasAccessToken() && cred.EnterpriseID == "" && cred.Endpoint().Region == TencentCodeBuddyRegionChina
}

// growthCall 发出一次 growth / billing / web 请求并解开信封，返回 data。
func (c *TencentCodeBuddyClient) growthCall(ctx context.Context, account *Account, method, url string, payload any, extra map[string]string) (json.RawMessage, error) {
	if err := c.requireAccount(account); err != nil {
		return nil, err
	}
	cred := account.TencentCodeBuddyCredential()
	if !cred.HasAccessToken() {
		return nil, infraerrors.New(http.StatusBadRequest, "TENCENT_CODEBUDDY_MISSING_ACCESS_TOKEN",
			"credentials.access_token is required")
	}
	var body []byte
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = raw
	}
	headers := map[string]string{"User-Agent": tencentCodeBuddyBillingUserAgent}
	for key, value := range extra {
		headers[key] = value
	}
	resp, err := c.do(ctx, account, cred, method, url, body, false, headers)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, tencentCodeBuddyGrowthMaxBody))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	var envelope struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	parseErr := json.Unmarshal(raw, &envelope)
	if resp.StatusCode >= 400 {
		msg := envelope.Msg
		if parseErr != nil || msg == "" {
			msg = truncateString(strings.TrimSpace(string(raw)), 200)
		}
		code, _ := envelope.Code.Int64()
		return nil, &TencentCodeBuddyGrowthError{Status: resp.StatusCode, Code: code, Msg: msg}
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parse response: %w (body: %s)", parseErr, truncateString(string(raw), 120))
	}
	if code, _ := envelope.Code.Int64(); code != 0 {
		return nil, &TencentCodeBuddyGrowthError{Status: resp.StatusCode, Code: code, Msg: truncateString(envelope.Msg, 200)}
	}
	return envelope.Data, nil
}

func (c *TencentCodeBuddyClient) growthJSON(ctx context.Context, account *Account, method, path string, payload any) (json.RawMessage, error) {
	return c.growthCall(ctx, account, method, tencentCodeBuddyGrowthHost+path, payload, nil)
}

func (c *TencentCodeBuddyClient) billingJSON(ctx context.Context, account *Account, method, path string, payload any) (json.RawMessage, error) {
	return c.growthCall(ctx, account, method, tencentCodeBuddyBillingHost+path, payload, nil)
}

// tencentCodeBuddyClientToken 生成前端同款的幂等令牌（UUID 形态）。
func tencentCodeBuddyClientToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

// ===== 活跃上报 =====

// ReportChatActivity 上报一条 chat_request_send 事件（官方客户端每次发起对话时上报的同款事件）。
// 只在账号确实发生过对话时调用：事件内容必须对应一次真实对话。
func (c *TencentCodeBuddyClient) ReportChatActivity(ctx context.Context, account *Account, conversationID, modelID string) error {
	uid := account.TencentCodeBuddyCredential().UserID
	now := time.Now().UnixMilli()
	event := map[string]any{
		"eventCode":             "chat_request_send",
		"timestamp":             now,
		"reportDelay":           0,
		"mode":                  "craft",
		"conversationId":        conversationID,
		"requestId":             conversationID,
		"inputLength":           12,
		"requestModelId":        modelID,
		"requestModelName":      modelID,
		"isPlan":                false,
		"isAutoExecuteTerminal": false,
		"isAutoModify":          false,
		"codebaseEnable":        false,
		"maxToken":              0,
		"maxSteps":              0,
		"temperature":           0,
		"maxRetries":            0,
		"mentionContexts":       []any{},
		"knowledgeId":           []any{},
		"knowledgeName":         []any{},
		"codebaseId":            "",
		"mentionContextCount":   0,
		"command":               "",
		"expertId":              "",
		"recommendId":           "",
		"skillId":               "",
		"skillCount":            0,
		"totalCount":            0,
		"fileUri":               "",
		"presentAt":             now,
		"traceId":               "",
		"rootRequestId":         conversationID,
		"parentConversationId":  conversationID,
		"agentName":             "default",
		"agentType":             "conversation",
		"userId":                uid,
	}
	_, err := c.billingJSON(ctx, account, http.MethodPost, "/v2/report", []any{event})
	return err
}

// RunShortChat 发一次真实的极短对话并读完响应，返回会话 ID。用于需要真实对话的任务。
func (c *TencentCodeBuddyClient) RunShortChat(ctx context.Context, account *Account, model string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":          model,
		"messages":       []map[string]any{{"role": "user", "content": "1+1等于几？直接回答。"}},
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	})
	resp, err := c.ChatCompletion(ctx, account, body, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", &TencentCodeBuddyGrowthError{Status: resp.StatusCode, Msg: truncateString(string(raw), 200)}
	}
	if !bytes.Contains(raw, []byte("data:")) {
		return "", fmt.Errorf("empty chat response")
	}
	return fmt.Sprintf("sub2api-%d", time.Now().UnixMilli()), nil
}

// ===== 连登兑换 / 抽奖 / 补签 / 礼包 =====

// TencentCodeBuddyStreak 是连登状态（GET /activity/growth/streak）。
type TencentCodeBuddyStreak struct {
	Streak struct {
		Days int `json:"days"`
	} `json:"streak"`
	MakeupCards struct {
		Balance int `json:"balance"`
	} `json:"makeup_cards"`
	RedemptionStatus struct {
		Tier7dStatus  string `json:"tier_7d_status"`
		Tier14dStatus string `json:"tier_14d_status"`
		Tier28dStatus string `json:"tier_28d_status"`
		Tiers         []struct {
			Tier    string `json:"tier"`
			Credit  int    `json:"credit"`
			Chances int    `json:"chances"`
		} `json:"tiers"`
	} `json:"redemption_status"`
}

func (s *TencentCodeBuddyStreak) tierStatus(tier string) string {
	switch tier {
	case "7d":
		return s.RedemptionStatus.Tier7dStatus
	case "14d":
		return s.RedemptionStatus.Tier14dStatus
	case "28d":
		return s.RedemptionStatus.Tier28dStatus
	}
	return ""
}

func (c *TencentCodeBuddyClient) GrowthStreak(ctx context.Context, account *Account) (*TencentCodeBuddyStreak, error) {
	data, err := c.growthJSON(ctx, account, http.MethodGet, "/activity/growth/streak", nil)
	if err != nil {
		return nil, err
	}
	out := &TencentCodeBuddyStreak{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GrowthRedeemTier 兑换连登档位（7d / 14d / 28d）；未解锁时上游返回 403。
func (c *TencentCodeBuddyClient) GrowthRedeemTier(ctx context.Context, account *Account, tier string) error {
	_, err := c.growthJSON(ctx, account, http.MethodPost, "/activity/growth/redeem",
		map[string]any{"tier": tier, "client_token": tencentCodeBuddyClientToken()})
	return err
}

func (c *TencentCodeBuddyClient) LotteryChances(ctx context.Context, account *Account) (int, error) {
	data, err := c.growthJSON(ctx, account, http.MethodGet, "/activity/growth/lottery/summary", nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Chances int `json:"chances"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, err
	}
	return resp.Chances, nil
}

func (c *TencentCodeBuddyClient) LotteryDraw(ctx context.Context, account *Account) (json.RawMessage, error) {
	return c.growthJSON(ctx, account, http.MethodPost, "/activity/growth/lottery/draw",
		map[string]any{"client_token": tencentCodeBuddyClientToken()})
}

// HeatmapMissed 判断某天（YYYY-MM-DD）是否漏签。
func (c *TencentCodeBuddyClient) HeatmapMissed(ctx context.Context, account *Account, day string) (bool, error) {
	data, err := c.growthJSON(ctx, account, http.MethodGet, "/activity/growth/heatmap", nil)
	if err != nil {
		return false, err
	}
	var resp struct {
		Cells []struct {
			Date  string `json:"date"`
			Score int    `json:"score"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return false, err
	}
	for _, cell := range resp.Cells {
		if len(cell.Date) >= 10 && cell.Date[:10] == day {
			return cell.Score == 0, nil
		}
	}
	return false, nil
}

func (c *TencentCodeBuddyClient) UseMakeupCard(ctx context.Context, account *Account, day string) error {
	_, err := c.growthJSON(ctx, account, http.MethodPost, "/activity/growth/makeup-cards/use", map[string]any{"target_date": day})
	return err
}

// ClaimGift 领取新手礼包；没有可领的礼包时返回业务错误。
func (c *TencentCodeBuddyClient) ClaimGift(ctx context.Context, account *Account) (int64, error) {
	return c.claimCredit(ctx, account, "/billing/meter/claim-gift")
}

// ClaimCompensation 领取活动补偿；没有时返回业务错误。
func (c *TencentCodeBuddyClient) ClaimCompensation(ctx context.Context, account *Account) (int64, error) {
	return c.claimCredit(ctx, account, "/billing/meter/claim-compensation")
}

func (c *TencentCodeBuddyClient) claimCredit(ctx context.Context, account *Account, path string) (int64, error) {
	data, err := c.billingJSON(ctx, account, http.MethodPost, path, map[string]any{})
	if err != nil {
		return 0, err
	}
	var resp struct {
		Credit int64 `json:"credit"`
	}
	_ = json.Unmarshal(data, &resp)
	return resp.Credit, nil
}

// ===== 猫猫旅行 =====

// TencentCodeBuddyTravelState 是猫猫旅行状态。
type TencentCodeBuddyTravelState struct {
	State             string `json:"state"` // idle / traveling / arrived
	DailyLimitReached bool   `json:"daily_limit_reached"`
	RecordID          int64  `json:"record_id"`
	RewardCredit      int64  `json:"reward_credit"`
}

func (c *TencentCodeBuddyClient) TravelStatus(ctx context.Context, account *Account) (*TencentCodeBuddyTravelState, error) {
	data, err := c.growthJSON(ctx, account, http.MethodGet, "/activity/growth/buddy/travel/status", nil)
	if err != nil {
		return nil, err
	}
	out := &TencentCodeBuddyTravelState{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *TencentCodeBuddyClient) TravelDepart(ctx context.Context, account *Account, locationID int) error {
	_, err := c.growthJSON(ctx, account, http.MethodPost, "/activity/growth/buddy/travel/depart", map[string]any{"location_id": locationID})
	return err
}

func (c *TencentCodeBuddyClient) TravelClaim(ctx context.Context, account *Account, recordID int64) (int64, error) {
	data, err := c.growthJSON(ctx, account, http.MethodPost, "/activity/growth/buddy/travel/claim", map[string]any{"record_id": recordID})
	if err != nil {
		return 0, err
	}
	var resp struct {
		RewardCredit int64 `json:"reward_credit"`
	}
	_ = json.Unmarshal(data, &resp)
	return resp.RewardCredit, nil
}

// HasBuddy 报告账号是否已经领养了猫。
func (c *TencentCodeBuddyClient) HasBuddy(ctx context.Context, account *Account) (bool, error) {
	data, err := c.growthJSON(ctx, account, http.MethodGet, "/activity/growth/buddy/info", nil)
	if err != nil {
		return false, err
	}
	var resp struct {
		Buddy json.RawMessage `json:"buddy"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return false, err
	}
	trimmed := strings.TrimSpace(string(resp.Buddy))
	return trimmed != "" && trimmed != "null" && trimmed != "{}", nil
}

// AdoptBuddy 同意协议并领养第一只猫。领养门槛是当天有过对话。
func (c *TencentCodeBuddyClient) AdoptBuddy(ctx context.Context, account *Account) error {
	if _, err := c.growthJSON(ctx, account, http.MethodPost, "/activity/growth/buddy/agreement", map[string]any{"agree": true}); err != nil {
		return err
	}
	_, err := c.growthJSON(ctx, account, http.MethodPost, "/activity/growth/buddy/first", map[string]any{})
	return err
}

// ===== 昵称 =====

// FetchNickname 读取 Web 控制台账号资料里的昵称。响应里的手机号等其它字段一律不解析。
func (c *TencentCodeBuddyClient) FetchNickname(ctx context.Context, account *Account) (string, error) {
	data, err := c.growthCall(ctx, account, http.MethodGet, tencentCodeBuddyWebHost+"/console/account", nil, map[string]string{
		"x-client-platform": "web",
		"Accept":            "application/json, text/plain, */*",
		"Origin":            tencentCodeBuddyWebHost,
		"Referer":           tencentCodeBuddyWebHost + "/profile/account-settings",
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		UID      string `json:"uid"`
		Nickname string `json:"nickname"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	if uid := account.TencentCodeBuddyCredential().UserID; resp.UID != "" && uid != "" && resp.UID != uid {
		return "", fmt.Errorf("profile uid mismatch")
	}
	return strings.TrimSpace(resp.Nickname), nil
}
