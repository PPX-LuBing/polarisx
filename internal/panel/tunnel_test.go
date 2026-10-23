package panel

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 免域名隧道的可测部分：从 metrics 端口取域名、写回 sub_base、单元渲染。
// 真实隧道要连 Cloudflare，这里用假 metrics 服务替代（协议一致：/quicktunnel 返回 JSON）。
func TestQuickTunnelHost(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
		want string
	}{
		{"正常", `{"hostname":"calm-river-1234.trycloudflare.com"}`, 200, "calm-river-1234.trycloudflare.com"},
		{"大写转小写", `{"hostname":"Calm-River.TRYCLOUDFLARE.COM"}`, 200, "calm-river.trycloudflare.com"},
		{"不是临时隧道域名", `{"hostname":"example.com"}`, 200, ""},
		{"空", `{}`, 200, ""},
		{"非 JSON", `not json`, 200, ""},
		{"服务端错误", `{"hostname":"x.trycloudflare.com"}`, 500, "x.trycloudflare.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/quicktunnel" {
					t.Errorf("路径应为 /quicktunnel，实际 %s", r.URL.Path)
				}
				w.WriteHeader(c.code)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			st := Defaults()
			st.SubTunnelMetrics = strings.TrimPrefix(srv.URL, "http://")
			if got := QuickTunnelHost(st, 3*time.Second); got != c.want {
				t.Errorf("QuickTunnelHost = %q，期望 %q", got, c.want)
			}
		})
	}

	t.Run("服务没起来返回空", func(t *testing.T) {
		st := Defaults()
		st.SubTunnelMetrics = "127.0.0.1:1" // 必然连不上
		if got := QuickTunnelHost(st, 500*time.Millisecond); got != "" {
			t.Errorf("连不上时应返回空，实际 %q", got)
		}
	})
}

func TestWaitQuickTunnel(t *testing.T) {
	// 前两次请求返回空，第三次给出域名：验证轮询能等到
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"hostname":"late.trycloudflare.com"}`))
	}))
	defer srv.Close()
	st := Defaults()
	st.SubTunnelMetrics = strings.TrimPrefix(srv.URL, "http://")
	got := WaitQuickTunnel(st, 20, nil)
	if got != "late.trycloudflare.com" {
		t.Errorf("应等到域名，实际 %q（请求 %d 次）", got, n)
	}

	t.Run("一直拿不到就超时返回空", func(t *testing.T) {
		srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv2.Close()
		st2 := Defaults()
		st2.SubTunnelMetrics = strings.TrimPrefix(srv2.URL, "http://")
		if got := WaitQuickTunnel(st2, 1, nil); got != "" {
			t.Errorf("超时应返回空，实际 %q", got)
		}
	})
}

func TestApplyTunnelBase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	st := Defaults()
	st.SubBase = "http://1.2.3.4:61887"
	st.SubTunnelPrevBase = "http://1.2.3.4:61887"

	changed, err := ApplyTunnelBase(&st, "abc.trycloudflare.com")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("首次切换应报告已变化")
	}
	if st.SubBase != "https://abc.trycloudflare.com" || st.SubTunnelHost != "abc.trycloudflare.com" {
		t.Errorf("设置没写对: sub_base=%s host=%s", st.SubBase, st.SubTunnelHost)
	}
	if st.SubTunnelPrevBase != "http://1.2.3.4:61887" {
		t.Errorf("开隧道前的前缀应保留以便还原，实际 %s", st.SubTunnelPrevBase)
	}
	// 落盘了
	re, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if re.SubBase != "https://abc.trycloudflare.com" {
		t.Errorf("没写盘: %s", re.SubBase)
	}
	// 域名没变就不写盘（定时器每 2 分钟跑，不该反复改 mtime）
	if changed, err = ApplyTunnelBase(&st, "abc.trycloudflare.com"); err != nil || changed {
		t.Errorf("域名没变时不该报告变化: changed=%v err=%v", changed, err)
	}
}

func TestTunnelUnitsRenderSettings(t *testing.T) {
	st := Defaults()
	st.CloudflaredBin = "/opt/cf/cloudflared"
	st.SubTunnelMetrics = "127.0.0.1:19999"
	st.SubTunnelTarget = "http://127.0.0.1:8080"
	units := TunnelUnits(st)
	if len(units) != 3 {
		t.Fatalf("应有 3 个单元，实际 %d", len(units))
	}
	svc := units["polarisx-tunnel.service"]
	// 单元里必须用当前设置渲染，否则会出现「单元写死的参数与设置不一致」的坑
	for _, want := range []string{"/opt/cf/cloudflared", "--metrics 127.0.0.1:19999",
		"--url http://127.0.0.1:8080", "Restart=always"} {
		if !strings.Contains(svc, want) {
			t.Errorf("tunnel.service 缺 %q:\n%s", want, svc)
		}
	}
	if !strings.Contains(units["polarisx-tunnel-refresh.timer"], "OnUnitActiveSec=120") {
		t.Error("刷新定时器应是 2 分钟")
	}
	if !strings.Contains(units["polarisx-tunnel-refresh.service"], "polarisx tunnel refresh") {
		t.Error("刷新服务应调用 polarisx tunnel refresh")
	}
}

func TestFindCloudflared(t *testing.T) {
	dir := t.TempDir()
	// 设置里指定的路径存在就用它
	fake := filepath.Join(dir, "cloudflared")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := Defaults()
	st.CloudflaredBin = fake
	if got := FindCloudflared(st); got != fake {
		t.Errorf("应用设置里的路径，实际 %q", got)
	}
	// 路径不存在则回落（本机没装就是空，装了就是系统路径）
	st.CloudflaredBin = filepath.Join(dir, "nope")
	got := FindCloudflared(st)
	if got != "" && got != "/usr/local/bin/cloudflared" && got != "/usr/bin/cloudflared" {
		t.Errorf("回落到意料之外的路径: %q", got)
	}
}

func TestSubBaseFor(t *testing.T) {
	st := Defaults()
	st.SubBase = "https://abc.trycloudflare.com/"
	if got := SubBaseFor(st); got != "https://abc.trycloudflare.com" {
		t.Errorf("末尾斜杠应去掉，实际 %q", got)
	}
	st.SubBase = ""
	st.SubListen = "0.0.0.0:8080"
	if got := SubBaseFor(st); got != "http://0.0.0.0:8080" {
		t.Errorf("没配 sub_base 时应回落到监听地址，实际 %q", got)
	}
}
