// Package sub 是 PolarisX 的订阅 HTTP 服务。
package sub

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PPX-LuBing/polarisx/internal/panel"
)

// Server 是订阅服务。settings 每个请求重读（改订阅名/分流预设不用重启）。
type Server struct {
	Store *panel.Store
}

// New 打开数据库并返回服务实例。
func New(dbPath string) (*Server, error) {
	st, err := panel.Open(dbPath)
	if err != nil {
		return nil, err
	}
	return &Server{Store: st}, nil
}

// Close 关闭底层数据库。
func (s *Server) Close() error { return s.Store.Close() }

// ListenAndServe 以默认设置（明文 HTTP）在 addr 上提供服务（addr 形如 0.0.0.0:8080）。
// 需要 TLS 时用 Serve(settings, addr)。
func (s *Server) ListenAndServe(addr string) error {
	return s.Serve(panel.Defaults(), addr)
}

// infoDoc 的字段顺序与 Python 版一致（正文逐字节比对要用）。
type infoDoc struct {
	Name           string   `json:"name"`
	UsedBytes      int64    `json:"used_bytes"`
	UsedUpBytes    int64    `json:"used_up_bytes"`
	UsedDownBytes  int64    `json:"used_down_bytes"`
	UsedHuman      string   `json:"used_human"`
	UsedUpHuman    string   `json:"used_up_human"`
	UsedDownHuman  string   `json:"used_down_human"`
	QuotaBytes     int64    `json:"quota_bytes"`
	RemainingBytes *int64   `json:"remaining_bytes"`
	ExpireAt       string   `json:"expire_at"`
	Enable         bool     `json:"enable"`
	Nodes          []string `json:"nodes"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 每个请求重读设置：改完订阅名 / 地区 / 分流预设不用重启订阅服务
	st, err := panel.LoadSettings()
	if err != nil {
		writeText(w, 500, "error: "+err.Error()+"\n", nil)
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	switch path {
	case "/", "/index.html":
		writeText(w, 200, fmt.Sprintf("%s %s ok\n", panel.ProductName, panel.VersionString()), nil)
		return
	case "/health":
		writeText(w, 200, "ok\n", nil)
		return
	}
	if !strings.HasPrefix(path, "/sub/") {
		writeText(w, 404, "not found\n", nil)
		return
	}
	var seg []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			seg = append(seg, p)
		}
	}
	seg = seg[1:] // 去掉 "sub"
	if len(seg) == 0 {
		writeText(w, 404, "missing token\n", nil)
		return
	}
	token := seg[0]
	sub := ""
	if len(seg) > 1 {
		if u, err := url.PathUnescape(seg[1]); err == nil {
			sub = u
		} else {
			sub = seg[1]
		}
	}
	// 带名字的订阅地址：/sub/<token>/定制.yaml —— 客户端会把 URL 文件名当订阅名
	switch low := strings.ToLower(sub); {
	case strings.HasSuffix(low, ".yaml"), strings.HasSuffix(low, ".yml"):
		sub = "clash"
	case strings.HasSuffix(low, ".json"):
		sub = "singbox"
	case strings.HasSuffix(low, ".txt"), strings.HasSuffix(low, ".b64"):
		sub = ""
	}

	u, err := s.Store.UserByName(token)
	if err != nil {
		writeText(w, 404, "invalid token\n", nil)
		return
	}
	title := panel.ExpandTitle(st)
	headers := map[string]string{
		"Subscription-Userinfo":   panel.UserinfoHeader(u, st),
		"Profile-Update-Interval": "12",
		"Profile-Title":           "base64:" + base64.StdEncoding.EncodeToString([]byte(title)),
	}
	if u.Enable == 0 {
		reason := u.DisabledReason
		if reason == "" {
			reason = "管理员停用"
		}
		var sb strings.Builder
		for _, line := range strings.Split("订阅已停用: "+reason, "\n") {
			sb.WriteString("# " + line + "\n")
		}
		writeText(w, 200, sb.String(), headers)
		return
	}

	q := r.URL.Query()
	format := strings.ToLower(q.Get("fmt"))
	if sub == "singbox" || sub == "sing-box" || format == "singbox" || format == "sing-box" {
		body, skipped, err := s.Store.SingboxConfig(u, st)
		if err != nil {
			writeText(w, 500, "error: "+err.Error()+"\n", headers)
			return
		}
		if len(skipped) > 0 {
			headers["X-PolarisX-Skipped-Nodes"] = strings.Join(skipped, ",")
		}
		headers["Content-Disposition"] = panel.ContentDisposition(title, ".json")
		writeBody(w, 200, body, "application/json; charset=utf-8", headers)
		return
	}
	if sub == "clash" || format == "clash" {
		body, err := s.Store.ClashYAML(u, st, time.Now())
		if err != nil {
			writeText(w, 500, "error: "+err.Error()+"\n", headers)
			return
		}
		headers["Content-Disposition"] = panel.ContentDisposition(title, ".yaml")
		writeBody(w, 200, body, "text/yaml; charset=utf-8", headers)
		return
	}
	if sub == "info" || format == "json" {
		nodes, err := s.Store.NodesForUser(u, true)
		if err != nil {
			writeText(w, 500, "error: "+err.Error()+"\n", headers)
			return
		}
		tags := []string{}
		for _, n := range nodes {
			tags = append(tags, n.Tag)
		}
		quota := panel.QuotaBytes(u, st)
		used := panel.UsedBytes(u)
		var remaining *int64
		if quota > 0 {
			v := quota - used
			if v < 0 {
				v = 0
			}
			remaining = &v
		}
		doc := infoDoc{
			Name: u.Name, UsedBytes: used, UsedUpBytes: u.UsedUp, UsedDownBytes: u.UsedDown,
			UsedHuman: panel.HumanBytes(used), UsedUpHuman: panel.HumanBytes(u.UsedUp),
			UsedDownHuman: panel.HumanBytes(u.UsedDown), QuotaBytes: quota,
			RemainingBytes: remaining, ExpireAt: u.ExpireAt, Enable: u.Enable != 0, Nodes: tags,
		}
		body, err := panel.MarshalIndent(doc)
		if err != nil {
			writeText(w, 500, "error: "+err.Error()+"\n", headers)
			return
		}
		writeBody(w, 200, body, "application/json; charset=utf-8", headers)
		return
	}

	if format == "" {
		if q.Has("plain") {
			format = "plain"
		} else {
			format = "base64"
		}
	}
	body, ctype, err := s.Store.SubPayload(u, st, format)
	if err != nil {
		writeText(w, 500, "error: "+err.Error()+"\n", headers)
		return
	}
	headers["Content-Disposition"] = panel.ContentDisposition(title, ".txt")
	writeBody(w, 200, body, ctype, headers)
}

func writeText(w http.ResponseWriter, code int, body string, headers map[string]string) {
	writeBody(w, code, body, "text/plain; charset=utf-8", headers)
}

func writeBody(w http.ResponseWriter, code int, body, ctype string, headers map[string]string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	for k, v := range headers {
		h.Set(k, v)
	}
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
