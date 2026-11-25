package workbuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type growthTaskItem struct {
	Code      string
	Status    string
	Credit    int
	Accepted  bool
	Completed bool
	Claimed   bool
	CanClaim  bool
}

// BuildSkillShareEvents 构造 skill_share 激励任务所需的完整 4 事件遥测上报负载。
//
// 每个事件对象内部均强制携带 qimei36、machineId 与 userId，确保服务端将上报关联至对应账号设备。
func BuildSkillShareEvents(creds AccountCreds, nowMs int64, shareID string) []map[string]any {
	qimei36, machineID := EnsureDevice(creds.Qimei36, creds.MachineID)
	uid := strings.TrimSpace(creds.UID)
	if nowMs <= 0 {
		nowMs = time.Now().UnixMilli()
	}
	if strings.TrimSpace(shareID) == "" {
		shareID = generateUUIDv4()
	}

	makeEvent := func(name string, offsetMs int64, extra map[string]any) map[string]any {
		ev := map[string]any{
			"event":        name,
			"eventName":    name,
			"timestamp":    nowMs + offsetMs,
			"qimei36":      qimei36,
			"machineId":    machineID,
			"userId":       uid,
			"product":      ProductName,
			"ideType":      IDEType,
			"ideName":      IDEName,
			"ideVersion":   IDEVersion,
			"clientCommit": ClientCommit,
			"shareId":      shareID,
		}
		for k, v := range extra {
			ev[k] = v
		}
		return ev
	}

	return []map[string]any{
		makeEvent("skill_share_entry_click", 0, map[string]any{
			"entry":   "skill_card",
			"channel": "copy_link",
		}),
		makeEvent("skill_share_channel_action", 120, map[string]any{
			"channel": "copy_link",
			"action":  "copy",
		}),
		makeEvent("skill_share_deeplink_result", 260, map[string]any{
			"channel": "copy_link",
			"result":  "success",
			"success": true,
		}),
		makeEvent("skill_share_conversion", 480, map[string]any{
			"channel":   "copy_link",
			"converted": true,
			"success":   true,
		}),
	}
}

// Harvest 通过账号专属代理与绑定设备指纹执行一次全自动零成本积分收割流水线。
func Harvest(ctx context.Context, creds AccountCreds, doChat bool) (*HarvestResult, error) {
	return DefaultClient().Harvest(ctx, creds, doChat)
}

