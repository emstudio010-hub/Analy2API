package workbuddy

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var packageCodeDisplayNames = map[string]string{
	"TCACA_code_001": "体验积分包",
	"TCACA_code_002": "免费基础积分",
	"TCACA_code_003": "每日签到积分",
	"TCACA_code_007": "专享活动积分",
	"TCACA_code_008": "任务激励加赠积分",
	"TCACA_code_009": "限时体验包",
	"TCACA_code_010": "邀请奖励积分",
	"TCACA_code_020": "专业版套餐积分",
	"TCACA_code_030": "订阅周期基础积分",
}

// FetchBillingSummary 通过账号专属代理与设备指纹查询 WorkBuddy 高精度 PascalCase 账单积分明细。
func FetchBillingSummary(ctx context.Context, creds AccountCreds) (*BillingSummary, error) {
	return DefaultClient().FetchBillingSummary(ctx, creds)
}

// FetchBillingSummary 调用 POST {Domain}/billing/meter/get-user-resource-summary 并解析高精度积分数据。
func (c *Client) FetchBillingSummary(ctx context.Context, creds AccountCreds) (*BillingSummary, error) {
	extra := AuthHeaders(creds, false, true)
	_, root, _, _, err := c.doAPI(ctx, creds, http.MethodPost, PathBillingSummary, map[string]any{}, "application/json", extra)
	if err != nil {
		return nil, err
	}

	data := unwrapDataMap(root)
	summary := &BillingSummary{
		SubscriptionPackageName: firstNonEmptyString(data, "SubscriptionPackageName", "subscriptionPackageName", "subscription_package_name"),
		IsPaidUser:              parseFirstBool(data, "IsPaidUser", "isPaidUser", "is_paid_user"),
		Raw:                     data,
	}

	// 解析 CreditGroups
	rawGroups := extractSlice(data, "CreditGroups")
	if len(rawGroups) == 0 {
		rawGroups = extractSlice(data, "creditGroups")
	}
	if len(rawGroups) == 0 {
		rawGroups = extractSlice(data, "credit_groups")
	}

	var groupsRemainSum float64
	for _, item := range rawGroups {
		gm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		totalCap, _ := parseOptionalFloat(gm, "CycleTotalCapacityPrecise", "CycleTotalCapacity", "cycleTotalCapacityPrecise", "cycleTotalCapacity", "cycle_total_capacity")
		remainCap, _ := parseOptionalFloat(gm, "CycleRemainCapacityPrecise", "CycleRemainCapacity", "cycleRemainCapacityPrecise", "cycleRemainCapacity", "cycle_remain_capacity")
		usedCap, hasUsed := parseOptionalFloat(gm, "CycleUsedCapacityPrecise", "CycleUsedCapacity", "cycleUsedCapacityPrecise", "cycleUsedCapacity", "cycle_used_capacity")
		if !hasUsed && totalCap >= remainCap {
			usedCap = totalCap - remainCap
		}
		groupsRemainSum += remainCap

		summary.CreditGroups = append(summary.CreditGroups, CreditGroup{
			Kind:                firstNonEmptyString(gm, "Kind", "kind"),
			Name:                firstNonEmptyString(gm, "Name", "name"),
			CycleTotalCapacity:  totalCap,
			CycleRemainCapacity: remainCap,
			CycleUsedCapacity:   usedCap,
		})
	}

	// 优先读取 PascalCase TotalCredits（可能为高精度字符串或浮点数）；缺失时回退聚合 CreditGroups。
	if tc, ok := parseOptionalFloat(data, "TotalCredits", "totalCredits", "total_credits"); ok {
		summary.TotalCredits = tc
	} else {
		summary.TotalCredits = groupsRemainSum
	}

	// 解析 Packages
	rawPkgs := extractSlice(data, "Packages")
	if len(rawPkgs) == 0 {
		rawPkgs = extractSlice(data, "packages")
	}
	if len(rawPkgs) == 0 {
		rawPkgs = extractSlice(data, "PackageDetails")
	}
	if len(rawPkgs) == 0 {
		rawPkgs = extractSlice(data, "ResourcePackages")
	}

	now := time.Now()
	for _, item := range rawPkgs {
		pm, ok := item.(map[string]any)
		if !ok {
			continue
		}

		code := firstNonEmptyString(pm, "PackageCode", "packageCode", "package_code")
		name := firstNonEmptyString(pm, "PackageName", "packageName", "package_name")
		displayName := resolvePackageDisplayName(code, name)

		total, _ := parseOptionalFloat(pm,
			"TotalCapacityPrecise", "CycleTotalCapacityPrecise", "TotalPrecise",
			"TotalCapacity", "CycleTotalCapacity", "Total", "total", "total_capacity",
		)
		remain, _ := parseOptionalFloat(pm,
			"RemainCapacityPrecise", "CycleRemainCapacityPrecise", "RemainPrecise",
			"RemainCapacity", "CycleRemainCapacity", "Remain", "remain", "remain_capacity",
		)
		used, hasUsed := parseOptionalFloat(pm,
			"UsedCapacityPrecise", "CycleUsedCapacityPrecise", "UsedPrecise",
			"UsedCapacity", "CycleUsedCapacity", "Used", "used", "used_capacity",
		)
		if !hasUsed && total >= remain {
			used = total - remain
		}

		expiredAt, expired, expiringSoon, daysLeft := resolvePackageExpiry(pm, now)

		summary.Packages = append(summary.Packages, CreditPackage{
			PackageCode:  code,
			PackageName:  name,
			DisplayName:  displayName,
			Kind:         firstNonEmptyString(pm, "Kind", "kind", "DeductType", "deductType"),
			Total:        total,
			Remain:       remain,
			Used:         used,
			TotalCount:   firstInt(pm, "TotalCount", "totalCount", "total_count"),
			ExpiredAt:    expiredAt,
			Expired:      expired,
			ExpiringSoon: expiringSoon,
			DaysLeft:     daysLeft,
		})
	}

	return summary, nil
}

