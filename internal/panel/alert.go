package panel

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AlertEvent 是一条告警（用量预警或用户被停用）。
type AlertEvent struct {
	Kind    string `json:"kind"` // warn | disabled
	User    string `json:"user"`
	Message string `json:"message"`
	Used    int64  `json:"used_bytes"`
	Quota   int64  `json:"quota_bytes"`
	At      string `json:"at"`
}

// AlertText 拼一条人能直接读的消息。
// 带 `text` 字段是为了 Slack / Discord / 飞书这类"贴一个 JSON 就发消息"的 webhook
// 不用额外适配；同时保留结构化的 events，自建端点可以按字段处理。
func AlertText(host string, events []AlertEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PolarisX 用量告警（%s）", host)
	for _, e := range events {
		b.WriteString("\n")
		switch e.Kind {
		case "disabled":
			fmt.Fprintf(&b, "⛔ %s 已停用：%s", e.User, e.Message)
		default:
			fmt.Fprintf(&b, "⚠️ %s：%s", e.User, e.Message)
		}
		if e.Quota > 0 {
			fmt.Fprintf(&b, "（%s / %s）", HumanBytes(e.Used), HumanBytes(e.Quota))
		}
	}
	return b.String()
}

// SendAlert POST 一条告警到 st.AlertWebhook。没配就什么都不做。
func SendAlert(st Settings, events []AlertEvent, timeout time.Duration) error {
	url := strings.TrimSpace(st.AlertWebhook)
	if url == "" || len(events) == 0 {
		return nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("alert_webhook 必须以 http:// 或 https:// 开头")
	}
	body, err := marshalNoEscape(map[string]any{
		"text":   AlertText(st.ServerHost, events),
		"events": events,
	}, "")
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: timeout}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回 %d", resp.StatusCode)
	}
	return nil
}

// alertEventsFromReport 把一次采集里的预警/停用转成告警事件。
func alertEventsFromReport(rep Report, st Settings, now time.Time) []AlertEvent {
	var out []AlertEvent
	for _, d := range rep.Disabled {
		out = append(out, AlertEvent{Kind: "disabled", User: d.Name, Message: d.Reason,
			At: now.Format("2006-01-02 15:04:05")})
	}
	for _, w := range rep.Warned {
		out = append(out, AlertEvent{Kind: "warn", User: w.Name,
			Message: fmt.Sprintf("已用 %.1f%%", w.Pct),
			Used:    w.Used, Quota: w.Quota, At: now.Format("2006-01-02 15:04:05")})
	}
	return out
}
