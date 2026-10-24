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
