package panel

import (
	"path/filepath"
	"testing"
)

// selftest 靠这份客户端配置去连自己的节点，字段错了就永远测不通。
// 这里把每种协议/传输的关键字段钉住（夹具里的节点覆盖了主要组合）。
func TestBuildClientOutbound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st := Defaults()
	st.ServerHost = "203.0.113.9"

	u, err := store.CreateUser("alice", 10, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("reality 用公钥而不是私钥", func(t *testing.T) {
		n := Node{Tag: "r1", Proto: "vless", Net: "tcp", Security: "reality",
			Port: 443, EntryHost: "1.2.3.4", SNI: "s.example.com",
			PrivKey: "PRIV", PubKey: "PUB", ShortID: "abcd1234", Flow: "xtls-rprx-vision"}
		out, err := store.BuildClientOutbound(n, u, st)
		if err != nil {
			t.Fatal(err)
		}
		ss := out["streamSettings"].(map[string]any)
		rs := ss["realitySettings"].(map[string]any)
		if rs["publicKey"] != "PUB" {
			t.Errorf("客户端要用公钥，实际 %v", rs["publicKey"])
		}
		if rs["serverName"] != "s.example.com" || rs["shortId"] != "abcd1234" {
			t.Errorf("reality 参数不对: %+v", rs)
		}
		if rs["fingerprint"] != "chrome" || rs["spiderX"] != "/" {
			t.Errorf("默认指纹/spiderX 没补上: %+v", rs)
		}
		v := out["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)
		if v["address"] != "1.2.3.4" || v["port"] != 443 {
			t.Errorf("入口地址不对: %+v", v)
		}
		us := v["users"].([]any)[0].(map[string]any)
		if us["id"] != u.UUID || us["flow"] != "xtls-rprx-vision" {
			t.Errorf("用户字段不对: %+v", us)
		}
	})

	t.Run("自签 tls 要 pin 证书", func(t *testing.T) {
		cert := filepath.Join("..", "..", "testdata", "certs", "test.crt")
		n := Node{Tag: "h1", Proto: "hysteria", Net: "hysteria", Security: "tls",
			Port: 8443, EntryHost: "1.2.3.4", SNI: "hy2.example.com", CertFile: cert}
		out, err := store.BuildClientOutbound(n, u, st)
		if err != nil {
			t.Fatal(err)
		}
		ss := out["streamSettings"].(map[string]any)
		tls := ss["tlsSettings"].(map[string]any)
		// Xray 26 移除了 allowInsecure，自签必须 pin（这是实测踩过的坑）
		pin, _ := tls["pinnedPeerCertSha256"].(string)
		if len(pin) != 64 {
			t.Errorf("自签证书要 pin（64 位十六进制），实际 %q", pin)
		}
		if tls["serverName"] != "hy2.example.com" {
			t.Errorf("SNI 不对: %v", tls["serverName"])
		}
		if alpn, _ := tls["alpn"].([]any); len(alpn) != 1 || alpn[0] != "h3" {
			t.Errorf("hysteria 的 alpn 应为 h3，实际 %v", tls["alpn"])
		}
		s := out["settings"].(map[string]any)
		if s["address"] != "1.2.3.4" || s["port"] != 8443 {
			t.Errorf("hysteria 出站的 server 字段不对: %+v", s)
		}
		// auth 必须在传输层：Xray 出站 settings 里没有 auth 字段，写那儿等于没写
		// （曾经因此让客户端发空 auth，服务端回 auth failed code 404）
		if _, bad := s["auth"]; bad {
			t.Error("auth 不该放在出站 settings 里（Xray 那里没有这个字段）")
		}
		hs, ok := ss["hysteriaSettings"].(map[string]any)
		if !ok {
			t.Fatalf("缺 hysteriaSettings: %+v", ss)
		}
		if hs["auth"] == "" || hs["auth"] == nil {
			t.Error("每用户 PSK 必须放在 hysteriaSettings.auth")
		}
	})

	t.Run("trojan 用密码", func(t *testing.T) {
		n := Node{Tag: "t1", Proto: "trojan", Net: "grpc", Security: "tls",
			Port: 2087, EntryHost: "1.2.3.4", SNI: "g.example.com", GRPCService: "GunService"}
		out, err := store.BuildClientOutbound(n, u, st)
		if err != nil {
			t.Fatal(err)
		}
		srv := out["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
		if srv["password"] != u.Password {
			t.Errorf("trojan 密码不对: %+v", srv)
		}
		ss := out["streamSettings"].(map[string]any)
		if ss["grpcSettings"].(map[string]any)["serviceName"] != "GunService" {
			t.Errorf("grpc serviceName 没带上: %+v", ss)
		}
	})

	t.Run("shadowsocks 的 method 保持原样", func(t *testing.T) {
		n := Node{Tag: "s1", Proto: "shadowsocks", Net: "tcp", Security: "none",
			Port: 8388, EntryHost: "1.2.3.4", Method: "2022-blake3-aes-128-gcm"}
		out, err := store.BuildClientOutbound(n, u, st)
		if err != nil {
			t.Fatal(err)
		}
		srv := out["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
		// 订阅链接里就是 2022- 开头，别自作聪明去掉前缀
		if srv["method"] != "2022-blake3-aes-128-gcm" {
			t.Errorf("method 应原样，实际 %v", srv["method"])
		}
		if srv["password"] == "" {
			t.Error("SS2022 要带每用户密钥")
		}
	})

	t.Run("没入口地址要报错", func(t *testing.T) {
		st2 := st
		st2.ServerHost = ""
		n := Node{Tag: "x", Proto: "vless", Net: "tcp", Security: "none", Port: 443}
		if _, err := store.BuildClientOutbound(n, u, st2); err == nil {
			t.Error("入口地址全空时应报错，而不是生成一份连不上的配置")
		}
	})

	t.Run("SelfTestConfig 带 socks 入站", func(t *testing.T) {
		n := Node{Tag: "r2", Proto: "vless", Net: "tcp", Security: "reality",
			Port: 443, EntryHost: "1.2.3.4", SNI: "s.example.com",
			PubKey: "PUB", ShortID: "ab"}
		cfg, err := store.SelfTestConfig(n, u, st, 18099)
		if err != nil {
			t.Fatal(err)
		}
		in := cfg["inbounds"].([]any)[0].(map[string]any)
		if in["protocol"] != "socks" || in["port"] != 18099 {
			t.Errorf("socks 入站不对: %+v", in)
		}
		if len(cfg["outbounds"].([]any)) != 1 {
			t.Error("应只有一个出站（被测节点）")
		}
	})
}

// selftest 的跳过规则：hy2 用 Xray 客户端测不了（实测三种 auth 形式都握不上手，
// 而 mihomo 同一时刻一次就通），必须如实跳过而不是报"节点不通"。
func TestSelfTestSkipped(t *testing.T) {
	// 目前所有协议都能测。hysteria2 曾经被跳过，根因是我的配置 bug（auth 放错层），
	// 读 Xray 源码确认后已修，不该再跳过任何协议。
	for _, p := range []string{"vless", "vmess", "trojan", "shadowsocks", "hysteria"} {
		if why := SelfTestSkipped(Node{Proto: p}); why != "" {
			t.Errorf("%s 不该跳过: %s", p, why)
		}
	}
}

// SS2022 的客户端密码必须是「服务端PSK:用户PSK」——只给用户 PSK 会认证失败。
// 这里之前用的是裸的 UserNodeKey，导致自检对 SS2022 节点永远不通
// （clash/singbox/订阅链接都走 SSClientPassword，所以只有自检错）。
func TestBuildClientOutboundSS2022Password(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st := Defaults()
	st.ServerHost = "203.0.113.9"

	u, err := store.CreateUser("alice", 10, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	n := Node{Tag: "s1", Proto: "shadowsocks", Net: "tcp", Security: "none",
		Port: 30003, EntryHost: "1.2.3.4", Method: "2022-blake3-aes-128-gcm",
		SSKey: "SERVERPSK=="}
	out, err := store.BuildClientOutbound(n, u, st)
	if err != nil {
		t.Fatal(err)
	}
	pw := out["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)["password"].(string)

	userKey, err := store.UserNodeKey(u.ID, n)
	if err != nil {
		t.Fatal(err)
	}
	if pw == userKey {
		t.Fatal("客户端密码不能是裸的用户 PSK（SS2022 要拼服务端 PSK）")
	}
	if want := n.SSKey + ":" + userKey; pw != want {
		t.Errorf("客户端密码 = %q，期望 %q", pw, want)
	}
	// 与 clash / singbox / 订阅链接保持一致
	if cp, err := store.SSClientPassword(u, n); err != nil || cp != pw {
		t.Errorf("与 SSClientPassword 不一致: %q vs %q (%v)", pw, cp, err)
	}
}
