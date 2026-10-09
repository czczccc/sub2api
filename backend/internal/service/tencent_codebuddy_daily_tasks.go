package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// TencentCodeBuddyDailyTaskService 执行 WorkBuddy 日常保号任务（北京时间整点）：
//   - activity：活跃上报，点亮成长中心连登。当天经网关真实对话过的账号直接上报；没有对话的账号
//     先真的发一次极短对话再上报，上报内容始终对应一次真实对话；
//   - streak：补签保连登、领新手礼包/补偿、兑换已解锁的连登档位、抽完抽奖次数；
//   - travel：猫猫旅行，到站领奖、空闲派出；还没有猫时领养（门槛是当天有过对话）；
//   - nickname：同步官网昵称到账号 extra，供后台展示；
//   - balance：每隔几分钟刷新剩余积分，积分恢复后解除"积分耗尽"暂停。
//
// 每个任务在后台「设置 → WorkBuddy」里可单独关闭、调整时点，也可手动立即执行。
// 每个账号每个任务的最近结果写入 extra（codebuddy_task_<name>），供后台展示。
type TencentCodeBuddyDailyTaskService struct {
	accountRepo    AccountRepository
	client         *TencentCodeBuddyClient
	settingService *SettingService
	now            func() time.Time

	mu          sync.Mutex
	lastSlot    map[string]string // task → 已执行的 "日期 小时"
	running     map[string]bool
	lastBalance time.Time

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// tencentCodeBuddyTaskAccountDelay 是账号之间的间隔，避免短时间集中请求上游。
var tencentCodeBuddyTaskAccountDelay = 800 * time.Millisecond

const (
	tencentCodeBuddyTaskExtraPrefix   = "codebuddy_task_"
	tencentCodeBuddyExtraNickname     = "codebuddy_nickname"
	tencentCodeBuddyTravelLocationID  = 4
	tencentCodeBuddyActivityChatModel = "deepseek-v4-flash"
)

// TencentCodeBuddyTaskResult 是单个账号一次任务的结果，写入 extra。
type TencentCodeBuddyTaskResult struct {
	At      string `json:"at"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func NewTencentCodeBuddyDailyTaskService(accountRepo AccountRepository, client *TencentCodeBuddyClient, settingService *SettingService) *TencentCodeBuddyDailyTaskService {
	return &TencentCodeBuddyDailyTaskService{
		accountRepo:    accountRepo,
		client:         client,
		settingService: settingService,
		now:            time.Now,
		lastSlot:       map[string]string{},
		running:        map[string]bool{},
		stopCh:         make(chan struct{}),
	}
}

func (s *TencentCodeBuddyDailyTaskService) Start() {
	if s == nil || s.accountRepo == nil || s.client == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.activateGlobalAccounts()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.tick()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *TencentCodeBuddyDailyTaskService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *TencentCodeBuddyDailyTaskService) config() WorkBuddyConfig {
	if s.settingService == nil {
		return normalizeWorkBuddyConfig(WorkBuddyConfig{})
	}
	return s.settingService.GetWorkBuddyConfig(context.Background())
}

// tick 每分钟检查一次：到点的任务各跑一次（同一整点只跑一次），余额按间隔刷新。
func (s *TencentCodeBuddyDailyTaskService) tick() {
	cfg := s.config()
	now := s.now().In(tencentCodeBuddyResetLoc)
	slot := now.Format("2006-01-02 15")
	for _, task := range []string{WorkBuddyTaskStreak, WorkBuddyTaskTravel, WorkBuddyTaskActivity, WorkBuddyTaskNickname} {
		schedule := cfg.TaskSchedule(task)
		if schedule.Disabled || !containsHour(schedule.Hours, now.Hour()) {
			continue
		}
		s.mu.Lock()
		due := s.lastSlot[task] != slot
		if due {
			s.lastSlot[task] = slot
		}
		s.mu.Unlock()
		if due {
			go func(task string) { _, _ = s.Run(context.Background(), task) }(task)
		}
	}
	s.mu.Lock()
	globalDue := s.lastSlot["global_activate"] != slot
	s.lastSlot["global_activate"] = slot
	s.mu.Unlock()
	if globalDue {
		go s.activateGlobalAccounts()
	}
	if !cfg.BalanceRefreshDisabled {
		s.mu.Lock()
		due := now.Sub(s.lastBalance) >= time.Duration(cfg.BalanceRefreshMinutes)*time.Minute
		if due {
			s.lastBalance = now
		}
		s.mu.Unlock()
		if due {
			go func() { _, _ = s.Run(context.Background(), WorkBuddyTaskBalance) }()
		}
	}
}

func containsHour(hours []int, hour int) bool {
	for _, h := range hours {
		if h == hour {
			return true
		}
	}
	return false
}

// TencentCodeBuddyTaskRunSummary 是一次任务执行的汇总。
type TencentCodeBuddyTaskRunSummary struct {
	Task      string `json:"task"`
	Accounts  int    `json:"accounts"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

var errTencentCodeBuddyTaskRunning = infraerrors.New(http.StatusConflict, "CODEBUDDY_TASK_RUNNING", "该任务正在执行")

// Run 立即对全部适用账号执行一次任务（同一任务不并发执行）。
func (s *TencentCodeBuddyDailyTaskService) Run(ctx context.Context, task string) (TencentCodeBuddyTaskRunSummary, error) {
	summary := TencentCodeBuddyTaskRunSummary{Task: task}
	if s == nil || s.accountRepo == nil || s.client == nil {
		return summary, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED", "codebuddy tasks are not configured")
	}
	runner := s.runner(task)
	if runner == nil {
		return summary, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_TASK_UNKNOWN", "未知任务："+task)
	}
	s.mu.Lock()
	if s.running[task] {
		s.mu.Unlock()
		return summary, errTencentCodeBuddyTaskRunning
	}
	s.running[task] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, task)
		s.mu.Unlock()
	}()

	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformTencentCodeBuddy)
	if err != nil {
		return summary, err
	}
	first := true
	for i := range accounts {
		account := &accounts[i]
		if !account.IsActive() || !tencentCodeBuddyGrowthSupported(account) {
			// 余额刷新对所有支持积分查询的账号都做（含企业账号）。
			if task != WorkBuddyTaskBalance || !account.IsActive() || !tencentCodeBuddyCreditsSupported(account) ||
				!account.TencentCodeBuddyCredential().HasAccessToken() {
				continue
			}
		}
		if !first && task != WorkBuddyTaskBalance {
			select {
			case <-time.After(tencentCodeBuddyTaskAccountDelay):
			case <-ctx.Done():
				return summary, ctx.Err()
			}
		}
		first = false
		summary.Accounts++
		accountCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		message, runErr := runner(accountCtx, account)
		cancel()
		result := TencentCodeBuddyTaskResult{At: s.now().UTC().Format(time.RFC3339), OK: runErr == nil, Message: message}
		if runErr != nil {
			summary.Failed++
			result.Message = truncateString(strings.TrimSpace(strings.TrimPrefix(message+"；"+runErr.Error(), "；")), 300)
			log.Printf("[CodeBuddyTasks] %s account %d: %s", task, account.ID, result.Message)
		} else {
			summary.Succeeded++
		}
		if task != WorkBuddyTaskBalance {
			if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{tencentCodeBuddyTaskExtraPrefix + task: result}); err != nil {
				log.Printf("[CodeBuddyTasks] save %s result for account %d: %v", task, account.ID, err)
			}
		}
	}
	return summary, nil
}

