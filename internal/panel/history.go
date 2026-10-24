package panel

import (
	"fmt"
	"time"
)

// HistoryRetentionDays 历史保留天数。采集每分钟一次，留着不裁剪会一直涨；
// 30 天足够看出趋势，也够排查"哪天开始异常的"。
const HistoryRetentionDays = 30

// RecordTraffic 记一次采集增量（增量为 0 不记，省空间；图表里没数据的时段就是 0）。
func (s *Store) RecordTraffic(ts time.Time, deltas map[string][2]int64) error {
	if len(deltas) == 0 {
		return nil
	}
	stamp := ts.Format("2006-01-02 15:04:05")
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for email, d := range deltas {
		if d[0] == 0 && d[1] == 0 {
			continue
		}
		if _, err := tx.Exec(
			"INSERT INTO traffic_history(ts, email, up, down) VALUES (?,?,?,?)",
			stamp, email, d[0], d[1]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneTraffic 删掉过期的历史（每次采集顺手做，代价很小）。
func (s *Store) PruneTraffic(now time.Time) (int64, error) {
	cut := now.AddDate(0, 0, -HistoryRetentionDays).Format("2006-01-02 15:04:05")
	res, err := s.db.Exec("DELETE FROM traffic_history WHERE ts < ?", cut)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TrafficPoint 是一个时间桶的流量（字节）。
type TrafficPoint struct {
	Label string `json:"label"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
}

// TrafficSeries 按小时分桶返回最近 hours 小时的流量（含没有流量的空桶，图表才连得上）。
func (s *Store) TrafficSeries(now time.Time, hours int) ([]TrafficPoint, error) {
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*HistoryRetentionDays {
		hours = 24 * HistoryRetentionDays
	}
	// 序列要**包含当前这一小时**（图表得看到"现在"），所以从 hours-1 小时前算起
	start := now.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)
	rows, err := s.db.Query(
		`SELECT substr(ts, 1, 13) AS h, SUM(up), SUM(down) FROM traffic_history
		 WHERE ts >= ? GROUP BY h`, start.Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string][2]int64{}
	for rows.Next() {
		var h string
		var up, down int64
		if err := rows.Scan(&h, &up, &down); err != nil {
			return nil, err
		}
		got[h] = [2]int64{up, down}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]TrafficPoint, 0, hours)
	for i := 0; i < hours; i++ {
		t := start.Add(time.Duration(i) * time.Hour)
		key := t.Format("2006-01-02 15") // substr(ts,1,13) 的形状
		v := got[key]
		out = append(out, TrafficPoint{Label: t.Format("01-02 15:00"), Up: v[0], Down: v[1]})
	}
	return out, nil
}

// TrafficTotal 最近 hours 小时的总量（给 CLI 与 API 用）。
func TrafficTotal(series []TrafficPoint) (up, down int64) {
	for _, p := range series {
		up += p.Up
		down += p.Down
	}
	return
}

// Sparkline 把一串数字画成终端迷你图（用 8 级方块）。
func Sparkline(vals []int64) string {
	if len(vals) == 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var max int64
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	var out []rune
	for _, v := range vals {
		if max == 0 {
			out = append(out, blocks[0])
			continue
		}
		idx := int(v * int64(len(blocks)-1) / max)
		out = append(out, blocks[idx])
	}
	return string(out)
}

// TrafficSummary 一行摘要（CLI 用）。
func TrafficSummary(series []TrafficPoint) string {
	up, down := TrafficTotal(series)
	return fmt.Sprintf("↑%s ↓%s 共 %s", HumanBytes(up), HumanBytes(down), HumanBytes(up+down))
}
