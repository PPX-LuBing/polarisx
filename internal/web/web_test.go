package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PPX-LuBing/polarisx/internal/panel"
	"github.com/PPX-LuBing/polarisx/internal/sub"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	root := filepath.Join("..", "..", "testdata")
	src, err := os.ReadFile(filepath.Join(root, "fixture", "panel.db"))
	if err != nil {
		t.Skipf("没有夹具（先跑 python3 tools/mkfixture.py）: %v", err)
	}
	fixtureAbs, _ := filepath.Abs(filepath.Join(root, "fixture"))
	t.Setenv("POLARISX_HOME", fixtureAbs)
	db := filepath.Join(t.TempDir(), "panel.db")
	if err := os.WriteFile(db, src, 0o600); err != nil {
		t.Fatal(err)
	}
	subSrv, err := sub.New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { subSrv.Close() })
	return &Server{Store: subSrv.Store, Sub: subSrv,
		Probe: func(panel.Settings, time.Duration) bool { return true }}
}

func TestIndexServed(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"PolarisX", "流量账本", "订阅地址", "api/overview",
		"prefers-color-scheme", "80%"} {
		if !strings.Contains(body, want) {
			t.Errorf("页面缺少 %q", want)
		}
	}
}

func TestOverviewAPI(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/overview", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Version    string `json:"version"`
		XrayOnline bool   `json:"xray_online"`
		SubBase    string `json:"sub_base"`
		Settings   struct {
			ClashRules string `json:"clash_rules"`
		} `json:"settings"`
		Nodes []struct {
			Tag      string   `json:"tag"`
			Problems []string `json:"problems"`
		} `json:"nodes"`
		Users []struct {
			Name       string `json:"name"`
			Enable     bool   `json:"enable"`
			QuotaBytes int64  `json:"quota_bytes"`
		} `json:"users"`
		Subs []struct {
			User  string `json:"user"`
			Clash string `json:"clash"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Version != panel.Version {
		t.Errorf("版本 %q", d.Version)
	}
	if !d.XrayOnline {
		t.Error("探活桩应返回在线")
	}
	if len(d.Nodes) != 10 || len(d.Users) != 3 {
		t.Errorf("节点 %d 用户 %d，期望 10 / 3", len(d.Nodes), len(d.Users))
	}
	if len(d.Subs) != 3 {
		t.Fatalf("订阅条目 %d，期望 3", len(d.Subs))
	}
	if !strings.HasSuffix(d.Subs[0].Clash, "/sub/tok-alice/clash") {
		t.Errorf("订阅地址 = %q", d.Subs[0].Clash)
	}
	// 坏节点要带上问题（前端会显示）
	bad := ""
	for _, n := range d.Nodes {
		if n.Tag == "bad-reality" {
			bad = strings.Join(n.Problems, ";")
		}
	}
	if !strings.Contains(bad, "reality") {
		t.Errorf("bad-reality 应带问题描述，实际 %q", bad)
	}
	// 停用用户 enable=false
	carol := false
	for _, u := range d.Users {
		if u.Name == "carol" && !u.Enable {
			carol = true
		}
	}
	if !carol {
		t.Error("carol 应为停用状态")
	}
	if d.Settings.ClashRules == "" {
		t.Error("应带分流预设")
	}
}

// /sub/ 必须复用订阅服务（一个进程同时对外提供面板与订阅）。
func TestSubDelegation(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/sub/tok-alice/clash", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/yaml") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "proxy-groups:") {
		t.Error("应返回 Clash 订阅")
	}
}

func TestUnknownPath(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 {
		t.Errorf("状态码 %d，期望 404", rec.Code)
	}
}

func TestSubURLHelpers(t *testing.T) {
	st := panel.Defaults()
	st.SubBase = "http://example.com:61887/"
	st.SubListen = "127.0.0.1:8080"
	if got := panel.SubBaseFor(st); got != "http://example.com:61887" {
		t.Errorf("SubBaseFor = %q", got)
	}
	if got := SubURLFor(st, "tok", "/clash"); got != "http://example.com:61887/sub/tok/clash" {
		t.Errorf("SubURLFor = %q", got)
	}
	st.SubBase = ""
	if got := panel.SubBaseFor(st); got != "http://127.0.0.1:8080" {
		t.Errorf("没配 sub_base 应回落到监听地址，实际 %q", got)
	}
}

// 流量曲线接口：图表全靠它，桶数、合计、参数边界都要对。
func TestTrafficAPI(t *testing.T) {
	s := newTestServer(t)
	// 造两小时的历史
	base := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	if err := s.Store.RecordTraffic(base, map[string][2]int64{"alice": {1000, 2000}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.RecordTraffic(base.Add(time.Hour), map[string][2]int64{"alice": {3000, 4000}}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/traffic?hours=6", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Hours  int                  `json:"hours"`
		Series []panel.TrafficPoint `json:"series"`
		Up     int64                `json:"up"`
		Down   int64                `json:"down"`
		Total  int64                `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	if got.Hours != 6 || len(got.Series) != 6 {
		t.Errorf("应有 6 个桶，实际 hours=%d len=%d", got.Hours, len(got.Series))
	}
	if got.Up != 4000 || got.Down != 6000 || got.Total != 10000 {
		t.Errorf("合计不对: ↑%d ↓%d 共 %d", got.Up, got.Down, got.Total)
	}
	// 最后一个桶必须是当前小时（图表要能看到"现在"）
	last := got.Series[len(got.Series)-1]
	if !strings.HasPrefix(last.Label, time.Now().Format("01-02 15")) {
		t.Errorf("最后一个桶应是当前小时，实际 %s", last.Label)
	}
	// 参数边界：非法值回落 24，超大值被夹到保留期
	for _, c := range []struct {
		q    string
		want int
	}{
		{"?hours=abc", 24}, {"?hours=0", 24}, {"?hours=-5", 24}, {"?hours=999999", 24 * panel.HistoryRetentionDays},
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/traffic"+c.q, nil))
		var v struct {
			Hours int `json:"hours"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		if v.Hours != c.want {
			t.Errorf("%s 应回落/夹到 %d，实际 %d", c.q, c.want, v.Hours)
		}
	}
}

// 面板 HTML 里要有图表代码（否则接口对了也没人画）。
func TestIndexHasChart(t *testing.T) {
	for _, want := range []string{"流量曲线", "api/traffic", "renderChart", "loadTraffic"} {
		if !strings.Contains(string(indexHTML), want) {
			t.Errorf("面板 HTML 缺少 %q", want)
		}
	}
}