// Harvest 执行完整的 WorkBuddy 10 步积分收割流水线：
// accept_tasks -> daily_checkin -> claim_gift -> claim_compensation -> forge_skill_share
// -> open_blindbox -> redeem_streak -> lottery_draw -> chat_task(可选) -> claim_completed。
func (c *Client) Harvest(ctx context.Context, creds AccountCreds, doChat bool) (*HarvestResult, error) {
	creds = c.prepareCreds(creds)
	res := &HarvestResult{
		OK:         true,
		AccountUID: creds.UID,
		Steps:      make([]HarvestStep, 0, 10),
	}

	extra := AuthHeaders(creds, false, true)
	fatalAuth := false

	recordStep := func(step HarvestStep, err error) bool {
		if err != nil {
			if apiErr, ok := unwrapNestedError(err); ok {
				if apiErr.IsAlreadyDone() {
					step.Kind = "skip"
					step.OK = true
					step.Skipped = true
					if step.Detail == "" {
						step.Detail = apiErr.Message
					}
					res.Steps = append(res.Steps, step)
					return true
				}
				if apiErr.IsAuthFailure() {
					step.Kind = "fail"
					step.OK = false
					step.Detail = apiErr.Error()
					res.Steps = append(res.Steps, step)
					res.OK = false
					res.Error = apiErr.Error()
					fatalAuth = true
					return false
				}
			}
			step.Kind = "fail"
			step.OK = false
			step.Detail = err.Error()
			res.Steps = append(res.Steps, step)
			return true
		}
		if step.Skipped {
			step.Kind = "skip"
			step.OK = true
		} else {
			step.Kind = "ok"
			step.OK = true
		}
		res.CreditGained += step.Credit
		res.Steps = append(res.Steps, step)
		return true
	}

	// 1. accept_tasks
	tasks, tasksErr := c.fetchGrowthTasks(ctx, creds, extra)
	if tasksErr != nil {
		if !recordStep(HarvestStep{Name: "accept_tasks"}, tasksErr) && fatalAuth {
			return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
		}
	} else {
		var toAccept []string
		for _, t := range tasks {
			if !t.Accepted && !t.Completed && !t.Claimed && t.Code != "" {
				toAccept = append(toAccept, t.Code)
			}
		}
		if len(toAccept) == 0 {
			recordStep(HarvestStep{Name: "accept_tasks", Skipped: true, Detail: "no unaccepted tasks"}, nil)
		} else {
			_, _, _, _, err := c.doAPI(ctx, creds, http.MethodPost, PathGrowthTasksAccept, map[string]any{
				"taskCodes": toAccept,
			}, "application/json", extra)
			if err != nil {
				if !recordStep(HarvestStep{Name: "accept_tasks"}, err) && fatalAuth {
					return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
				}
			} else {
				recordStep(HarvestStep{Name: "accept_tasks", Detail: fmt.Sprintf("accepted=%s", strings.Join(toAccept, ","))}, nil)
			}
		}
	}

	// 2. daily_checkin
	{
		_, root, _, _, err := c.doAPI(ctx, creds, http.MethodPost, PathDailyCheckin, map[string]any{}, "application/json", extra)
		if err != nil {
			if !recordStep(HarvestStep{Name: "daily_checkin"}, err) && fatalAuth {
				return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
			}
		} else {
			data := unwrapDataMap(root)
			if parseFirstBool(data, "already", "alreadyCheckedIn", "checkedInAlready") {
				recordStep(HarvestStep{Name: "daily_checkin", Skipped: true, Detail: "already checked in"}, nil)
			} else {
				credit := firstInt(data, "credit", "credits", "rewardCredits", "amount", "gained")
				streak := firstInt(data, "streak", "continuousDays", "checkinDays", "days")
				if streak > res.Streak {
					res.Streak = streak
				}
				checkedDate := firstNonEmptyString(data, "checkedInDate", "date")
				if checkedDate == "" {
					checkedDate = time.Now().Format("2006-01-02")
				}
				res.CheckedInDate = checkedDate
				recordStep(HarvestStep{
					Name:   "daily_checkin",
					Credit: credit,
					Detail: fmt.Sprintf("date=%s streak=%d", checkedDate, streak),
				}, nil)
			}
		}
	}

	// 3. claim_gift
	{
		_, root, _, _, err := c.doAPI(ctx, creds, http.MethodGet, PathGiftStatus, nil, "application/json", extra)
		if err != nil {
			if !recordStep(HarvestStep{Name: "claim_gift"}, err) && fatalAuth {
				return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
			}
		} else {
			data := unwrapDataMap(root)
			claimed := parseFirstBool(data, "claimed", "isClaimed")
			eligible := !claimed && (parseFirstBool(data, "canClaim", "eligible", "available") || (!hasAnyKey(data, "canClaim", "eligible", "available") && !claimed))
			if !eligible {
				recordStep(HarvestStep{Name: "claim_gift", Skipped: true, Detail: "gift not available or already claimed"}, nil)
			} else {
				_, claimRoot, _, _, claimErr := c.doAPI(ctx, creds, http.MethodPost, PathClaimGift, map[string]any{}, "application/json", extra)
				if claimErr != nil {
					if !recordStep(HarvestStep{Name: "claim_gift"}, claimErr) && fatalAuth {
						return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
					}
				} else {
					cData := unwrapDataMap(claimRoot)
					credit := firstInt(cData, "credit", "credits", "amount", "rewardCredits")
					recordStep(HarvestStep{Name: "claim_gift", Credit: credit, Detail: "gift claimed"}, nil)
				}
			}
		}
	}

	// 4. claim_compensation
	{
		_, root, _, _, err := c.doAPI(ctx, creds, http.MethodGet, PathCompStatus, nil, "application/json", extra)
		if err != nil {
			if !recordStep(HarvestStep{Name: "claim_compensation"}, err) && fatalAuth {
				return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
			}
		} else {
			data := unwrapDataMap(root)
			claimed := parseFirstBool(data, "claimed", "isClaimed")
			canClaim := !claimed && parseFirstBool(data, "hasCompensation", "canClaim", "eligible", "available")
			if !canClaim {
				recordStep(HarvestStep{Name: "claim_compensation", Skipped: true, Detail: "no eligible compensation"}, nil)
			} else {
				_, claimRoot, _, _, claimErr := c.doAPI(ctx, creds, http.MethodPost, PathClaimComp, map[string]any{}, "application/json", extra)
				if claimErr != nil {
					if !recordStep(HarvestStep{Name: "claim_compensation"}, claimErr) && fatalAuth {
						return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
					}
				} else {
					cData := unwrapDataMap(claimRoot)
					credit := firstInt(cData, "credit", "credits", "amount")
					recordStep(HarvestStep{Name: "claim_compensation", Credit: credit, Detail: "compensation claimed"}, nil)
				}
			}
		}
	}

	// 5. forge_skill_share（POST /v2/report 发送 4 事件漏斗数组）
	{
		tasksNow, _ := c.fetchGrowthTasks(ctx, creds, extra)
		skillTask, hasSkillTask := findGrowthTask(tasksNow, "skill_share")
		if hasSkillTask && (skillTask.Completed || skillTask.Claimed) {
			recordStep(HarvestStep{Name: "forge_skill_share", Skipped: true, Detail: "skill_share already completed"}, nil)
		} else {
			events := BuildSkillShareEvents(creds, time.Now().UnixMilli(), "")
			_, _, _, _, reportErr := c.doAPI(ctx, creds, http.MethodPost, PathReport, events, "application/json", extra)
			if reportErr != nil {
				if !recordStep(HarvestStep{Name: "forge_skill_share"}, reportErr) && fatalAuth {
					return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
				}
			} else {
				completed := c.pollSkillShareCompletion(ctx, creds, extra)
				detail := fmt.Sprintf("reported 4 events qimei36=%s completed=%t", creds.Qimei36, completed)
				recordStep(HarvestStep{Name: "forge_skill_share", Detail: detail}, nil)
			}
		}
	}

	// 6. open_blindbox
	{
		_, root, _, _, err := c.doAPI(ctx, creds, http.MethodGet, PathGrowthBuddyQuota, nil, "application/json", extra)
		if err != nil {
			if !recordStep(HarvestStep{Name: "open_blindbox"}, err) && fatalAuth {
				return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
			}
		} else {
			data := unwrapDataMap(root)
			quota := firstInt(data, "remainQuota", "quota", "remain", "availableCount", "count")
			if quota <= 0 {
				recordStep(HarvestStep{Name: "open_blindbox", Skipped: true, Detail: "quota=0"}, nil)
			} else {
				if quota > 10 {
					quota = 10
				}
				var opened, gained int
				var openErr error
				for i := 0; i < quota; i++ {
					_, openRoot, _, _, e := c.doAPI(ctx, creds, http.MethodPost, PathGrowthBuddyOpen, map[string]any{}, "application/json", extra)
					if e != nil {
						if apiErr, ok := unwrapNestedError(e); ok && apiErr.IsAlreadyDone() {
							break
						}
						openErr = e
						break
					}
					opened++
					oData := unwrapDataMap(openRoot)
					gained += firstInt(oData, "credit", "credits", "rewardCredits", "amount")
				}
				if openErr != nil && opened == 0 {
					recordStep(HarvestStep{Name: "open_blindbox"}, openErr)
				} else {
					recordStep(HarvestStep{
						Name:   "open_blindbox",
						Credit: gained,
						Detail: fmt.Sprintf("opened=%d", opened),
					}, nil)
				}
			}
		}
	}

	// 7. redeem_streak
	{
		_, root, _, _, err := c.doAPI(ctx, creds, http.MethodGet, PathGrowthStreak, nil, "application/json", extra)
		if err != nil {
			if !recordStep(HarvestStep{Name: "redeem_streak"}, err) && fatalAuth {
				return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
			}
		} else {
			data := unwrapDataMap(root)
			if s := firstInt(data, "streak", "days", "currentStreak", "continuousDays"); s > res.Streak {
				res.Streak = s
			}
			milestones := extractSlice(data, "milestones")
			if len(milestones) == 0 {
				milestones = extractSlice(data, "rewards")
			}
			var redeemed, gained int
			var redeemErr error
			for _, item := range milestones {
				mm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if parseFirstBool(mm, "claimed", "isClaimed", "redeemed") {
					continue
				}
				if !parseFirstBool(mm, "canClaim", "eligible", "reached", "canRedeem") {
					continue
				}
				payload := map[string]any{}
				if id := firstNonEmptyString(mm, "id", "milestoneId", "rewardId"); id != "" {
					payload["milestoneId"] = id
					payload["rewardId"] = id
				}
				if d := firstInt(mm, "days", "targetDays", "streakDays"); d > 0 {
					payload["days"] = d
				}
				_, rRoot, _, _, e := c.doAPI(ctx, creds, http.MethodPost, PathGrowthRedeem, payload, "application/json", extra)
				if e != nil {
					if apiErr, ok := unwrapNestedError(e); ok && apiErr.IsAlreadyDone() {
						continue
					}
					redeemErr = e
					break
				}
				redeemed++
				rData := unwrapDataMap(rRoot)
				gained += firstInt(rData, "credit", "credits", "rewardCredits", "amount")
			}
			if redeemErr != nil && redeemed == 0 {
				recordStep(HarvestStep{Name: "redeem_streak"}, redeemErr)
			} else if redeemed == 0 {
				recordStep(HarvestStep{Name: "redeem_streak", Skipped: true, Detail: "no claimable streak milestones"}, nil)
			} else {
				recordStep(HarvestStep{Name: "redeem_streak", Credit: gained, Detail: fmt.Sprintf("redeemed=%d", redeemed)}, nil)
			}
		}
	}

	// 8. lottery_draw
	{
		_, root, _, _, err := c.doAPI(ctx, creds, http.MethodGet, PathGrowthLottery, nil, "application/json", extra)
		if err != nil {
			if !recordStep(HarvestStep{Name: "lottery_draw"}, err) && fatalAuth {
				return res, fmt.Errorf("harvest aborted on auth failure: %s", res.Error)
			}
		} else {
			data := unwrapDataMap(root)
			chances := firstInt(data, "remainChances", "freeChances", "chances", "remainCount", "remain")
			if chances <= 0 {
				recordStep(HarvestStep{Name: "lottery_draw", Skipped: true, Detail: "chances=0"}, nil)
			} else {
				if chances > 10 {
					chances = 10
				}
				var drawn, gained int
				var drawErr error
				for i := 0; i < chances; i++ {
					_, dRoot, _, _, e := c.doAPI(ctx, creds, http.MethodPost, PathGrowthLotteryDraw, map[string]any{}, "application/json", extra)
					if e != nil {
						if apiErr, ok := unwrapNestedError(e); ok && apiErr.IsAlreadyDone() {
							break
						}
						drawErr = e
						break
					}
					drawn++
					dData := unwrapDataMap(dRoot)
					gained += firstInt(dData, "credit", "credits", "rewardCredits", "amount")
				}
				if drawErr != nil && drawn == 0 {
					recordStep(HarvestStep{Name: "lottery_draw"}, drawErr)
				} else {
					recordStep(HarvestStep{Name: "lottery_draw", Credit: gained, Detail: fmt.Sprintf("drawn=%d", drawn)}, nil)
				}
			}
		}
	}

	// 9. chat_task（可选：触发 RichMeow_Chat 对话任务）
	if !doChat {
		recordStep(HarvestStep{Name: "chat_task", Skipped: true, Detail: "doChat=false"}, nil)
	} else {
		tasksNow, _ := c.fetchGrowthTasks(ctx, creds, extra)
		chatTask, hasChatTask := findGrowthTask(tasksNow, "RichMeow_Chat")
		if hasChatTask && (chatTask.Completed || chatTask.Claimed) {
			recordStep(HarvestStep{Name: "chat_task", Skipped: true, Detail: "RichMeow_Chat already completed"}, nil)
		} else {
			lat, pingErr := c.Ping(ctx, creds, DefaultFastModel)
			if pingErr != nil {
				recordStep(HarvestStep{Name: "chat_task"}, pingErr)
			} else {
				recordStep(HarvestStep{Name: "chat_task", Detail: fmt.Sprintf("ping_ms=%d", lat.Milliseconds())}, nil)
			}
		}
	}

	// 10. claim_completed
	{
		finalTasks, err := c.fetchGrowthTasks(ctx, creds, extra)
		if err != nil {
			recordStep(HarvestStep{Name: "claim_completed"}, err)
		} else {
			var claimable []growthTaskItem
			for _, t := range finalTasks {
				if t.Code == "" || t.Claimed {
					continue
				}
				if t.CanClaim || t.Completed {
					claimable = append(claimable, t)
				}
			}
			if len(claimable) == 0 {
				recordStep(HarvestStep{Name: "claim_completed", Skipped: true, Detail: "no claimable completed tasks"}, nil)
			} else {
				var claimedCount, gained int
				var claimedCodes []string
				var lastErr error
				for _, t := range claimable {
					claimPath := fmt.Sprintf("/activity/growth/tasks/%s/claim", url.PathEscape(t.Code))
					_, cRoot, _, _, e := c.doAPI(ctx, creds, http.MethodPost, claimPath, map[string]any{}, "application/json", extra)
					if e != nil {
						if apiErr, ok := unwrapNestedError(e); ok && apiErr.IsAlreadyDone() {
							continue
						}
						lastErr = e
						continue
					}
					claimedCount++
					claimedCodes = append(claimedCodes, t.Code)
					cData := unwrapDataMap(cRoot)
					cCredit := firstInt(cData, "credit", "credits", "rewardCredits", "amount")
					if cCredit == 0 {
						cCredit = t.Credit
					}
					gained += cCredit
				}
				if lastErr != nil && claimedCount == 0 {
					recordStep(HarvestStep{Name: "claim_completed"}, lastErr)
				} else if claimedCount == 0 {
					recordStep(HarvestStep{Name: "claim_completed", Skipped: true, Detail: "all claimable tasks already claimed"}, nil)
				} else {
					recordStep(HarvestStep{
						Name:   "claim_completed",
						Credit: gained,
						Detail: fmt.Sprintf("claimed=%s", strings.Join(claimedCodes, ",")),
					}, nil)
				}
			}
		}
	}

	return res, nil
}

