package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// WorkBuddy 成长任务中心，移植自参考实现 workbuddy2api-panel 的 tasks.go / autotask.go /
// blackcat.go，只保留"真实使用"能完成的部分：
//   - 查看任务、报名（accept）、领取已达标任务的奖励；
//   - 需要真实对话的任务（chat_5、Model_chat_GLM5.2、first_buddy、black_cat）用真实对话完成，
//     每次对话后上报一条对应的对话事件。
//
// 参考实现里靠伪造桌面端 / 小程序行为事件点亮的任务（RichMeow_Chat、template_5、
// Sequential_Tasks_* 等）不做：这些任务只能在官方客户端里真实操作完成，完成后可在这里领奖。

const (
	tencentCodeBuddyTasksListPath   = "/v2/activity/growth/tasks"
	tencentCodeBuddyTasksAcceptPath = "/v2/activity/growth/tasks/accept"

	tencentCodeBuddyNightChatModel = "glm-5.2"
)

// tencentCodeBuddyTaskChatGap 是连续真实对话之间的间隔；tencentCodeBuddyTaskPollGap
// 是对话后回读进度的间隔（上游计分是异步的，约 5–8 秒后才更新）。测试里置 0。
var (
	tencentCodeBuddyTaskChatGap      = 4 * time.Second
	tencentCodeBuddyTaskPollGap      = 3 * time.Second
	tencentCodeBuddyTaskPollAttempts = 4
)

// TencentCodeBuddyGrowthTask 是成长任务的对外视图。
type TencentCodeBuddyGrowthTask struct {
	TaskCode     string `json:"task_code"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
	TaskDesc     string `json:"task_desc,omitempty"`
	Credit       int64  `json:"credit"`
	Energy       int64  `json:"energy"`
	Locked       bool   `json:"locked"`
	Target       int64  `json:"target"`
	Current      int64  `json:"current"`
	AcceptStatus string `json:"accept_status"`
	Claimable    bool   `json:"claimable"`
	Claimed      bool   `json:"claimed"`
	// Auto 表示网关能用真实对话自动完成该任务；AutoHint 说明怎么完成。
	Auto     bool   `json:"auto"`
	AutoHint string `json:"auto_hint,omitempty"`
}

func (c *TencentCodeBuddyClient) ListGrowthTasks(ctx context.Context, account *Account) ([]TencentCodeBuddyGrowthTask, error) {
	data, err := c.growthJSON(ctx, account, http.MethodGet, tencentCodeBuddyTasksListPath, nil)
	if err != nil {
		return nil, err
	}
	return parseTencentCodeBuddyGrowthTasks(data)
}

func parseTencentCodeBuddyGrowthTasks(data json.RawMessage) ([]TencentCodeBuddyGrowthTask, error) {
	var resp struct {
		Tasks []struct {
			TaskCode     string          `json:"task_code"`
			Title        string          `json:"title"`
			Description  string          `json:"description"`
			TaskDesc     string          `json:"task_desc"`
			RewardCredit int64           `json:"reward_credit"`
			RewardEnergy int64           `json:"reward_energy"`
			Locked       bool            `json:"locked"`
			AcceptStatus string          `json:"accept_status"`
			Target       int64           `json:"target"`
			Current      int64           `json:"current"`
			Progress     json.RawMessage `json:"progress"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	out := make([]TencentCodeBuddyGrowthTask, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		current, target := t.Current, t.Target
		// progress 可能是 {current,target} 对象，优先于平铺字段。
		if len(t.Progress) > 0 && string(t.Progress) != "null" {
			var progress struct {
				Current int64 `json:"current"`
				Target  int64 `json:"target"`
			}
			if json.Unmarshal(t.Progress, &progress) == nil && (progress.Target > 0 || progress.Current > 0) {
				current, target = progress.Current, progress.Target
			}
		}
		claimed := t.AcceptStatus == "claimed"
		task := TencentCodeBuddyGrowthTask{
			TaskCode:     t.TaskCode,
			Title:        t.Title,
			Description:  t.Description,
			TaskDesc:     t.TaskDesc,
			Credit:       t.RewardCredit,
			Energy:       t.RewardEnergy,
			Locked:       t.Locked,
			Target:       target,
			Current:      current,
			AcceptStatus: t.AcceptStatus,
			Claimable:    !claimed && target > 0 && current >= target,
			Claimed:      claimed,
		}
		if action, ok := tencentCodeBuddyTaskActions[t.TaskCode]; ok {
			task.Auto = true
			task.AutoHint = action.hint
		}
		out = append(out, task)
	}
	return out, nil
}