func resolvePackageDisplayName(code, rawName string) string {
	if exact, ok := packageCodeDisplayNames[code]; ok {
		return exact
	}
	for prefix, label := range packageCodeDisplayNames {
		if strings.HasPrefix(code, prefix+"_") || strings.HasPrefix(code, prefix) {
			if rawName != "" {
				return rawName
			}
			return label
		}
	}
	return fallbackString(rawName, code)
}

func resolvePackageExpiry(pm map[string]any, now time.Time) (*string, bool, bool, *float64) {
	if parseFirstBool(pm, "Expired", "expired") {
		if expStr := firstNonEmptyString(pm, "ExpiredAt", "expiredAt", "expired_at", "EndTime", "endTime", "CycleEndTime"); expStr != "" {
			return &expStr, true, false, nil
		}
		return nil, true, false, nil
	}

	var expTime *time.Time
	for _, k := range []string{"ExpiredAt", "expiredAt", "expired_at", "EndTime", "endTime", "CycleEndTime", "cycleEndTime"} {
		val, exists := pm[k]
		if !exists || val == nil {
			continue
		}
		if t, ok := parseFlexibleTime(val); ok {
			expTime = &t
			break
		}
	}
	if expTime == nil {
		return nil, false, false, nil
	}

	// 超过 730 天的远期哨兵时间戳视为永不过期。
	days := expTime.Sub(now).Hours() / 24.0
	if days > 730 || expTime.Year() >= 2099 {
		return nil, false, false, nil
	}

	formatted := expTime.UTC().Format(time.RFC3339)
	expired := !expTime.After(now)
	expiringSoon := !expired && days <= 7.0
	return &formatted, expired, expiringSoon, &days
}

func parseFlexibleTime(val any) (time.Time, bool) {
	switch t := val.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" || s == "0" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if parsed, err := time.Parse(layout, s); err == nil {
				return parsed, true
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return unixTimestampToTime(n)
		}
	default:
		if n, ok := toInt64(val); ok {
			return unixTimestampToTime(n)
		}
	}
	return time.Time{}, false
}

func unixTimestampToTime(n int64) (time.Time, bool) {
	if n <= 0 {
		return time.Time{}, false
	}
	if n > 1_000_000_000_000 {
		return time.UnixMilli(n), true
	}
	return time.Unix(n, 0), true
}

func parseFirstBool(m map[string]any, keys ...string) bool {
	if m == nil {
		return false
	}
	for _, k := range keys {
		if v, exists := m[k]; exists && v != nil {
			return parseBoolValue(v)
		}
	}
	return false
}
