package panel

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"time"
)

// ReconcileConfig 把「用户/节点状态」落到 config.json（不做重启，运行时由 adu/rmu 负责）。
// 没有这一步的话：超额停用的用户虽然被摘掉了，但配置文件里还留着，xray 一重启就会放回来。
// 返回是否发生了重写。
func (s *Store) ReconcileConfig(st Settings) (bool, error) {
	desired, err := s.BuildConfig(st)
	if err != nil {
		return false, err
	}
	same, err := configSameAsDisk(st, desired)
	if err != nil {
		return false, err
	}
	if same {
		return false, nil
	}
	verify := st.VerifyConfig
	if err := WriteConfig(st, desired, "", verify); err != nil {
		return false, err
	}
	return true, nil
}

func configSameAsDisk(st Settings, desired map[string]any) (bool, error) {
	b, err := os.ReadFile(st.XrayConfig)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var disk any
	if err := json.Unmarshal(b, &disk); err != nil {
		return false, nil // 磁盘上的配置坏了，直接重写
	}
	want, err := toAny(desired)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(disk, want), nil
}

func toAny(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Collect 拉一次统计 → 累加 → 每月重置 → 额度/到期检查 → 超额停用 → 配置对账。
// 与 Python 版 collect 的行为一致（报告字段名不同，语义相同）。
func (s *Store) Collect(st Settings, now time.Time, kick bool) (Report, error) {
	stats, err := FetchStats(st, true)
	if err != nil {
		return Report{}, err
	}
	rep, err := s.ApplyStats(stats, st, now, kick)
	if err != nil {
		return rep, err
	}
	if _, err := s.ReconcileConfig(st); err != nil {
		return rep, err
	}
	return rep, nil
}

// CompatIssue 是一条兼容性检查结果。
type CompatIssue struct {
	Level   string // ok / warn / bad
	Message string
}

// CheckCompat 检查现有数据目录能否被 PolarisX 直接接管（只读，不改动任何东西）。
func CheckCompat(st Settings) []CompatIssue {
	var out []CompatIssue
	add := func(level, format string, args ...any) {
		out = append(out, CompatIssue{level, fmt.Sprintf(format, args...)})
	}

	if _, err := os.Stat(DBPath()); err != nil {
		add("bad", "找不到数据库 %s（Python 版没装过？先跑一次 ixpanel init）", DBPath())
		return out
	}
	store, err := Open(DBPath())
	if err != nil {
		add("bad", "打不开数据库: %v", err)
		return out
	}
	defer store.Close()

	nodes, err := store.Nodes(false)
	if err != nil {
		add("bad", "读节点失败: %v", err)
	} else {
		add("ok", "节点 %d 个", len(nodes))
	}
	users, err := store.Users()
	if err != nil {
		add("bad", "读用户失败: %v", err)
	} else {
		add("ok", "用户 %d 个", len(users))
	}
	// 关键列是否齐全（老库缺列会被 Open 自动补上，这里只提示）
	var missing []string
	for _, c := range []struct{ table, col string }{
		{"nodes", "hy2_up"}, {"nodes", "hy2_down"}, {"nodes", "method"},
		{"users", "last_reset"}, {"users", "disabled_reason"},
	} {
		ok, err := columnExists(store, c.table, c.col)
		if err == nil && !ok {
			missing = append(missing, c.table+"."+c.col)
		}
	}
	if len(missing) > 0 {
		add("warn", "老库缺列（接管时会自动补上）: %v", missing)
	} else {
		add("ok", "数据库结构完整")
	}

	if b := FindXray(st); b != "" {
		add("ok", "xray: %s", b)
	} else {
		add("warn", "找不到 xray 二进制（设置 xray_bin）")
	}
	if _, err := os.Stat(st.XrayConfig); err == nil {
		add("ok", "xray 配置: %s", st.XrayConfig)
	} else {
		add("warn", "还没有 xray 配置（接管后跑一次 apply）")
	}
	if st.SubBase == "" {
		add("warn", "没配 sub_base，订阅地址会回落到 %s", st.SubListen)
	} else {
		add("ok", "订阅地址: %s", st.SubBase)
	}
	return out
}

func columnExists(s *Store, table, col string) (bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}
