package panel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GIB 1 GiB；traffic_unit_gib=false 时按 1000^3 算。
const GIB = 1024 * 1024 * 1024

// HumanBytes 人类可读的字节数（与 Python 版格式一致）。
func HumanBytes(n int64) string {
	f := float64(n)
	for _, unit := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || unit == "TB" {
			if unit == "B" {
				return fmt.Sprintf("%d B", int64(f))
			}
			return fmt.Sprintf("%.2f %s", f, unit)
		}
		f /= 1024
	}
	return ""
}

// HumanGB 保留两位的 GB 显示。
func HumanGB(n float64) string { return fmt.Sprintf("%.2f GB", n) }

// ParseDate 解析 YYYY-MM-DD / YYYY/MM/DD / YYYY-MM-DD HH:MM:SS。
func ParseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02", "2006/01/02", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("日期格式无法识别: %s (用 YYYY-MM-DD)", s)
}

// QuotaBytes 用户额度换算成字节；0 或负数 = 不限。
func QuotaBytes(u User, st Settings) int64 {
	if u.QuotaGB <= 0 {
		return 0
	}
	unit := float64(1000 * 1000 * 1000)
	if st.TrafficUnitGiB {
		unit = GIB
	}
	return int64(u.QuotaGB * unit)
}

// UsedBytes 已用流量（上行 + 下行，双向计费）。
func UsedBytes(u User) int64 { return u.UsedUp + u.UsedDown }

// ResetDayOfMonth 每月重置日：1-28 有效（29-31 会漏月，当没配），0 = 不自动重置。
func ResetDayOfMonth(st Settings) int {
	d := st.QuotaResetDay
	if d >= 1 && d <= 28 {
		return d
	}
	return 0
}

// LastResetDue 今天（含）之前最近一次该重置的日期；没配重置日返回零值。
func LastResetDue(today time.Time, day int) time.Time {
	if day == 0 {
		return time.Time{}
	}
	thisMonth := time.Date(today.Year(), today.Month(), day, 0, 0, 0, 0, today.Location())
	if !thisMonth.After(today) {
		return thisMonth
	}
	if today.Month() == time.January {
		return time.Date(today.Year()-1, time.December, day, 0, 0, 0, 0, today.Location())
	}
	return time.Date(today.Year(), today.Month()-1, day, 0, 0, 0, 0, today.Location())
}

func dateOrNone(s string) (time.Time, bool) {
	if len(s) < 10 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", s[:10], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ResetUsage 清零用量 + 清 raw_stats + 记事件。
func (s *Store) ResetUsage(u User) error {
	if _, err := s.db.Exec("UPDATE users SET used_up=0, used_down=0 WHERE id=?", u.ID); err != nil {
		return err
	}
	if _, err := s.db.Exec("DELETE FROM raw_stats WHERE email=?", u.Name); err != nil {
		return err
	}
	return s.Event("重置流量统计", "info", u.Name)
}

// EnableUser 启用用户（每月重置后自动恢复被超额停用的）。
func (s *Store) EnableUser(u User) error {
	if _, err := s.db.Exec("UPDATE users SET enable=1, disabled_reason='' WHERE id=?", u.ID); err != nil {
		return err
	}
	return s.Event("启用用户", "info", u.Name)
}

// Event 写一条事件（运维日志）。
func (s *Store) Event(message, level, userName string) error {
	_, err := s.db.Exec(
		"INSERT INTO events(ts, level, user_name, message) VALUES (?,?,?,?)",
		time.Now().Format("2006-01-02 15:04:05"), level, userName, message)
	return err
}

// AutoResetMonthly 把跨过重置日的用户清零（含上个月被超额停用的：新周期自动恢复）。
// 幂等：同一周期内跑多少次都只清零一次（靠 last_reset 记账）。
// today 由调用方传入，便于测试与 Python 版对齐。
func (s *Store) AutoResetMonthly(st Settings, today time.Time) (int, error) {
	day := ResetDayOfMonth(st)
	due := LastResetDue(today, day)
	if due.IsZero() {
		return 0, nil
	}
	users, err := s.Users()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users {
		if last, ok := dateOrNone(u.LastReset); ok && !last.Before(due) {
			continue
		}
		if err := s.ResetUsage(u); err != nil {
			return n, err
		}
		if _, err := s.db.Exec("UPDATE users SET last_reset=? WHERE id=?",
			due.Format("2006-01-02"), u.ID); err != nil {
			return n, err
		}
		if u.Enable == 0 {
			if err := s.EnableUser(u); err != nil {
				return n, err
			}
		}
		n++
	}
	return n, nil
}

var statNameRe = regexp.MustCompile(`name:\s*"([^"]+)"`)
var statValueRe = regexp.MustCompile(`value:\s*(\d+)`)

// ParseStats 解析 `xray api statsquery` 输出 -> {name: value}。
// 兼容两种格式：新版 JSON {"stat":[{"name":..,"value":..}]}（值为 0 的只给 name），
// 老版 protobuf 文本 `stat: < name: "..." value: 123 >`。
func ParseStats(text string) map[string]int64 {
	out := map[string]int64{}
	text = strings.TrimSpace(text)
	if text == "" {
		return out
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		var data any
		if err := json.Unmarshal([]byte(text), &data); err != nil {
			data = nil
		}
		if m, ok := data.(map[string]any); ok {
			if items, ok := m["stat"].([]any); ok {
				for _, it := range items {
					if d, ok := it.(map[string]any); ok {
						if name, _ := d["name"].(string); name != "" {
							out[name] = toInt64(d["value"])
						}
					}
				}
				return out
			}
		}
		if arr, ok := data.([]any); ok {
			for _, it := range arr {
				if d, ok := it.(map[string]any); ok {
					if name, _ := d["name"].(string); name != "" {
						out[name] = toInt64(d["value"])
					}
				}
			}
			return out
		}
	}
	// 老格式回退
	for _, m := range statNameRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[m[2]:m[3]]
		tail := text[m[1]:min(m[1]+160, len(text))]
		vm := statValueRe.FindStringSubmatch(tail)
		if vm == nil { // 值在前的情况
			head := text[max(0, m[0]-160):m[0]]
			vm = statValueRe.FindStringSubmatch(head)
		}
		if vm != nil {
			v, _ := strconv.ParseInt(vm[1], 10, 64)
			out[name] = v
		} else {
			out[name] = 0
		}
	}
	return out
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// UserinfoHeader 订阅里的 Subscription-Userinfo 头。
func UserinfoHeader(u User, st Settings) string {
	q := QuotaBytes(u, st)
	expire := int64(0)
	if t, ok := dateOrNone(u.ExpireAt); ok {
		// 当天 23:59:59 过期
		expire = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.Local).Unix()
	}
	return fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", u.UsedUp, u.UsedDown, q, expire)
}

