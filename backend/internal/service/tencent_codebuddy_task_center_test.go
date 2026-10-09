package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func zeroTaskCenterGaps(t *testing.T) {
	chat, poll := tencentCodeBuddyTaskChatGap, tencentCodeBuddyTaskPollGap
	tencentCodeBuddyTaskChatGap, tencentCodeBuddyTaskPollGap = 0, 0
	t.Cleanup(func() { tencentCodeBuddyTaskChatGap, tencentCodeBuddyTaskPollGap = chat, poll })
}

func TestParseTencentCodeBuddyGrowthTasks(t *testing.T) {
	tasks, err := parseTencentCodeBuddyGrowthTasks([]byte(`{"tasks":[
		{"task_code":"chat_5","title":"对话5次","reward_credit":100,"accept_status":"accepted","progress":{"current":5,"target":5}},
		{"task_code":"RichMeow_Chat","title":"桌面端对话","accept_status":"claimed","target":1,"current":1},
		{"task_code":"black_cat","accept_status":"not_accepted","target":3,"current":1}]}`))
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	require.True(t, tasks[0].Claimable)
	require.True(t, tasks[0].Auto)
	require.Equal(t, int64(100), tasks[0].Credit)
	require.False(t, tasks[1].Auto, "伪造事件类任务不自动完成")
	require.True(t, tasks[1].Claimed)
	require.False(t, tasks[1].Claimable)
	require.Equal(t, 2, remainingTaskCount(tasks[2], 3))
}

func TestTencentCodeBuddyInNightWindow(t *testing.T) {
	at := func(hour int) time.Time { return time.Date(2026, 10, 9, hour, 30, 0, 0, tencentCodeBuddyResetLoc) }
	require.True(t, tencentCodeBuddyInNightWindow(at(23)))
	require.True(t, tencentCodeBuddyInNightWindow(at(7)))
	require.False(t, tencentCodeBuddyInNightWindow(at(8)))
	require.False(t, tencentCodeBuddyInNightWindow(at(22)))
}

func TestCompleteGrowthTask_ModelChatThenClaim(t *testing.T) {
	zeroTaskCenterGaps(t)
	var mu sync.Mutex
	chatted := false
	var chatModels []string
	upstream := newTencentCodeBuddyTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			chatted = true
			chatModels = append(chatModels, string(body))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\ndata: [DONE]\n\n")
		case r.URL.Path == "/v2/report":
			_, _ = io.WriteString(w, `{"code":0}`)
		case r.URL.Path == tencentCodeBuddyTasksAcceptPath:
			_, _ = io.WriteString(w, `{"code":0}`)
		case r.URL.Path == tencentCodeBuddyTasksListPath:
			current := 0
			if chatted {
				current = 1
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"tasks":[{"task_code":"Model_chat_GLM5.2","accept_status":"accepted","target":1,"current":`+string(rune('0'+current))+`}]}}`)
		case r.URL.Path == "/activity/growth/tasks/Model_chat_GLM5.2/claim":
			require.Equal(t, "www.workbuddy.cn", r.Host)
			require.Equal(t, "web", r.Header.Get("x-client-platform"))
			_, _ = io.WriteString(w, `{"code":0,"data":{"credit":100,"energy":5}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	svc := NewTencentCodeBuddyDailyTaskService(nil, NewTencentCodeBuddyClient(upstream), nil)
	account := growthTestAccount()
	task := TencentCodeBuddyGrowthTask{TaskCode: "Model_chat_GLM5.2", AcceptStatus: "not_accepted", Target: 1}

	message, err := svc.completeGrowthTask(context.Background(), account, task)
	require.NoError(t, err)
	require.Equal(t, "完成 1 次 glm-5.2 对话，领取 100 积分、5 能量", message)
	require.Len(t, chatModels, 1)
	require.Contains(t, chatModels[0], `"model":"glm-5.2"`)
}

func TestCompleteGrowthTask_ManualTaskNotFaked(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected upstream call %s", r.URL.Path)
	})
	svc := NewTencentCodeBuddyDailyTaskService(nil, NewTencentCodeBuddyClient(upstream), nil)
	_, err := svc.completeGrowthTask(context.Background(), growthTestAccount(),
		TencentCodeBuddyGrowthTask{TaskCode: "RichMeow_Chat", Target: 1})
	require.Error(t, err)
}

func TestCompleteGrowthTask_BlackCatOnlyAtNight(t *testing.T) {
	upstream := newTencentCodeBuddyTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected upstream call %s", r.URL.Path)
	})
	svc := NewTencentCodeBuddyDailyTaskService(nil, NewTencentCodeBuddyClient(upstream), nil)
	svc.now = func() time.Time { return time.Date(2026, 10, 9, 15, 0, 0, 0, tencentCodeBuddyResetLoc) }
	_, err := svc.completeGrowthTask(context.Background(), growthTestAccount(),
		TencentCodeBuddyGrowthTask{TaskCode: "black_cat", Target: 3})
	require.Error(t, err)
	message, err := svc.runBlackCat(context.Background(), growthTestAccount())
	require.NoError(t, err)
	require.Contains(t, message, "跳过")
}
