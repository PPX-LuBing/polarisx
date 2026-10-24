package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClashGolden 比对 Clash/mihomo 订阅全文（3 个用户 × 6 个预设 = 18 份）。
// Golden files lock the current Go output; update them explicitly with UPDATE_GOLDEN=1.
func TestClashGolden(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	raw, err := os.ReadFile(filepath.Join(root, "golden", "subscriptions.json"))
	if err != nil {
		t.Skipf("没有订阅夹具（testdata fixtures are frozen）: %v", err)
	}
	var want struct {
		Clash    map[string]string `json:"clash"`
		Now      string            `json:"clash_frozen_now"`
		RepoRoot string            `json:"repo_root"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want.Clash) == 0 {
		t.Fatal("夹具里没有 clash 期望值")
	}
	genRepo := want.RepoRoot
	now, err := time.ParseInLocation("2006-01-02 15:04:05", want.Now, time.Local)
	if err != nil {
		t.Fatal(err)
	}

	fixtureAbs, _ := filepath.Abs(filepath.Join(root, "fixture"))
	t.Setenv("POLARISX_HOME", fixtureAbs)
	store := openFixture(t, root)
	defer store.Close()

	for key, exp := range want.Clash {
		key, exp := key, exp
		t.Run(key, func(t *testing.T) {
			userName, variant := splitKey(key)
			u, err := store.UserByName(userName)
			if err != nil {
				t.Fatal(err)
			}
			st := Defaults()
			st.ClashRules = variant
			if variant == "direct-ports" {
				st.ClashRules = "myclash"
				st.DirectPorts = "22, 2222, x"
			}
			got, err := store.ClashYAML(u, st, now)
			if err != nil {
				t.Fatal(err)
			}
			repoAbs, _ := filepath.Abs(filepath.Join(root, ".."))
			got = strings.ReplaceAll(got, repoAbs, "{REPO}")
			exp = strings.ReplaceAll(exp, genRepo, "{REPO}")
			if d := clashDiff(got, exp); d != "" {
				t.Errorf("%s: %s", key, d)
			}
		})
	}
}

// clashDiff 归一化产品名后逐行比对，报出第一处不同。
func clashDiff(got, want string) string {
	norm := func(s string) string {
		lines := strings.Split(s, "\n")
		if len(lines) > 0 && strings.HasPrefix(lines[0], "# ") {
			// Keep fixture comparison focused on protocol output, not generator name.
			if i := strings.Index(lines[0], " subscription for "); i > 0 {
				lines[0] = "# polarisx" + lines[0][i:]
			}
		}
		return strings.Join(lines, "\n")
	}
	g, w := norm(got), norm(want)
	if g == w {
		return ""
	}
	gl, wl := strings.Split(g, "\n"), strings.Split(w, "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var a, b string
		if i < len(gl) {
			a = gl[i]
		}
		if i < len(wl) {
			b = wl[i]
		}
		if a != b {
			return "第 " + itoa(i+1) + " 行不同:\n  go   : " + a + "\n  golden: " + b
		}
	}
	return "行数不同"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