// RunInBackground 校验任务后在后台执行一次，供管理端“立即执行”使用：
// 账号多时逐个间隔执行会超过 HTTP 超时，结果按账号写入 extra 后在账号列表查看。
func (s *TencentCodeBuddyDailyTaskService) RunInBackground(task string) error {
	if s == nil || s.accountRepo == nil || s.client == nil {
		return infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED", "codebuddy tasks are not configured")
	}
	if s.runner(task) == nil {
		return infraerrors.New(http.StatusBadRequest, "CODEBUDDY_TASK_UNKNOWN", "未知任务："+task)
	}
	s.mu.Lock()
	running := s.running[task]
	s.mu.Unlock()
	if running {
		return errTencentCodeBuddyTaskRunning
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		summary, err := s.Run(ctx, task)
		if err != nil {
			log.Printf("[CodeBuddyTasks] manual %s: %v", task, err)
			return
		}
		log.Printf("[CodeBuddyTasks] manual %s done: accounts=%d ok=%d failed=%d", task, summary.Accounts, summary.Succeeded, summary.Failed)
	}()
	return nil
}

// tencentCodeBuddyExtraGlobalActivated 标记 WorkBuddy 国际版账号已完成注册激活。
const tencentCodeBuddyExtraGlobalActivated = "codebuddy_global_activated"

