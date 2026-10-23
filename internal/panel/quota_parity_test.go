package panel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestQuotaParity 比对额度/重置/采集：纯函数直接比，状态型（重置、采集）在
// 夹具 DB 的副本上跑一遍再比用户表——这是阶段④的验收标准。
func TestQuotaParity(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	raw, err := os.ReadFile(filepath.Join(root, "parity", "quota.json"))
	if err != nil {
		t.Skipf("没有额度夹具（先跑 python3 tools/mkfixture.py）: %v", err)
	}
	var want struct {
		Today           string            `json:"today"`
		HumanBytes      map[string]string `json:"human_bytes"`
		QuotaBytes      map[string]int64  `json:"quota_bytes"`
		QuotaBytesSI    map[string]int64  `json:"quota_bytes_si"`
		ResetDay        map[string]int    `json:"reset_day"`
		LastResetDue    map[string]any    `json:"last_reset_due"`
		ParseStats      map[string]any    `json:"parse_stats"`
		Userinfo        map[string]string `json:"userinfo"`
		AutoResetCount  int               `json:"auto_reset_count"`
		AutoResetAgain  int               `json:"auto_reset_again"`
		AfterReset      []any             `json:"after_reset"`
		AfterResetAgain []any             `json:"after_reset_again"`
		CollectStats    string            `json:"collect_stats_json"`
		CollectReport   any               `json:"collect_report"`
		AfterCollect    []any             `json:"after_collect"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	today, err := time.ParseInLocation("2006-01-02", want.Today, time.Local)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("human-bytes", func(t *testing.T) {
		for k, exp := range want.HumanBytes {
			var n int64
			if _, err := fmtSscan(k, &n); err != nil {
				t.Fatal(err)
			}
			if got := HumanBytes(n); got != exp {
				t.Errorf("%s -> %q，期望 %q", k, got, exp)
			}
		}
	})

	t.Run("quota-bytes", func(t *testing.T) {
		st := Defaults()
		for k, exp := range want.QuotaBytes {
			var gb float64
			if _, err := fmtSscan(k, &gb); err != nil {
				t.Fatal(err)
			}
			if got := QuotaBytes(User{QuotaGB: gb}, st); got != exp {
				t.Errorf("GiB %s GB -> %d，期望 %d", k, got, exp)
			}
		}
		si := Defaults()
		si.TrafficUnitGiB = false
		for k, exp := range want.QuotaBytesSI {
			var gb float64
			if _, err := fmtSscan(k, &gb); err != nil {
				t.Fatal(err)
			}
			if got := QuotaBytes(User{QuotaGB: gb}, si); got != exp {
				t.Errorf("SI %s GB -> %d，期望 %d", k, got, exp)
			}
		}
	})

	t.Run("reset-day", func(t *testing.T) {
		for k, exp := range want.ResetDay {
			var v int
			if _, err := fmtSscan(k, &v); err != nil {
				t.Fatal(err)
			}
			st := Defaults()
			st.QuotaResetDay = v
			if got := ResetDayOfMonth(st); got != exp {
				t.Errorf("quota_reset_day=%s -> %d，期望 %d", k, got, exp)
			}
		}
	})

	t.Run("last-reset-due", func(t *testing.T) {
		for day, expAny := range want.LastResetDue {
			d, err := time.ParseInLocation("2006-01-02", day, time.Local)
			if err != nil {
				t.Fatal(err)
			}
			got := LastResetDue(d, 5)
			gotStr := ""
			if !got.IsZero() {
				gotStr = got.Format("2006-01-02")
			}
			exp, _ := expAny.(string)
			if gotStr != exp {
				t.Errorf("today=%s -> %q，期望 %q", day, gotStr, exp)
			}
		}
	})

	t.Run("parse-stats", func(t *testing.T) {
		for name, expAny := range want.ParseStats {
			var text string
			switch name {
			case "json-zero":
				text = `{"stat":[{"name":"user>>>alice>>>traffic>>>uplink","value":1234},{"name":"user>>>bob>>>traffic>>>downlink"}]}`
			case "json-array":
				text = `[{"name":"user>>>alice>>>traffic>>>downlink","value":7}]`
			case "proto-text":
				text = "stat: <\n  name: \"user>>>alice>>>traffic>>>uplink\"\n  value: 999\n>\nstat: <\n  name: \"user>>>bob>>>traffic>>>downlink\"\n  value: 5\n>\n"
			case "value-first":
				text = `stat: < value: 42 name: "user>>>x>>>traffic>>>uplink" >`
			case "empty":
				text = ""
			case "garbage":
				text = "not a stats output at all"
			}
			got := ParseStats(text)
			gotNorm := normalize(t, mapToAny(got))
			expNorm := normalize(t, expAny)
			if !jsonEqual(gotNorm, expNorm) {
				t.Errorf("%s:\n  go   : %v\n  python: %v", name, got, expAny)
			}
		}
	})

	// 状态型：在夹具 DB 副本上跑
	mk := func(t *testing.T) (*Store, string) {
		dir := t.TempDir()
		src, err := os.ReadFile(filepath.Join(root, "fixture-quota", "panel.db"))
		if err != nil {
			t.Skipf("没有额度夹具 DB: %v", err)
		}
		db := filepath.Join(dir, "panel.db")
		if err := os.WriteFile(db, src, 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := Open(db)
		if err != nil {
			t.Fatal(err)
		}
		return st, dir
	}

	t.Run("userinfo", func(t *testing.T) {
		store, _ := mk(t)
		defer store.Close()
		st := Defaults()
		for name, exp := range want.Userinfo {
			u, err := store.UserByName(name)
			if err != nil {
				t.Fatal(err)
			}
			if got := UserinfoHeader(u, st); got != exp {
				t.Errorf("%s:\n  go   : %s\n  python: %s", name, got, exp)
			}
		}
	})

	t.Run("auto-reset", func(t *testing.T) {
		store, _ := mk(t)
		defer store.Close()
		st := Defaults()
		st.QuotaResetDay = 5
		n, err := store.AutoResetMonthly(st, today)
		if err != nil {
			t.Fatal(err)
		}
		if n != want.AutoResetCount {
			t.Errorf("重置人数 %d，期望 %d", n, want.AutoResetCount)
		}
		if d := usersDiff(t, store, want.AfterReset); d != "" {
			t.Errorf("重置后用户表不一致: %s", d)
		}
		again, err := store.AutoResetMonthly(st, today)
		if err != nil {
			t.Fatal(err)
		}
		if again != want.AutoResetAgain {
			t.Errorf("第二次重置 %d 人，期望 %d（幂等）", again, want.AutoResetAgain)
		}
		if d := usersDiff(t, store, want.AfterResetAgain); d != "" {
			t.Errorf("第二次重置后不一致: %s", d)
		}
	})

	t.Run("collect", func(t *testing.T) {
		store, _ := mk(t)
		defer store.Close()
		st := Defaults()
		st.QuotaResetDay = 5
		stats := ParseStats(want.CollectStats)
		rep, err := store.ApplyStats(stats, st, today, false)
		if err != nil {
			t.Fatal(err)
		}
		gotRep := normalize(t, map[string]any{
			"added_up": rep.AddedUp, "added_down": rep.AddedDown,
			"disabled": disabledAny(rep), "warned": warnedAny(rep),
		})
		expRep := normalize(t, want.CollectReport)
		// Python 的 report 还带 errors 字段
		if m, ok := expRep.(map[string]any); ok {
			delete(m, "errors")
		}
		if !jsonEqual(gotRep, expRep) {
			t.Errorf("采集报告不一致:\n  go   : %s\n  python: %s", mustJSON(gotRep), mustJSON(expRep))
		}
		if d := usersDiff(t, store, want.AfterCollect); d != "" {
			t.Errorf("采集后用户表不一致: %s", d)
		}
	})
}

func disabledAny(rep Report) []any {
	out := []any{}
	for _, d := range rep.Disabled {
		out = append(out, map[string]any{"name": d.Name, "reason": d.Reason})
	}
	return out
}

func warnedAny(rep Report) []any {
	out := []any{}
	for _, w := range rep.Warned {
		out = append(out, map[string]any{"name": w.Name, "used": w.Used, "quota": w.Quota,
			"pct": w.Pct})
	}
	return out
}

func mapToAny(m map[string]int64) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// usersDiff 把 Go 侧用户表与 Python 期望逐字段比对。
func usersDiff(t *testing.T, store *Store, want []any) string {
	t.Helper()
	rows, err := store.DB().Query("SELECT id, name, uuid, password, token, quota_gb, used_up," +
		" used_down, expire_at, enable, note, disabled_reason, last_reset, created" +
		" FROM users ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []map[string]any
	for rows.Next() {
		var id int64
		var name, uuid, pw, token, expire, note, reason, lastReset, created string
		var quotaGB float64
		var up, down int64
		var enable int
		if err := rows.Scan(&id, &name, &uuid, &pw, &token, &quotaGB, &up, &down, &expire,
			&enable, &note, &reason, &lastReset, &created); err != nil {
			t.Fatal(err)
		}
		got = append(got, map[string]any{
			"id": id, "name": name, "uuid": uuid, "password": pw, "token": token,
			"quota_gb": quotaGB, "used_up": up, "used_down": down, "expire_at": expire,
			"enable": enable, "note": note, "disabled_reason": reason,
			"last_reset": lastReset, "created": created,
		})
	}
	g, w := normalize(t, got), normalize(t, want)
	if jsonEqual(g, w) {
		return ""
	}
	return firstDiff(t, g, w)
}

// fmtSscan 是测试用的小解析（strconv 的便捷包装）。
func fmtSscan(s string, v any) (int, error) {
	return fmt.Sscan(s, v)
}
