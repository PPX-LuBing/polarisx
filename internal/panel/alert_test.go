package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 告警消息要能被 Slack/Discord 那类"贴 JSON 就发"的 webhook 直接用，
// 所以必须带 text；同时保留结构化 events 给自建端点。
func TestSendAlertPayload(t *testing.T) {
	var got []byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("应为 POST，实际 %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("Content-Type 不对: %s", ct)
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = b
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer srv.Close()

	st := Defaults()
	st.AlertWebhook = srv.URL
	st.ServerHost = "203.0.113.9"
	evs := []AlertEvent{
		{Kind: "warn", User: "alice", Message: "已用 85.0%", Used: 85 << 30, Quota: 100 << 30},
		{Kind: "disabled", User: "bob", Message: "流量超额 (1 GB / 1 GB)"},
	}
	if err := SendAlert(st, evs, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	body := got
	mu.Unlock()
	var parsed struct {
		Text   string       `json:"text"`
		Events []AlertEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("不是合法 JSON: %v\n%s", err, body)
	}
	if !strings.Contains(parsed.Text, "203.0.113.9") || !strings.Contains(parsed.Text, "alice") ||
		!strings.Contains(parsed.Text, "bob") {
		t.Errorf("text 缺内容: %s", parsed.Text)
	}
	if !strings.Contains(parsed.Text, "85.00 GB / 100.00 GB") {
		t.Errorf("text 应带上用量: %s", parsed.Text)
	}
	if len(parsed.Events) != 2 || parsed.Events[0].Kind != "warn" {
		t.Errorf("events 结构不对: %+v", parsed.Events)
	}

	t.Run("没配地址就什么都不做", func(t *testing.T) {
		st2 := st
		st2.AlertWebhook = ""
		if err := SendAlert(st2, evs, time.Second); err != nil {
			t.Errorf("没配地址不该报错: %v", err)
		}
		if err := SendAlert(st2, nil, time.Second); err != nil {
			t.Errorf("没有事件不该报错: %v", err)
		}
	})
	t.Run("地址不合法要报错", func(t *testing.T) {
		st3 := st
		st3.AlertWebhook = "hooks.example.com/xxx"
		if err := SendAlert(st3, evs, time.Second); err == nil {
			t.Error("缺 http:// 前缀应报错（否则会静默失败）")
		}
	})
	t.Run("服务端 5xx 要报错", func(t *testing.T) {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
		defer bad.Close()
		st4 := st
		st4.AlertWebhook = bad.URL
		if err := SendAlert(st4, evs, time.Second); err == nil {
			t.Error("5xx 应报错")
		}
	})
}

// 采集触发告警：过 80% 会发一条、被停用也会发，且**合并成一次请求**（不刷屏）。
func TestCollectSendsAlert(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var mu sync.Mutex
	calls, lastBody := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		lastBody = string(b)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	st := Defaults()
	st.AlertWebhook = srv.URL
	st.ServerHost = "203.0.113.9"
	if _, err := store.CreateUser("alice", 1, "", "", nil); err != nil { // 1 GB 额度
		t.Fatal(err)
	}
	if _, err := store.CreateUser("bob", 100, "", "", nil); err != nil {
		t.Fatal(err)
	}

	// alice 用掉 90% → 预警；bob 用掉 200% → 停用
	stats := map[string]int64{
		"user>>>alice>>>traffic>>>uplink":   900 << 20,
		"user>>>alice>>>traffic>>>downlink": 0,
		"user>>>bob>>>traffic>>>uplink":     200 << 30,
		"user>>>bob>>>traffic>>>downlink":   0,
	}
	rep, err := store.ApplyStats(stats, st, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warned) != 1 || len(rep.Disabled) != 1 {
		t.Fatalf("夹具前提不对: warned=%d disabled=%d", len(rep.Warned), len(rep.Disabled))
	}
	// ApplyStats 本身不发；Collect 才发。这里直接调内部组装 + 发送，等价路径
	evs := alertEventsFromReport(rep, st, time.Now())
	if err := SendAlert(st, evs, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("应合并成 1 次请求，实际 %d 次", calls)
	}
	if !strings.Contains(lastBody, "alice") || !strings.Contains(lastBody, "bob") {
		t.Errorf("一条消息里应包含两个用户: %s", lastBody)
	}
	// 没有事件时不该发请求
	before := calls
	if err := SendAlert(st, alertEventsFromReport(Report{}, st, time.Now()), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Error("没有事件时不该发请求")
	}
}