// activateGlobalAccounts 为尚未激活的 WorkBuddy 国际版账号补做注册激活（启动时与每小时一次）。
// 老账号或登录时激活失败的账号由这里兜底，否则对话会一直报 14017 trial not activated。
func (s *TencentCodeBuddyDailyTaskService) activateGlobalAccounts() {
	if s == nil || s.accountRepo == nil || s.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformTencentCodeBuddy)
	if err != nil {
		return
	}
	for i := range accounts {
		account := &accounts[i]
		cred := account.TencentCodeBuddyCredential()
		if !account.IsActive() || !cred.HasAccessToken() || !isTencentWorkBuddyGlobal(cred) {
			continue
		}
		if done, _ := account.Extra[tencentCodeBuddyExtraGlobalActivated].(bool); done {
			continue
		}
		accountCtx, cancelAccount := context.WithTimeout(ctx, time.Minute)
		message, err := s.client.ActivateWorkBuddyGlobal(accountCtx, account)
		cancelAccount()
		updates := map[string]any{}
		if err != nil {
			message = truncateString(err.Error(), 300)
			log.Printf("[CodeBuddyTasks] global activate account %d: %s", account.ID, message)
		} else {
			updates[tencentCodeBuddyExtraGlobalActivated] = true
		}
		updates[tencentCodeBuddyTaskExtraPrefix+"global_activate"] = TencentCodeBuddyTaskResult{
			At: s.now().UTC().Format(time.RFC3339), OK: err == nil, Message: message,
		}
		if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
			log.Printf("[CodeBuddyTasks] save global activation for account %d: %v", account.ID, err)
		}
	}
}

type tencentCodeBuddyTaskRunner func(ctx context.Context, account *Account) (string, error)

func (s *TencentCodeBuddyDailyTaskService) runner(task string) tencentCodeBuddyTaskRunner {
	switch task {
	case WorkBuddyTaskActivity:
		return s.runActivity
	case WorkBuddyTaskStreak:
		return s.runStreak
	case WorkBuddyTaskTravel:
		return s.runTravel
	case WorkBuddyTaskNickname:
		return s.runNickname
	case WorkBuddyTaskBalance:
		return s.runBalance
	}
	return nil
}

// usedToday 报告账号今天（北京时间）是否经网关服务过请求。
func (s *TencentCodeBuddyDailyTaskService) usedToday(account *Account) bool {
	if account.LastUsedAt == nil {
		return false
	}
	return account.LastUsedAt.In(tencentCodeBuddyResetLoc).Format("2006-01-02") == s.now().In(tencentCodeBuddyResetLoc).Format("2006-01-02")
}

// runActivity 活跃上报：上报始终对应一次真实对话。
func (s *TencentCodeBuddyDailyTaskService) runActivity(ctx context.Context, account *Account) (string, error) {
	conversationID := fmt.Sprintf("sub2api-%d", s.now().UnixMilli())
	prefix := "今天已有对话，已上报"
	if !s.usedToday(account) {
		id, err := s.client.RunShortChat(ctx, account, tencentCodeBuddyActivityChatModel)
		if err != nil {
			return "今天还没有对话，补一次短对话失败", err
		}
		conversationID = id
		prefix = "今天还没有对话，已发一次短对话并上报"
	}
	if err := s.client.ReportChatActivity(ctx, account, conversationID, tencentCodeBuddyActivityChatModel); err != nil {
		return "活跃上报失败", err
	}
	if streak, err := s.client.GrowthStreak(ctx, account); err == nil {
		return fmt.Sprintf("%s，连登 %d 天", prefix, streak.Streak.Days), nil
	}
	return prefix, nil
}

