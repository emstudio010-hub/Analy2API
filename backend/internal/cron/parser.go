// Package cron 提供标准 5 段 / 6 段及描述符 Cron 表达式解析与未来运行时间计算。
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrInvalidExpr 表示 Cron 表达式语法无效。
	ErrInvalidExpr = errors.New("invalid cron expression")
)

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4,
	"MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8,
	"SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var dowNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3,
	"THU": 4, "FRI": 5, "SAT": 6,
}

// Schedule 表示已解析的 Cron 调度计划。
type Schedule struct {
	expr     string
	interval time.Duration // 非零表示 @every <duration>

	seconds uint64 // bit 0..59
	minutes uint64 // bit 0..59
	hours   uint64 // bit 0..23
	dom     uint64 // bit 1..31
	months  uint64 // bit 1..12
	dow     uint64 // bit 0..6 (0 = Sunday)

	domWild bool // 日期字段是否为通配符 *
	dowWild bool // 星期字段是否为通配符 *
}

// Parse 解析 Cron 表达式。
//
// 支持格式：
//   - 标准 5 段：min hour dom mon dow（秒默认 0）
//   - 扩展 6 段：sec min hour dom mon dow
//   - 描述符：@yearly / @annually / @monthly / @weekly / @daily / @midnight / @hourly / @every <duration>
func Parse(expr string) (*Schedule, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return nil, fmt.Errorf("%w: 表达式不能为空", ErrInvalidExpr)
	}

	if strings.HasPrefix(raw, "@") {
		return parseDescriptor(raw)
	}

	fields := strings.Fields(raw)
	if len(fields) != 5 && len(fields) != 6 {
		return nil, fmt.Errorf("%w: 需要 5 段 (分 时 日 月 周) 或 6 段 (秒 分 时 日 月 周)，实际为 %d 段", ErrInvalidExpr, len(fields))
	}

	secExpr := "0"
	idx := 0
	if len(fields) == 6 {
		secExpr = fields[0]
		idx = 1
	}

	minExpr := fields[idx]
	hourExpr := fields[idx+1]
	domExpr := fields[idx+2]
	monExpr := fields[idx+3]
	dowExpr := fields[idx+4]

	seconds, _, err := parseField(secExpr, 0, 59, nil, false)
	if err != nil {
		return nil, fmt.Errorf("%w (秒): %v", ErrInvalidExpr, err)
	}
	minutes, _, err := parseField(minExpr, 0, 59, nil, false)
	if err != nil {
		return nil, fmt.Errorf("%w (分): %v", ErrInvalidExpr, err)
	}
	hours, _, err := parseField(hourExpr, 0, 23, nil, false)
	if err != nil {
		return nil, fmt.Errorf("%w (时): %v", ErrInvalidExpr, err)
	}
	dom, domWild, err := parseField(domExpr, 1, 31, nil, false)
	if err != nil {
		return nil, fmt.Errorf("%w (日): %v", ErrInvalidExpr, err)
	}
	months, _, err := parseField(monExpr, 1, 12, monthNames, false)
	if err != nil {
		return nil, fmt.Errorf("%w (月): %v", ErrInvalidExpr, err)
	}
	dow, dowWild, err := parseField(dowExpr, 0, 7, dowNames, true)
	if err != nil {
		return nil, fmt.Errorf("%w (周): %v", ErrInvalidExpr, err)
	}

	return &Schedule{
		expr:    raw,
		seconds: seconds,
		minutes: minutes,
		hours:   hours,
		dom:     dom,
		months:  months,
		dow:     dow,
		domWild: domWild,
		dowWild: dowWild,
	}, nil
}

func parseDescriptor(raw string) (*Schedule, error) {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch lower {
	case "@yearly", "@annually":
		return Parse("0 0 1 1 *")
	case "@monthly":
		return Parse("0 0 1 * *")
	case "@weekly":
		return Parse("0 0 * * 0")
	case "@daily", "@midnight":
		return Parse("0 0 * * *")
	case "@hourly":
		return Parse("0 * * * *")
	}

	if strings.HasPrefix(lower, "@every") {
		rest := strings.TrimSpace(strings.TrimPrefix(lower, "@every"))
		if rest == "" {
			return nil, fmt.Errorf("%w: @every 后缺少时间间隔，例如 @every 30m", ErrInvalidExpr)
		}
		dur, err := time.ParseDuration(rest)
		if err != nil {
			return nil, fmt.Errorf("%w: 无效的 @every 间隔 %q: %v", ErrInvalidExpr, rest, err)
		}
		if dur < time.Second {
			return nil, fmt.Errorf("%w: @every 最小间隔不能低于 1s", ErrInvalidExpr)
		}
		return &Schedule{
			expr:     raw,
			interval: dur.Truncate(time.Second),
		}, nil
	}

	return nil, fmt.Errorf("%w: 未知描述符 %q", ErrInvalidExpr, raw)
}

// Expr 返回原始表达式文本。
func (s *Schedule) Expr() string {
	return s.expr
}

