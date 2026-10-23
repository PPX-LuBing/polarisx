package panel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 管理层的往返测试：建节点/用户 → 出现在配置与订阅里 → 改 → 删。
// 这是「卸载 Python 版」的前提：Go 版必须自己能把机器管起来。
func TestManageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st := Defaults()

	t.Run("建节点", func(t *testing.T) {
		n := Node{Tag: "ix-reality", Proto: "vless", Net: "tcp", Security: "reality",
			Port: 443, EntryHost: "1.2.3.4", Dest: "www.microsoft.com:443",
			SNI: "www.microsoft.com", PrivKey: "PRIV", PubKey: "PUB", ShortID: "abcd",
			Flow: "xtls-rprx-vision"}
		if err := store.CreateNode(n); err != nil {
			t.Fatal(err)
		}
		// tag 重复要报错
		if err := store.CreateNode(n); err == nil {
			t.Error("重复 tag 应该报错")
		}
		got, err := store.NodeByTag("ix-reality")
		if err != nil {
			t.Fatal(err)
		}
		if got.Fingerprint != "chrome" || got.SpiderX != "/" || got.Enabled != 1 {
			t.Errorf("默认值没补上: %+v", got)
		}
		// hysteria2 别名与强制 TLS
		h := Node{Tag: "hy2", Proto: "hysteria2", Net: "hysteria", Port: 8443,
			CertFile: "/tmp/c.pem", KeyFile: "/tmp/k.pem", SNI: "a.example.com"}
		if err := store.CreateNode(h); err != nil {
			t.Fatal(err)
		}
		gh, _ := store.NodeByTag("hy2")
		if gh.Proto != "hysteria" || gh.Security != "tls" {
			t.Errorf("hysteria2 应归一成 hysteria 且强制 tls: %+v", gh)
		}
	})

	t.Run("建用户并出现在订阅里", func(t *testing.T) {
		u, err := store.CreateUser("alice", 100, "", "测试", nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(u.Token) != 16 || u.UUID == "" || u.Password == "" {
			t.Errorf("生成的凭据不完整: %+v", u)
		}
		// 名字校验
		if _, err := store.CreateUser("a", 1, "", "", nil); err == nil {
			t.Error("太短的用户名应报错")
		}
		if _, err := store.CreateUser("坏名字!", 1, "", "", nil); err == nil {
			t.Error("非法字符应报错")
		}
		if _, err := store.CreateUser("alice", 1, "", "", nil); err == nil {
			t.Error("重名应报错")
		}
		links, err := store.LinksForUser(u, st, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(links) != 2 {
			t.Fatalf("应有 2 条链接（两个节点），实际 %d", len(links))
		}
		if !strings.HasPrefix(links[0], "vless://") {
			t.Errorf("第一条应是 vless: %s", links[0])
		}
		// 配置里要有两个 inbound，且包含该用户
		cfg, err := store.BuildConfig(st)
		if err != nil {
			t.Fatal(err)
		}
		if inb := cfg["inbounds"].([]any); len(inb) != 2 {
			t.Errorf("inbound 数 %d，期望 2", len(inb))
		}
		// 绑定节点：只留 hy2
		if err := store.BindUserNodes("alice", []string{"hy2"}); err != nil {
			t.Fatal(err)
		}
		links, _ = store.LinksForUser(u, st, true)
		if len(links) != 1 || !strings.HasPrefix(links[0], "hysteria2://") {
			t.Errorf("绑定后应只剩 hysteria2 链接: %v", links)
		}
	})

	t.Run("改用户与节点", func(t *testing.T) {
		if err := store.UpdateUser("alice", []string{"quota_gb=50", "note=改了"}); err != nil {
			t.Fatal(err)
		}
		u, _ := store.UserByName("alice")
		if u.QuotaGB != 50 || u.Note != "改了" {
			t.Errorf("用户没改上: %+v", u)
		}
		// 停用后再启用应清掉停用原因
		if err := store.DisableUser(st, u, "流量超额 (1 GB / 1 GB)", false); err != nil {
			t.Fatal(err)
		}
		u, _ = store.UserByName("alice")
		if u.Enable != 0 || u.DisabledReason == "" {
			t.Errorf("停用状态不对: %+v", u)
		}
		if err := store.UpdateUser("alice", []string{"enable=1"}); err != nil {
			t.Fatal(err)
		}
		u, _ = store.UserByName("alice")
		if u.Enable != 1 || u.DisabledReason != "" {
			t.Errorf("重新启用应清掉原因: %+v", u)
		}
		// 非法字段要报错
		if err := store.UpdateUser("alice", []string{"uuid=xx"}); err == nil {
			t.Error("不可改的字段应报错")
		}
		if err := store.UpdateNode("hy2", []string{"port=8443", "sni=x.example.com"}); err != nil {
			t.Fatal(err)
		}
		gh, _ := store.NodeByTag("hy2")
		if gh.SNI != "x.example.com" {
			t.Errorf("节点没改上: %+v", gh)
		}
		if err := store.UpdateNode("hy2", []string{"port=abc"}); err == nil {
			t.Error("端口非整数应报错")
		}
		if err := store.UpdateNode("不存在", []string{"port=1"}); err == nil {
			t.Error("改不存在的节点应报错")
		}
	})

	t.Run("删用户与节点", func(t *testing.T) {
		// 删掉一个节点：另一个节点还有人用，所以应剩 1 个 inbound
		if err := store.DeleteNode("ix-reality"); err != nil {
			t.Fatal(err)
		}
		cfg, _ := store.BuildConfig(st)
		if inb := cfg["inbounds"].([]any); len(inb) != 1 {
			t.Errorf("删一个节点后 inbound 数 %d，期望 1", len(inb))
		}
		if err := store.DeleteUser("alice"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UserByName("alice"); err == nil {
			t.Error("用户应已删除")
		}
		// 没有用户了：节点不进配置（与 Python 版一致）
		cfg, _ = store.BuildConfig(st)
		if inb := cfg["inbounds"].([]any); len(inb) != 0 {
			t.Errorf("没有用户时不应有 inbound，实际 %d", len(inb))
		}
		if err := store.DeleteNode("hy2"); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteNode("不存在"); err == nil {
			t.Error("删不存在的节点应报错")
		}
	})

	t.Run("事件有记录", func(t *testing.T) {
		var n int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Error("管理操作应写事件")
		}
	})
}

// Apply --no-restart 只写配置，不碰 xray（离线可测）。
func TestApplyNoRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st := Defaults()
	st.XrayConfig = filepath.Join(dir, "xray-config.json")
	st.VerifyConfig = false // 离线环境没有 xray 可校验
	if err := store.CreateNode(Node{Tag: "n1", Proto: "vless", Net: "tcp", Security: "none",
		Port: 8443, EntryHost: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser("bob", 10, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(st, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(st.XrayConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"n1"`) || !strings.Contains(string(b), `"bob"`) {
		t.Errorf("配置里应包含节点与用户:\n%s", string(b)[:400])
	}
	// 权限必须是 600（里面有私钥）
	fi, _ := os.Stat(st.XrayConfig)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("配置权限 %o，期望 600", fi.Mode().Perm())
	}
}

// 事件日志早就写进表了，但一直没有查看入口；这里把查询的排序/限量/字段钉住。
func TestEventsOrderAndLimit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, m := range []string{"第一条", "第二条", "第三条"} {
		if err := store.Event(m, "info", "alice"); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := store.Events(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("limit 没生效，拿到 %d 条", len(evs))
	}
	if evs[0].Message != "第三条" || evs[1].Message != "第二条" {
		t.Errorf("应按时间倒序，实际 %s / %s", evs[0].Message, evs[1].Message)
	}
	if evs[0].UserName != "alice" || evs[0].Level != "info" {
		t.Errorf("字段没读全: %+v", evs[0])
	}
	if n := store.EventCount(); n != 3 {
		t.Errorf("EventCount = %d，期望 3", n)
	}
	if _, err := store.Events(0); err != nil {
		t.Errorf("limit=0 应回落默认值: %v", err)
	}
}