// runStreak 连登管家：补签 → 礼包/补偿 → 兑换已解锁档位 → 抽完抽奖次数。
func (s *TencentCodeBuddyDailyTaskService) runStreak(ctx context.Context, account *Account) (string, error) {
	var parts []string
	yesterday := s.now().In(tencentCodeBuddyResetLoc).AddDate(0, 0, -1).Format("2006-01-02")
	if missed, err := s.client.HeatmapMissed(ctx, account, yesterday); err == nil && missed {
		if streak, err := s.client.GrowthStreak(ctx, account); err == nil && streak.MakeupCards.Balance > 0 {
			if err := s.client.UseMakeupCard(ctx, account, yesterday); err == nil {
				parts = append(parts, "已用补签卡补签 "+yesterday)
			}
		}
	}
	if credit, err := s.client.ClaimGift(ctx, account); err == nil {
		parts = append(parts, fmt.Sprintf("新手礼包 +%d", credit))
	}
	if credit, err := s.client.ClaimCompensation(ctx, account); err == nil {
		parts = append(parts, fmt.Sprintf("补偿 +%d", credit))
	}
	streak, err := s.client.GrowthStreak(ctx, account)
	if err != nil {
		return strings.Join(parts, "，"), err
	}
	for _, tier := range streak.RedemptionStatus.Tiers {
		status := streak.tierStatus(tier.Tier)
		if status == "locked" || status == "claimed" {
			continue
		}
		if err := s.client.GrowthRedeemTier(ctx, account, tier.Tier); err != nil {
			continue // 未解锁时上游 403，属预期
		}
		parts = append(parts, fmt.Sprintf("兑换 %s 档 +%d 积分", tier.Tier, tier.Credit))
	}
	chances, err := s.client.LotteryChances(ctx, account)
	if err == nil {
		drawn := 0
		for i := 0; i < chances; i++ {
			if _, err := s.client.LotteryDraw(ctx, account); err != nil {
				break
			}
			drawn++
		}
		if drawn > 0 {
			parts = append(parts, fmt.Sprintf("抽奖 %d 次", drawn))
		}
	}
	parts = append([]string{fmt.Sprintf("连登 %d 天", streak.Streak.Days)}, parts...)
	return strings.Join(parts, "，"), nil
}

// runTravel 猫猫旅行：无猫先领养；到站领奖；空闲且今天还没出发就派出。
func (s *TencentCodeBuddyDailyTaskService) runTravel(ctx context.Context, account *Account) (string, error) {
	hasBuddy, err := s.client.HasBuddy(ctx, account)
	if err != nil {
		return "", err
	}
	if !hasBuddy {
		if !s.usedToday(account) {
			return "还没有猫，领养需要当天先有对话，等活跃任务之后再试", nil
		}
		if err := s.client.AdoptBuddy(ctx, account); err != nil {
			var growthErr *TencentCodeBuddyGrowthError
			if errors.As(err, &growthErr) && strings.Contains(strings.ToLower(growthErr.Msg), "first_buddy task not completed") {
				return "还没有猫，领养门槛未达成，明天再试", nil
			}
			return "领养失败", err
		}
		return "已领养第一只猫", nil
	}
	state, err := s.client.TravelStatus(ctx, account)
	if err != nil {
		return "", err
	}
	switch state.State {
	case "arrived":
		if state.RecordID == 0 {
			return "已到站但没有记录 ID，跳过", nil
		}
		reward, err := s.client.TravelClaim(ctx, account, state.RecordID)
		if err != nil {
			return "领奖失败", err
		}
		return fmt.Sprintf("旅行归来，领取 %d 积分", reward), nil
	case "idle":
		if state.DailyLimitReached {
			return "今天已经旅行过", nil
		}
		if err := s.client.TravelDepart(ctx, account, tencentCodeBuddyTravelLocationID); err != nil {
			return "派出失败", err
		}
		return "已派出旅行", nil
	case "traveling":
		return "旅行中", nil
	}
	return "未知状态 " + state.State, nil
}

func (s *TencentCodeBuddyDailyTaskService) runNickname(ctx context.Context, account *Account) (string, error) {
	nickname, err := s.client.FetchNickname(ctx, account)
	if err != nil {
		return "", err
	}
	if nickname == "" {
		return "官网未设置昵称", nil
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{tencentCodeBuddyExtraNickname: nickname}); err != nil {
		return "", err
	}
	return "昵称：" + nickname, nil
}

// runBalance 刷新剩余积分；积分恢复后解除"积分耗尽"暂停。
func (s *TencentCodeBuddyDailyTaskService) runBalance(ctx context.Context, account *Account) (string, error) {
	updates, err := refreshTencentCodeBuddyCredits(ctx, s.accountRepo, s.client, account)
	if err != nil {
		return "", err
	}
	remain, _ := tencentCodeBuddyCreditsNumber(updates[tencentCodeBuddyExtraCreditsRemain])
	if remain > 0 && strings.HasPrefix(account.TempUnschedulableReason, tencentCodeBuddyCreditsExhaustedReason) &&
		account.TempUnschedulableUntil != nil && s.now().Before(*account.TempUnschedulableUntil) {
		if err := s.accountRepo.ClearTempUnschedulable(ctx, account.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("积分已恢复（%.2f），解除暂停", remain), nil
	}
	return fmt.Sprintf("剩余 %.2f", remain), nil
}