func (c *Client) pollSkillShareCompletion(ctx context.Context, creds AccountCreds, extra http.Header) bool {
	delay := c.getHarvestPollDelay()
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false
			case <-timer.C:
			}
		}
		tasks, err := c.fetchGrowthTasks(ctx, creds, extra)
		if err != nil {
			continue
		}
		if t, ok := findGrowthTask(tasks, "skill_share"); ok && (t.Completed || t.Claimed || t.CanClaim) {
			return true
		}
	}
	return false
}

func (c *Client) fetchGrowthTasks(ctx context.Context, creds AccountCreds, extra http.Header) ([]growthTaskItem, error) {
	rawBytes, root, _, _, err := c.doAPI(ctx, creds, http.MethodGet, PathGrowthTasks, nil, "application/json", extra)
	if err != nil {
		return nil, err
	}

	var rawList []any
	if root != nil {
		data := unwrapDataMap(root)
		rawList = extractSlice(data, "tasks")
		if len(rawList) == 0 {
			rawList = extractSlice(data, "list")
		}
		if len(rawList) == 0 {
			rawList = extractSlice(data, "items")
		}
		if len(rawList) == 0 {
			rawList = extractSlice(root, "data")
		}
		if len(rawList) == 0 {
			rawList = extractSlice(root, "tasks")
		}
	} else {
		_ = json.Unmarshal(rawBytes, &rawList)
	}

	tasks := make([]growthTaskItem, 0, len(rawList))
	for _, item := range rawList {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		code := firstNonEmptyString(m, "code", "taskCode", "taskId", "id")
		status := strings.ToLower(firstNonEmptyString(m, "status", "state", "taskStatus"))
		credit := firstInt(m, "credit", "credits", "rewardCredits", "reward", "amount")

		claimed := parseFirstBool(m, "claimed", "isClaimed") || status == "claimed" || status == "rewarded" || status == "3"
		completed := parseFirstBool(m, "completed", "isCompleted", "finished") || status == "completed" || status == "finished" || status == "done" || status == "wait_claim" || status == "claimable" || status == "2" || claimed
		canClaim := !claimed && (parseFirstBool(m, "canClaim", "claimable") || status == "wait_claim" || status == "claimable" || status == "2" || (completed && !claimed))
		accepted := parseFirstBool(m, "accepted", "isAccepted") || completed || claimed || status == "in_progress" || status == "accepted" || status == "ongoing" || status == "running" || status == "1"

		tasks = append(tasks, growthTaskItem{
			Code:      code,
			Status:    status,
			Credit:    credit,
			Accepted:  accepted,
			Completed: completed,
			Claimed:   claimed,
			CanClaim:  canClaim,
		})
	}
	return tasks, nil
}

func findGrowthTask(tasks []growthTaskItem, code string) (growthTaskItem, bool) {
	for _, t := range tasks {
		if strings.EqualFold(t.Code, code) {
			return t, true
		}
	}
	return growthTaskItem{}, false
}

func hasAnyKey(m map[string]any, keys ...string) bool {
	if m == nil {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}
