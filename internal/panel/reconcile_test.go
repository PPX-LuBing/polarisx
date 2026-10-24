package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 结构比较：只换用户要判"相同"（走 API 热更新），换 routing/outbound 要判"不同"（重启）。
// 之前 Apply 一律走 API，改分流后运行中的实例还是旧路由（真机踩到）。
func TestConfigStructureSame(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	path := filepath.Join(dir, "config.json")
	st := Defaults()
	st.XrayConfig = path

	base := map[string]any{
		"inbounds": []any{map[string]any{
			"tag": "ix-shadowsocks", "protocol": "shadowsocks", "port": 30003,
			"settings": map[string]any{"method": "2022-blake3-aes-128-gcm",
				"clients": []any{map[string]any{"email": "a", "password": "K1"}}}}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
		"routing": map[string]any{"rules": []any{map[string]any{
			"type": "field", "domain": []any{"domain:openai.com"}, "outboundTag": "chain"}}},
	}
	write := func(cfg map[string]any) {
		b, err := marshalNoEscape(cfg, "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clone := func() map[string]any {
		b, _ := marshalNoEscape(base, "  ")
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	write(base)
	if !configStructureSame(st, clone()) {
		t.Error("完全一样应判为相同")
	}

	// 只换用户 → 仍然"相同"（可热更新）
	onlyUsers := clone()
	onlyUsers["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["clients"] =
		[]any{map[string]any{"email": "b", "password": "K2"}}
	if !configStructureSame(st, onlyUsers) {
		t.Error("只改用户应判为相同（走 API 热更新，不该重启）")
	}

	// 改 routing → "不同"（必须重启）
	routeChanged := clone()
	routeChanged["routing"].(map[string]any)["rules"].([]any)[0].(map[string]any)["domain"] =
		[]any{"domain:chatgpt.com"}
	if configStructureSame(st, routeChanged) {
		t.Error("routing 变了必须判为不同（否则分流不生效）")
	}

	// 加 outbound → "不同"
	outChanged := clone()
	outChanged["outbounds"] = append(outChanged["outbounds"].([]any),
		map[string]any{"tag": "chain", "protocol": "shadowsocks"})
	if configStructureSame(st, outChanged) {
		t.Error("outbound 变了必须判为不同")
	}

	// 改端口 → "不同"
	portChanged := clone()
	portChanged["inbounds"].([]any)[0].(map[string]any)["port"] = 30004
	if configStructureSame(st, portChanged) {
		t.Error("端口变了必须判为不同")
	}
}

// 回归：比较不能改坏传进来的配置。maskClients 曾经直接改 desired，
// 于是 Apply 比较完再写盘时 clients 变成 null —— 用户被整个抹掉。
func TestConfigStructureSameDoesNotMutate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	path := filepath.Join(dir, "config.json")
	st := Defaults()
	st.XrayConfig = path

	cfg := map[string]any{
		"inbounds": []any{map[string]any{
			"tag": "ss", "protocol": "shadowsocks", "port": 30003,
			"settings": map[string]any{"method": "2022-blake3-aes-128-gcm",
				"clients": []any{map[string]any{"email": "hk", "password": "K"}}}}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
	}
	b, err := marshalNoEscape(cfg, "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = configStructureSame(st, cfg)

	clients := cfg["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["clients"]
	if clients == nil {
		t.Fatal("configStructureSame 不能把传进来的 clients 改成 nil（会导致用户被抹掉）")
	}
	if l, ok := clients.([]any); !ok || len(l) != 1 {
		t.Fatalf("clients 被改坏了: %#v", clients)
	}
}
