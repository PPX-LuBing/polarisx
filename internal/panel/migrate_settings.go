package panel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// 从旧版（Python ixpanel）的 settings.json 迁移到当前版本。
//
// 设计原则是"不猜"：
//   - 类型的唯一权威是 Settings 结构体本身（反射出 json tag -> Go 类型），
//     不靠人记、不靠猜；
//   - 旧文件里的键一个不落地搬，当前版本不认的键**列出来**，不静默丢掉；
//   - 类型对不上就报错，绝不静默变成零值；
//   - 路径指向旧数据目录的显式跳过并说明（有规则、可见，而不是挑着抄）。
//
// 之前的迁移就是"手挑几个键 + 猜类型"，结果漏了 30 个键、还把 string 猜成了 int，
// 直接把一台机器的 settings.json 写成解析不了。这个命令就是为了不再犯。

// settingsKinds 由结构体标签反射出来（key -> 字段类型），新增字段不用改这里。
var settingsKinds = func() map[string]reflect.Kind {
	m := map[string]reflect.Kind{}
	t := reflect.TypeOf(Settings{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			m[name] = f.Type.Kind()
		}
	}
	return m
}()

// SettingsMigration 是一次迁移的计划与结果。
type SettingsMigration struct {
	Changed  []SettingChange // 会改动的项
	Skipped  []string        // 旧文件里有、当前版本不认的键
	Warnings []string        // 需要注意的（例如路径指向旧数据目录）
	Errors   []string        // 类型对不上，无法迁移
}

// SettingChange 一项改动。
type SettingChange struct {
	Key    string
	Old    any // 当前值（迁移前）
	New    any // 迁移后的值
	Source string
}

// PlanSettingsMigration 读旧 settings.json，算出要合并进来的改动，但**不落盘**。
// 返回的 Settings 是合并后的结果，调用方确认后再 SaveSettings。
func PlanSettingsMigration(oldJSON []byte, cur Settings) (SettingsMigration, Settings, error) {
	return PlanSettingsMigrationExcept(oldJSON, cur, nil)
}

// PlanSettingsMigrationExcept 同 PlanSettingsMigration，但 keep 里的键保持当前值不动
// （给"我已经按新命名改过 sub_title"这类情况用，避免迁移把有意改动覆盖回去）。
func PlanSettingsMigrationExcept(oldJSON []byte, cur Settings, keep []string) (SettingsMigration, Settings, error) {
	var plan SettingsMigration
	out := cur

	var old map[string]any
	if err := json.Unmarshal(oldJSON, &old); err != nil {
		return plan, cur, fmt.Errorf("旧 settings.json 解析失败: %w", err)
	}
	if len(old) == 0 {
		return plan, cur, fmt.Errorf("旧 settings.json 是空的")
	}

	home := filepath.Clean(Home())
	curJSON, _ := json.Marshal(cur)
	var curMap map[string]any
	_ = json.Unmarshal(curJSON, &curMap)

	keys := make([]string, 0, len(old))
	for k := range old {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[strings.TrimSpace(k)] = true
	}
	for _, k := range keys {
		raw := old[k]
		if keepSet[k] {
			continue
		}
		kind, ok := settingsKinds[k]
		if !ok {
			plan.Skipped = append(plan.Skipped, k)
			continue
		}
		// 路径指向旧数据目录：显式跳过并说明（规则固定、结果可见，不是挑着抄）
		if s, isStr := raw.(string); isStr && strings.Contains(s, "/ixpanel") &&
			!strings.HasPrefix(filepath.Clean(s), home) {
			plan.Warnings = append(plan.Warnings,
				fmt.Sprintf("%s = %q 指向旧数据目录，已跳过（保持新值）", k, s))
			continue
		}
		nv, err := coerceSetting(k, raw, kind)
		if err != nil {
			plan.Errors = append(plan.Errors, err.Error())
			continue
		}
		// 比较统一成 JSON 形态：int 与 int64 在 any 里 DeepEqual 不相等，
		// 直接用 DeepEqual 会把"值没变"误报成"要改"（chain_port 0 -> 0 这种）。
		curNorm, _ := json.Marshal(curMap[k])
		newNorm, _ := json.Marshal(nv)
		if string(curNorm) == string(newNorm) {
			continue
		}
		if err := assignSetting(&out, k, nv); err != nil {
			plan.Errors = append(plan.Errors, err.Error())
			continue
		}
		plan.Changed = append(plan.Changed, SettingChange{Key: k, Old: curMap[k], New: nv, Source: "old"})
	}
	return plan, out, nil
}

// coerceSetting 按 Go 类型把旧值转过来；转不了就报错（不静默变零值）。
func coerceSetting(key string, raw any, kind reflect.Kind) (any, error) {
	bad := func() (any, error) {
		return nil, fmt.Errorf("%s: 值 %v（%T）不是 %s，拒绝迁移", key, raw, raw, kind)
	}
	switch kind {
	case reflect.String:
		switch v := raw.(type) {
		case string:
			return v, nil
		case nil:
			return "", nil
		default:
			return fmt.Sprintf("%v", v), nil // 数字/布尔当字符串是安全的
		}
	case reflect.Bool:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "1", "yes", "on":
				return true, nil
			case "false", "0", "no", "off", "":
				return false, nil
			}
			return bad()
		case float64: // JSON 数字
			return v != 0, nil
		}
		return bad()
	case reflect.Int, reflect.Int64:
		switch v := raw.(type) {
		case float64:
			return int64(v), nil
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return bad()
			}
			return n, nil
		}
		return bad()
	case reflect.Float64:
		switch v := raw.(type) {
		case float64:
			return v, nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return bad()
			}
			return f, nil
		}
		return bad()
	}
	return nil, fmt.Errorf("%s: 不支持的字段类型 %s", key, kind)
}

// assignSetting 把值写进结构体（按 json tag 找字段，靠反射，不写一堆 switch）。
func assignSetting(st *Settings, key string, val any) error {
	rv := reflect.ValueOf(st).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if strings.Split(f.Tag.Get("json"), ",")[0] != key {
			continue
		}
		fv := rv.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(val.(string))
		case reflect.Bool:
			fv.SetBool(val.(bool))
		case reflect.Int, reflect.Int64:
			fv.SetInt(val.(int64))
		case reflect.Float64:
			fv.SetFloat(val.(float64))
		default:
			return fmt.Errorf("%s: 不支持写入 %s", key, fv.Kind())
		}
		return nil
	}
	return fmt.Errorf("%s: Settings 里没有这个字段", key)
}

// MigrateSettingsFile 读一个旧 settings.json 并合并到当前设置；dryRun=true 只出计划。
func MigrateSettingsFile(path string, dryRun bool, keep []string) (SettingsMigration, Settings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SettingsMigration{}, Settings{}, err
	}
	cur, err := LoadSettings()
	if err != nil {
		return SettingsMigration{}, Settings{}, err
	}
	plan, out, err := PlanSettingsMigrationExcept(raw, cur, keep)
	if err != nil || dryRun || len(plan.Errors) > 0 {
		return plan, out, err
	}
	if len(plan.Changed) == 0 {
		return plan, out, nil
	}
	return plan, out, SaveSettings(out)
}