// Next 计算 strictly after 指定时间点的下一次调度时间（保留传入时间的 Location）。
// 若未来 5 年内都没有匹配时间（例如 2 月 31 日），返回零值 time.Time{}。
func (s *Schedule) Next(after time.Time) time.Time {
	if s == nil {
		return time.Time{}
	}
	loc := after.Location()
	if s.interval > 0 {
		// 对齐到秒级，严格大于 after
		return after.Truncate(time.Second).Add(s.interval).In(loc)
	}

	// 从 after 下一秒开始匹配
	t := after.Truncate(time.Second).Add(time.Second).In(loc)
	limitYear := t.Year() + 5

	for t.Year() <= limitYear {
		// 1. 匹配月份 (1..12)
		if (s.months & (uint64(1) << uint(t.Month()))) == 0 {
			// 跳到下个月 1 日 00:00:00
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}

		// 2. 匹配日期与星期（标准 Vixie cron 规则：若 dom 与 dow 均非 *，满足其一即可；否则需全部满足）
		if !s.matchDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}

		// 3. 匹配小时 (0..23)
		if (s.hours & (uint64(1) << uint(t.Hour()))) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}

		// 4. 匹配分钟 (0..59)
		if (s.minutes & (uint64(1) << uint(t.Minute()))) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()+1, 0, 0, loc)
			continue
		}

		// 5. 匹配秒 (0..59)
		if (s.seconds & (uint64(1) << uint(t.Second()))) == 0 {
			t = t.Add(time.Second)
			continue
		}

		return t
	}

	return time.Time{}
}

// NextN 计算从 after 起未来 count 次执行时间点。
func (s *Schedule) NextN(after time.Time, count int) []time.Time {
	if s == nil || count <= 0 {
		return nil
	}
	out := make([]time.Time, 0, count)
	cur := after
	for i := 0; i < count; i++ {
		next := s.Next(cur)
		if next.IsZero() {
			break
		}
		out = append(out, next)
		cur = next
	}
	return out
}

func (s *Schedule) matchDay(t time.Time) bool {
	domMatch := (s.dom & (uint64(1) << uint(t.Day()))) != 0
	dowMatch := (s.dow & (uint64(1) << uint(t.Weekday()))) != 0

	if !s.domWild && !s.dowWild {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}

func parseField(expr string, minVal, maxVal int, names map[string]int, isDow bool) (uint64, bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, false, errors.New("字段为空")
	}

	isWild := expr == "*" || expr == "?"
	var bits uint64

	parts := strings.Split(expr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, false, fmt.Errorf("无效列表项 %q", expr)
		}

		rangeAndStep := strings.Split(part, "/")
		if len(rangeAndStep) > 2 {
			return 0, false, fmt.Errorf("步长语法错误 %q", part)
		}

		step := 1
		if len(rangeAndStep) == 2 {
			var err error
			step, err = strconv.Atoi(strings.TrimSpace(rangeAndStep[1]))
			if err != nil || step <= 0 {
				return 0, false, fmt.Errorf("步长必须为正整数: %q", rangeAndStep[1])
			}
		}

		rangeExpr := strings.TrimSpace(rangeAndStep[0])
		var start, end int
		if rangeExpr == "*" || rangeExpr == "?" {
			start = minVal
			end = maxVal
			if isDow {
				// * 对星期只需要 0..6，避免 7 与 0 重复步长
				end = 6
			}
		} else if strings.Contains(rangeExpr, "-") {
			bounds := strings.Split(rangeExpr, "-")
			if len(bounds) != 2 {
				return 0, false, fmt.Errorf("范围语法错误 %q", rangeExpr)
			}
			var err error
			start, err = parseAtom(bounds[0], minVal, maxVal, names)
			if err != nil {
				return 0, false, err
			}
			end, err = parseAtom(bounds[1], minVal, maxVal, names)
			if err != nil {
				return 0, false, err
			}
			if start > end {
				return 0, false, fmt.Errorf("起始值 %d 不能大于结束值 %d", start, end)
			}
		} else {
			val, err := parseAtom(rangeExpr, minVal, maxVal, names)
			if err != nil {
				return 0, false, err
			}
			start = val
			if len(rangeAndStep) == 2 {
				end = maxVal
				if isDow {
					end = 6
				}
			} else {
				end = val
			}
		}

		for v := start; v <= end; v += step {
			bitPos := v
			if isDow && bitPos == 7 {
				bitPos = 0
			}
			bits |= uint64(1) << uint(bitPos)
		}
	}

	if bits == 0 {
		return 0, false, fmt.Errorf("未匹配任何值 %q", expr)
	}
	return bits, isWild, nil
}

func parseAtom(raw string, minVal, maxVal int, names map[string]int) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("数值为空")
	}
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("无效数值 %q", s)
	}
	if v < minVal || v > maxVal {
		return 0, fmt.Errorf("数值 %d 超出范围 [%d, %d]", v, minVal, maxVal)
	}
	return v, nil
}