// Report 是一次采集的结果（对应 Python 版 collect 的返回）。
type Report struct {
	AddedUp   int64
	AddedDown int64
	Disabled  []DisabledUser
	Warned    []WarnedUser
}

// DisabledUser 被停用的用户。
type DisabledUser struct {
	Name   string
	Reason string
}

// WarnedUser 用量超过 80% 的用户。
type WarnedUser struct {
	Name  string
	Used  int64
	Quota int64
	Pct   float64
}

// ApplyStats 把 xray 的增量统计累加到用户上，并做每月重置 + 额度/到期检查。
// 这是 collect 的核心（与 xray API 解耦，可离线测试）。
func (s *Store) ApplyStats(stats map[string]int64, st Settings, today time.Time, kick bool) (Report, error) {
	rep := Report{}
	upBy := map[string]int64{}
	downBy := map[string]int64{}
	for name, val := range stats {
		parts := strings.Split(name, ">>>")
		if len(parts) != 4 || parts[0] != "user" {
			continue
		}
		switch parts[3] {
		case "uplink":
			upBy[parts[1]] += val
		case "downlink":
			downBy[parts[1]] += val
		}
	}
	emails := map[string]bool{}
	for e := range upBy {
		emails[e] = true
	}
	for e := range downBy {
		emails[e] = true
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	for _, email := range sortedKeys(emails) {
		du, dd := upBy[email], downBy[email]
		if _, err := s.db.Exec(`INSERT INTO raw_stats(email, up, down, updated) VALUES (?,?,?,?)
			ON CONFLICT(email) DO UPDATE SET up=excluded.up, down=excluded.down, updated=excluded.updated`,
			email, du, dd, now); err != nil {
			return rep, err
		}
		u, err := s.UserByName(email)
		if err != nil {
			continue // 已删除用户残留的统计，忽略
		}
		if _, err := s.db.Exec("UPDATE users SET used_up=used_up+?, used_down=used_down+? WHERE id=?",
			du, dd, u.ID); err != nil {
			return rep, err
		}
		rep.AddedUp += du
		rep.AddedDown += dd
	}

	// 每月自动重置要放在额度检查之前：重置完就不该再按上个月用量停用
	if _, err := s.AutoResetMonthly(st, today); err != nil {
		return rep, err
	}

	users, err := s.Users()
	if err != nil {
		return rep, err
	}
	for _, u := range users {
		if u.Enable == 0 {
			continue
		}
		reason := ""
		q := QuotaBytes(u, st)
		used := UsedBytes(u)
		switch {
		case q > 0 && used >= q:
			reason = fmt.Sprintf("流量超额 (%s / %s)", HumanBytes(used), HumanBytes(q))
		case u.ExpireAt != "":
			if d, ok := dateOrNone(u.ExpireAt); ok && d.Before(today) {
				reason = "已过期 (" + u.ExpireAt + ")"
			}
		}
		if reason != "" {
			if err := s.DisableUser(u, reason, kick); err != nil {
				return rep, err
			}
			rep.Disabled = append(rep.Disabled, DisabledUser{u.Name, reason})
		} else if q > 0 && float64(used) >= float64(q)*0.8 {
			rep.Warned = append(rep.Warned, WarnedUser{u.Name, used, q,
				float64(used) / float64(q) * 100})
		}
	}
	return rep, nil
}

// DisableUser 停用用户并记录原因（kick=true 时同时踢掉在线连接）。
func (s *Store) DisableUser(u User, reason string, kick bool) error {
	if _, err := s.db.Exec("UPDATE users SET enable=0, disabled_reason=? WHERE id=?",
		reason, u.ID); err != nil {
		return err
	}
	if err := s.Event("停用用户: "+reason, "warn", u.Name); err != nil {
		return err
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ { // 插入排序，键通常很少
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
