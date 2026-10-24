package panel

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 停用用户必须真的把在线连接摘掉：超额用户只写 enable=0 不够，
// 已有连接会一直跑到自然断开（要等 xray 重启才生效）。
// 这里用假 xray 脚本验证真实调用路径（FindXray 认 settings.xray_bin）。
func TestDisableUserKicksViaRmu(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// 假 xray：把收到的参数记到文件，一律返回 0
	calls := filepath.Join(dir, "calls.log")
	fake := filepath.Join(dir, "xray")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+calls+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := Defaults()
	st.XrayBin = fake
	st.XrayAPI = "127.0.0.1:10085"

	if err := store.CreateNode(Node{Tag: "n1", Proto: "vless", Net: "tcp",
		Security: "reality", Port: 443, EntryHost: "1.2.3.4", SNI: "s.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser("alice", 1, "", "", []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.UserByName("alice")
	if err != nil {
		t.Fatal(err)
	}

	read := func() string {
		b, _ := os.ReadFile(calls)
		return string(b)
	}

	t.Run("kick=true 会调 rmu", func(t *testing.T) {
		if err := store.DisableUser(st, u, "流量超额", true); err != nil {
			t.Fatal(err)
		}
		got := read()
		if !strings.Contains(got, "api rmu") {
			t.Fatalf("没有调用 rmu，实际调用:\n%s", got)
		}
		if !strings.Contains(got, "-tag=n1") || !strings.Contains(got, "alice") {
			t.Errorf("rmu 参数不对（要按 tag + 用户名摘）:\n%s", got)
		}
		// 停用状态与原因仍要落库
		got2, _ := store.UserByName("alice")
		if got2.Enable != 0 || got2.DisabledReason != "流量超额" {
			t.Errorf("停用状态不对: %+v", got2)
		}
	})

	t.Run("kick=false 不碰 xray", func(t *testing.T) {
		os.Remove(calls)
		if err := store.DisableUser(st, u, "管理员手动停用", false); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != "" {
			t.Errorf("kick=false 不该调 xray，实际:\n%s", got)
		}
	})

	t.Run("API 不可用时不报错（老 xray 没有 rmu）", func(t *testing.T) {
		// 假 xray 改成非 0 退出，模拟老版本不支持 statsquery/rmu
		if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := store.DisableUser(st, u, "流量超额", true); err != nil {
			t.Errorf("API 不可用时应静默跳过，不该报错: %v", err)
		}
	})

	t.Run("没有 xray 二进制也不报错", func(t *testing.T) {
		st2 := st
		st2.XrayBin = filepath.Join(dir, "not-exist")
		if err := store.DisableUser(st2, u, "流量超额", true); err != nil {
			t.Errorf("找不到 xray 时应静默跳过: %v", err)
		}
	})
}

// REALITY 密钥必须与 `xray x25519` 同格式（base64url 无填充，43 字符），
// 且公私钥要自洽——否则客户端连不上，而这类错误在配置生成阶段看不出来。
func TestGenerateRealityKeys(t *testing.T) {
	priv, pub, sid, err := GenerateRealityKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(priv) != 43 || len(pub) != 43 {
		t.Errorf("长度应为 43（base64url 无填充 32 字节），实际 priv=%d pub=%d", len(priv), len(pub))
	}
	for _, s := range []string{priv, pub} {
		if strings.ContainsAny(s, "+/=") {
			t.Errorf("应为 base64url 无填充（不含 + / =）: %s", s)
		}
		if _, err := base64.RawURLEncoding.DecodeString(s); err != nil {
			t.Errorf("不是合法 base64url: %s", s)
		}
	}
	if len(sid) != 8 {
		t.Errorf("shortId 应为 8 位十六进制，实际 %q", sid)
	}
	if _, err := hex.DecodeString(sid); err != nil {
		t.Errorf("shortId 不是十六进制: %q", sid)
	}
	// 公钥必须由私钥推导得出
	kb, _ := base64.RawURLEncoding.DecodeString(priv)
	k, err := ecdh.X25519().NewPrivateKey(kb)
	if err != nil {
		t.Fatalf("私钥不可用: %v", err)
	}
	if got := base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()); got != pub {
		t.Errorf("公钥与私钥不匹配:\n  priv 导出 %s\n  给的    %s", got, pub)
	}
	// 两次生成必须不同
	priv2, _, sid2, _ := GenerateRealityKeys()
	if priv2 == priv || sid2 == sid {
		t.Error("两次生成不应相同")
	}
}
