// Package web 是 PolarisX 的内嵌 Web 面板：单文件 HTML + 只读 JSON API，
// 并把 /sub/ 直接复用订阅服务（一个进程同时对外提供面板与订阅）。
//
// 刻意只读：改节点/用户/额度仍走 CLI 与 TUI，Web 端只做「看」与「复制订阅地址」，
// 这样默认绑 127.0.0.1 也不怕被人改配置。要放开到公网时自己接反代并加鉴权。
package web

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PPX-LuBing/polarisx/internal/panel"
	"github.com/PPX-LuBing/polarisx/internal/sub"
)

//go:embed index.html
var indexHTML []byte

// Server 同时服务面板页面、JSON API 与订阅路径。
type Server struct {
	Store *panel.Store
	Sub   *sub.Server
	// Probe 可注入（测试里别真起 xray 子进程）
	Probe func(panel.Settings, time.Duration) bool
}

type settingsView struct {
	ServerHost string `json:"server_host"`
	ServerCC   string `json:"server_cc"`
	ServerASN  string `json:"server_asn"`
	ClashRules string `json:"clash_rules"`
	Family     string `json:"outbound_domain_strategy"`
}

type nodeView struct {
	Tag       string   `json:"tag"`
	Remark    string   `json:"remark"`
	Proto     string   `json:"proto"`
	Net       string   `json:"net"`
	Security  string   `json:"security"`
	Port      int      `json:"port"`
	EntryHost string   `json:"entry_host"`
	EntryPort int      `json:"entry_port"`
	Enabled   bool     `json:"enabled"`
	Problems  []string `json:"problems"`
}

type userView struct {
	Name           string `json:"name"`
	UsedBytes      int64  `json:"used_bytes"`
	UsedUpBytes    int64  `json:"used_up_bytes"`
	UsedDownBytes  int64  `json:"used_down_bytes"`
	QuotaBytes     int64  `json:"quota_bytes"`
	ExpireAt       string `json:"expire_at"`
	Enable         bool   `json:"enable"`
	DisabledReason string `json:"disabled_reason"`
	Nodes          int    `json:"nodes"`
}

type subView struct {
	User   string `json:"user"`
	Clash  string `json:"clash"`
	Sing   string `json:"singbox"`
	Plain  string `json:"plain"`
	Base64 string `json:"base64"`
}

type overview struct {
	Version    string       `json:"version"`
	Home       string       `json:"home"`
	XrayOnline bool         `json:"xray_online"`
	SubBase    string       `json:"sub_base"`
	Settings   settingsView `json:"settings"`
	Nodes      []nodeView   `json:"nodes"`
	Users      []userView   `json:"users"`
	Subs       []subView    `json:"subscriptions"`
}

// SubURLFor 拼订阅地址（suffix 形如 "" / "/clash" / "/singbox" / "?plain=1"）。
func SubURLFor(st panel.Settings, token, suffix string) string {
	return panel.SubBaseFor(st) + "/sub/" + token + suffix
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The panel is frequently exposed behind a reverse proxy. These headers are
	// safe defaults for the browser UI and do not rely on forwarded headers.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
	// /sub/ 交给订阅服务（同一进程、同一端口）
	if strings.HasPrefix(r.URL.Path, "/sub/") {
		if s.Sub == nil {
			http.Error(w, "subscription service disabled", http.StatusServiceUnavailable)
			return
		}
		s.Sub.ServeHTTP(w, r)
		return
	}
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "", "/index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(indexHTML)
	case "/api/overview":
		s.serveOverview(w)
	case "/api/traffic":
		s.serveTraffic(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// serveTraffic 返回按小时分桶的流量序列（Web 图表用）。
func (s *Server) serveTraffic(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			hours = n
		}
	}
	if hours > 24*panel.HistoryRetentionDays {
		hours = 24 * panel.HistoryRetentionDays
	}
	series, err := s.Store.TrafficSeries(time.Now(), hours)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	up, down := panel.TrafficTotal(series)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"hours": hours, "series": series,
		"up": up, "down": down, "total": up + down,
	})
}

func (s *Server) serveOverview(w http.ResponseWriter) {
	st, err := panel.LoadSettings()
	if err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	nodes, err := s.Store.Nodes(false)
	if err != nil {
		http.Error(w, "nodes: "+err.Error(), http.StatusInternalServerError)
		return
	}
	users, err := s.Store.Users()
	if err != nil {
		http.Error(w, "users: "+err.Error(), http.StatusInternalServerError)
		return
	}
	probe := s.Probe
	if probe == nil {
		probe = panel.APIAvailable
	}
	doc := overview{
		Version:    panel.Version,
		Home:       panel.Home(),
		XrayOnline: probe(st, 1500*time.Millisecond),
		SubBase:    panel.SubBaseFor(st),
		Settings: settingsView{st.ServerHost, st.ServerCC, st.ServerASN, st.ClashRules,
			st.OutboundDomainStrategy},
		Nodes: []nodeView{}, Users: []userView{}, Subs: []subView{},
	}
	for _, n := range nodes {
		probs := panel.NodeProblems(n)
		if probs == nil {
			probs = []string{}
		}
		doc.Nodes = append(doc.Nodes, nodeView{n.Tag, n.Remark, n.Proto, n.Net, n.Security,
			n.Port, n.EntryHost, n.EntryPort, n.Enabled != 0, probs})
	}
	for _, u := range users {
		un, _ := s.Store.NodesForUser(u, true)
		doc.Users = append(doc.Users, userView{u.Name, panel.UsedBytes(u), u.UsedUp, u.UsedDown,
			panel.QuotaBytes(u, st), u.ExpireAt, u.Enable != 0, u.DisabledReason, len(un)})
		doc.Subs = append(doc.Subs, subView{
			User:   u.Name,
			Clash:  SubURLFor(st, u.Token, "/clash"),
			Sing:   SubURLFor(st, u.Token, "/singbox"),
			Plain:  SubURLFor(st, u.Token, "?plain=1"),
			Base64: SubURLFor(st, u.Token, ""),
		})
	}
	b, err := panel.MarshalIndent(doc)
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b))
}

// New 打开数据库并装配 Web 面板（Sub 复用订阅服务，可为 nil）。
func New(dbPath string) (*Server, error) {
	subSrv, err := sub.New(dbPath)
	if err != nil {
		return nil, err
	}
	return &Server{Store: subSrv.Store, Sub: subSrv}, nil
}

// Close 关闭底层数据库。
func (s *Server) Close() error {
	if s.Sub != nil {
		return s.Sub.Close()
	}
	return s.Store.Close()
}

// ListenAndServe 在 addr 上提供服务。
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	return srv.ListenAndServe()
}
