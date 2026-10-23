package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSingboxParity 比对 sing-box 订阅：JSON 按语义比（键序无关），
// 但被跳过的节点列表与列表顺序必须完全一致。
func TestSingboxParity(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	raw, err := os.ReadFile(filepath.Join(root, "parity", "subscriptions.json"))
	if err != nil {
		t.Skipf("没有订阅夹具（先跑 python3 tools/mkfixture.py）: %v", err)
	}
	var want struct {
		Singbox map[string]struct {
			JSON    any      `json:"json"`
			Skipped []string `json:"skipped"`
		} `json:"singbox"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want.Singbox) == 0 {
		t.Fatal("夹具里没有 sing-box 期望值")
	}

	fixtureAbs, _ := filepath.Abs(filepath.Join(root, "fixture"))
	t.Setenv("POLARISX_HOME", fixtureAbs)
	store := openFixture(t, root)
	defer store.Close()

	for key, exp := range want.Singbox {
		key, exp := key, exp
		t.Run(key, func(t *testing.T) {
			userName, dns := splitKey(key)
			u, err := store.UserByName(userName)
			if err != nil {
				t.Fatal(err)
			}
			st := Defaults()
			st.SingboxDNS = dns
			text, skipped, err := store.SingboxConfig(u, st)
			if err != nil {
				t.Fatal(err)
			}
			// 按内容比：nil 切片与空切片在这里等价
			if strings.Join(skipped, ",") != strings.Join(exp.Skipped, ",") {
				t.Errorf("跳过列表: %v，期望 %v", skipped, exp.Skipped)
			}
			var got any
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatalf("生成的 JSON 不合法: %v", err)
			}
			gotNorm, wantNorm := normalize(t, got), normalize(t, exp.JSON)
			if !jsonEqual(gotNorm, wantNorm) {
				t.Errorf("配置与 Python 版不一致:\n%s", firstDiff(t, gotNorm, wantNorm))
			}
		})
	}
}
