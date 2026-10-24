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

// configStructureSame 比较磁盘上的配置与目标配置，**忽略 inbounds 里可热更新的 clients**。
// 只有"用户增删"能靠 xray API 热更新；inbound/outbound/routing 的结构变化必须重启
// —— xray 的 API 改不了 routing 规则，改了分流不重启就一直是旧路由（真机上踩到过：
// 配好 AI 走中转、apply 报"已生效"，实际四个域名全走本机，重启后才对）。
func configStructureSame(st Settings, desired map[string]any) bool {
	b, err := os.ReadFile(st.XrayConfig)
	if err != nil {
		return false
	}
	var disk map[string]any
	if json.Unmarshal(b, &disk) != nil {
		return false
	}
	// 先做 JSON 往返（= 深拷贝）再屏蔽 clients：maskClients 会改 map，
	// 直接改调用方传进来的 desired，会让它随后写盘时把 clients 写成 null——
	// 也就是"用户被整个抹掉"，节点立刻认证失败（这个 bug 我犯过，靠自检抓到）。
	a, err1 := toAny(disk)
	c, err2 := toAny(desired)
	if err1 != nil || err2 != nil {
		return false
	}
	am, ok1 := a.(map[string]any)
	cm, ok2 := c.(map[string]any)
	if !ok1 || !ok2 {
		return false
	}
	maskClients(am)
	maskClients(cm)
	return reflect.DeepEqual(am, cm)
}

// maskClients 把 inbounds[].settings.clients 清空（这部分是 API 能热更新的）。
func maskClients(cfg map[string]any) {
	inb, _ := cfg["inbounds"].([]any)
	for _, x := range inb {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		st, ok := m["settings"].(map[string]any)
		if !ok {
			continue
		}
		if _, has := st["clients"]; has {
			st["clients"] = nil
		}
	}
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
	// 用量告警（配了 webhook 才发）。发失败不影响采集结果——配置已经改好了，
	// 只是消息没送出去，记一条事件让人能在 `polarisx events` 里看到。
	if evs := alertEventsFromReport(rep, st, now); len(evs) > 0 {
		if err := SendAlert(st, evs, 8*timeSecond); err != nil {
			_ = s.Event("用量告警发送失败: "+err.Error(), "warn", "")
		} else {
			_ = s.Event(fmt.Sprintf("已发送用量告警 %d 条", len(evs)), "info", "")
		}
	}
	return rep, nil
}