// AcceptGrowthTasks 报名任务。报名本身不产生进度，已报名时重复调用无副作用。
func (c *TencentCodeBuddyClient) AcceptGrowthTasks(ctx context.Context, account *Account, codes []string) error {
	_, err := c.growthJSON(ctx, account, http.MethodPost, tencentCodeBuddyTasksAcceptPath, map[string]any{"task_codes": codes})
	return err
}

// ClaimGrowthTask 领取任务奖励（Web 成长中心的领奖接口，任务码在路径里）。
// 已领过时上游返回 already_claimed，按 0 奖励处理。
func (c *TencentCodeBuddyClient) ClaimGrowthTask(ctx context.Context, account *Account, code string) (credit, energy int64, err error) {
	data, err := c.growthCall(ctx, account, http.MethodPost,
		tencentCodeBuddyWebHost+"/activity/growth/tasks/"+url.PathEscape(code)+"/claim", nil,
		map[string]string{
			"x-client-platform": "web",
			"Accept":            "application/json, text/plain, */*",
			"Origin":            tencentCodeBuddyWebHost,
			"Referer":           tencentCodeBuddyWebHost + "/profile/growth-center",
			"User-Agent":        tencentCodeBuddyUserAgentFor(account.TencentCodeBuddyCredential()),
		})
	if err != nil {
		return 0, 0, err
	}
	var resp struct {
		AlreadyClaimed bool  `json:"already_claimed"`
		Credit         int64 `json:"credit"`
		Energy         int64 `json:"energy"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, 0, err
	}
	if resp.AlreadyClaimed {
		return 0, 0, nil
	}
	return resp.Credit, resp.Energy, nil
}

// ===== 自动完成（真实对话） =====

type tencentCodeBuddyTaskAction struct {
	hint string
	// night 为 true 时只在 23:00–08:00（北京时间）窗口内执行。
	night bool
	run   func(s *TencentCodeBuddyDailyTaskService, ctx context.Context, account *Account, task TencentCodeBuddyGrowthTask) (string, error)
}

var tencentCodeBuddyTaskActions = map[string]tencentCodeBuddyTaskAction{
	"chat_5": {
		hint: "补足差额次数的真实短对话，每次对话后上报",
		run: func(s *TencentCodeBuddyDailyTaskService, ctx context.Context, account *Account, task TencentCodeBuddyGrowthTask) (string, error) {
			return s.realChats(ctx, account, tencentCodeBuddyActivityChatModel, remainingTaskCount(task, 5))
		},
	},
	"Model_chat_GLM5.2": {
		hint: "用 glm-5.2 真实对话一次并上报",
		run: func(s *TencentCodeBuddyDailyTaskService, ctx context.Context, account *Account, task TencentCodeBuddyGrowthTask) (string, error) {
			return s.realChats(ctx, account, "glm-5.2", 1)
		},
	},
	"first_buddy": {
		hint: "真实对话一次后领养第一只猫",
		run: func(s *TencentCodeBuddyDailyTaskService, ctx context.Context, account *Account, task TencentCodeBuddyGrowthTask) (string, error) {
			if _, err := s.realChats(ctx, account, tencentCodeBuddyActivityChatModel, 1); err != nil {
				return "", err
			}
			if err := s.client.AdoptBuddy(ctx, account); err != nil {
				return "已对话，领养失败", err
			}
			return "已领养第一只猫", nil
		},
	},
	"black_cat": {
		hint:  "23:00–08:00 内用 glm-5.2 真实对话补足次数",
		night: true,
		run: func(s *TencentCodeBuddyDailyTaskService, ctx context.Context, account *Account, task TencentCodeBuddyGrowthTask) (string, error) {
			return s.realChats(ctx, account, tencentCodeBuddyNightChatModel, remainingTaskCount(task, 3))
		},
	},
}

func remainingTaskCount(task TencentCodeBuddyGrowthTask, fallbackTarget int64) int {
	target := task.Target
	if target <= 0 {
		target = fallbackTarget
	}
	if need := target - task.Current; need > 0 {
		return int(need)
	}
	return 0
}

// tencentCodeBuddyInNightWindow 报告北京时间是否处于夜猫子计数窗口（23:00–08:00）。
func tencentCodeBuddyInNightWindow(now time.Time) bool {
	hour := now.In(tencentCodeBuddyResetLoc).Hour()
	return hour >= 23 || hour < 8
}

// realChats 发 n 次真实短对话，每次成功后上报对应的对话事件。
func (s *TencentCodeBuddyDailyTaskService) realChats(ctx context.Context, account *Account, model string, n int) (string, error) {
	for i := 0; i < n; i++ {
		if i > 0 {
			select {
			case <-time.After(tencentCodeBuddyTaskChatGap):
			case <-ctx.Done():
				return fmt.Sprintf("完成 %d/%d 次对话", i, n), ctx.Err()
			}
		}
		conversationID, err := s.client.RunShortChat(ctx, account, model)
		if err != nil {
			return fmt.Sprintf("第 %d/%d 次对话失败", i+1, n), err
		}
		if err := s.client.ReportChatActivity(ctx, account, conversationID, model); err != nil {
			return fmt.Sprintf("第 %d/%d 次对话后上报失败", i+1, n), err
		}
	}
	return fmt.Sprintf("完成 %d 次 %s 对话", n, model), nil
}

func (s *TencentCodeBuddyDailyTaskService) loadGrowthAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.accountRepo == nil || s.client == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "TENCENT_CODEBUDDY_NOT_CONFIGURED", "codebuddy tasks are not configured")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !tencentCodeBuddyGrowthSupported(account) {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_GROWTH_UNSUPPORTED", "只有 WorkBuddy 中国大陆个人账号有成长任务")
	}
	return account, nil
}

// ListAccountGrowthTasks 返回账号的成长任务列表。
func (s *TencentCodeBuddyDailyTaskService) ListAccountGrowthTasks(ctx context.Context, accountID int64) ([]TencentCodeBuddyGrowthTask, error) {
	account, err := s.loadGrowthAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.client.ListGrowthTasks(ctx, account)
}

// ClaimAccountGrowthTask 领取账号某个任务的奖励。
func (s *TencentCodeBuddyDailyTaskService) ClaimAccountGrowthTask(ctx context.Context, accountID int64, code string) (string, error) {
	account, err := s.loadGrowthAccount(ctx, accountID)
	if err != nil {
		return "", err
	}
	credit, energy, err := s.client.ClaimGrowthTask(ctx, account, code)
	if err != nil {
		return "", err
	}
	return formatTencentCodeBuddyReward(credit, energy), nil
}

// RunAccountGrowthTask 用真实对话完成账号的某个任务，达标后自动领奖。
func (s *TencentCodeBuddyDailyTaskService) RunAccountGrowthTask(ctx context.Context, accountID int64, code string) (string, error) {
	account, err := s.loadGrowthAccount(ctx, accountID)
	if err != nil {
		return "", err
	}
	tasks, err := s.client.ListGrowthTasks(ctx, account)
	if err != nil {
		return "", err
	}
	for _, task := range tasks {
		if task.TaskCode == code {
			return s.completeGrowthTask(ctx, account, task)
		}
	}
	return "", infraerrors.New(http.StatusNotFound, "CODEBUDDY_TASK_NOT_FOUND", "任务不存在："+code)
}

// completeGrowthTask 报名 → 真实对话 → 回读进度 → 达标领奖。已达标的任务直接领奖。
func (s *TencentCodeBuddyDailyTaskService) completeGrowthTask(ctx context.Context, account *Account, task TencentCodeBuddyGrowthTask) (string, error) {
	if task.Claimed {
		return "已领取过", nil
	}
	if !task.Claimable {
		action, ok := tencentCodeBuddyTaskActions[task.TaskCode]
		if !ok {
			return "", infraerrors.New(http.StatusBadRequest, "CODEBUDDY_TASK_MANUAL", "该任务需要在官方客户端里完成，完成后可在这里领奖")
		}
		if action.night && !tencentCodeBuddyInNightWindow(s.now()) {
			return "", infraerrors.New(http.StatusBadRequest, "CODEBUDDY_TASK_NIGHT_ONLY", "夜猫子任务只在北京时间 23:00–08:00 计数")
		}
		if task.AcceptStatus == "" || task.AcceptStatus == "not_accepted" {
			if err := s.client.AcceptGrowthTasks(ctx, account, []string{task.TaskCode}); err != nil {
				log.Printf("[CodeBuddyTasks] accept %s for account %d: %v", task.TaskCode, account.ID, err)
			}
		}
		message, err := action.run(s, ctx, account, task)
		if err != nil {
			return message, err
		}
		refreshed, ok := s.waitGrowthTask(ctx, account, task.TaskCode)
		if !ok || !refreshed.Claimable {
			if ok && refreshed.Claimed {
				return message + "，奖励已到账", nil
			}
			return message + "，进度还没刷新，稍后再领奖", nil
		}
		task = refreshed
		credit, energy, err := s.client.ClaimGrowthTask(ctx, account, task.TaskCode)
		if err != nil {
			return message + "，领奖失败", err
		}
		return message + "，" + formatTencentCodeBuddyReward(credit, energy), nil
	}
	credit, energy, err := s.client.ClaimGrowthTask(ctx, account, task.TaskCode)
	if err != nil {
		return "领奖失败", err
	}
	return formatTencentCodeBuddyReward(credit, energy), nil
}

// waitGrowthTask 回读任务进度，未达标时有限次轮询（上游计分异步）。
func (s *TencentCodeBuddyDailyTaskService) waitGrowthTask(ctx context.Context, account *Account, code string) (TencentCodeBuddyGrowthTask, bool) {
	var last TencentCodeBuddyGrowthTask
	found := false
	for attempt := 0; attempt < tencentCodeBuddyTaskPollAttempts; attempt++ {
		select {
		case <-time.After(tencentCodeBuddyTaskPollGap):
		case <-ctx.Done():
			return last, found
		}
		tasks, err := s.client.ListGrowthTasks(ctx, account)
		if err != nil {
			continue
		}
		for _, task := range tasks {
			if task.TaskCode == code {
				last, found = task, true
			}
		}
		if found && (last.Claimable || last.Claimed) {
			break
		}
	}
	return last, found
}

func formatTencentCodeBuddyReward(credit, energy int64) string {
	switch {
	case credit == 0 && energy == 0:
		return "已领取"
	case energy == 0:
		return fmt.Sprintf("领取 %d 积分", credit)
	}
	return fmt.Sprintf("领取 %d 积分、%d 能量", credit, energy)
}

// runGrowth 定时任务：报名未报名的任务，用真实对话完成能自动完成的任务（夜猫子除外），
// 领取所有已达标的奖励。
func (s *TencentCodeBuddyDailyTaskService) runGrowth(ctx context.Context, account *Account) (string, error) {
	tasks, err := s.client.ListGrowthTasks(ctx, account)
	if err != nil {
		return "", err
	}
	var accept []string
	for _, task := range tasks {
		if !task.Locked && (task.AcceptStatus == "" || task.AcceptStatus == "not_accepted") {
			accept = append(accept, task.TaskCode)
		}
	}
	if len(accept) > 0 {
		if err := s.client.AcceptGrowthTasks(ctx, account, accept); err == nil {
			for i := range tasks {
				for _, code := range accept {
					if tasks[i].TaskCode == code {
						tasks[i].AcceptStatus = "accepted"
					}
				}
			}
		}
	}
	var parts []string
	failed := 0
	for _, task := range tasks {
		if task.Locked || task.Claimed {
			continue
		}
		action, auto := tencentCodeBuddyTaskActions[task.TaskCode]
		if !task.Claimable && (!auto || action.night) {
			continue
		}
		message, err := s.completeGrowthTask(ctx, account, task)
		label := task.Title
		if label == "" {
			label = task.TaskCode
		}
		if err != nil {
			failed++
			parts = append(parts, label+"："+strings.TrimPrefix(message+" "+err.Error(), " "))
			continue
		}
		parts = append(parts, label+"："+message)
	}
	if len(parts) == 0 {
		return "没有可自动完成或可领取的任务", nil
	}
	summary := truncateString(strings.Join(parts, "；"), 300)
	if failed > 0 {
		return summary, fmt.Errorf("%d 个任务失败", failed)
	}
	return summary, nil
}

// runBlackCat 定时任务：夜间窗口内补足 black_cat 的对话次数并领奖。
func (s *TencentCodeBuddyDailyTaskService) runBlackCat(ctx context.Context, account *Account) (string, error) {
	if !tencentCodeBuddyInNightWindow(s.now()) {
		return "不在 23:00–08:00 窗口内，跳过", nil
	}
	tasks, err := s.client.ListGrowthTasks(ctx, account)
	if err != nil {
		return "", err
	}
	for _, task := range tasks {
		if task.TaskCode == "black_cat" {
			if task.Claimed {
				return "今天已完成", nil
			}
			return s.completeGrowthTask(ctx, account, task)
		}
	}
	return "当前没有夜猫子任务", nil
}
