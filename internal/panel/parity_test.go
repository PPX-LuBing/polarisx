package panel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestParityWithPython 是这次迁移的验收标准：
// 同一份 panel.db + settings，Go 版生成的 xray 配置必须与 Python 版语义一致。
// 期望值由 tools/mkfixture.py 用 Python 版生成，提交在 testdata/parity/。
func TestParityWithPython(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	fixtureDB := filepath.Join(root, "fixture", "panel.db")
	if _, err := os.Stat(fixtureDB); err != nil {
		t.Skipf("没有夹具（先跑 python3 tools/mkfixture.py）: %v", err)
	}
	// Home() 决定 xray-error.log 的路径，必须与生成夹具时一致
	fixtureAbs, _ := filepath.Abs(filepath.Join(root, "fixture"))
	t.Setenv("POLARISX_HOME", fixtureAbs)
	// 夹具里存的是 {REPO} 占位符；生成机器上的仓库根从 subscriptions.json 读
	genRepo := ""
	if b, err := os.ReadFile(filepath.Join(root, "parity", "subscriptions.json")); err == nil {
		var meta struct {
			RepoRoot string `json:"repo_root"`
		}
		if json.Unmarshal(b, &meta) == nil {
			genRepo = meta.RepoRoot
		}
	}
	repoAbs, _ := filepath.Abs(filepath.Join(root, ".."))

	variants, err := filepath.Glob(filepath.Join(root, "parity", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range variants {
		if strings.HasSuffix(v, ".settings.json") {
			continue
		}
		// 只有带同名 .settings.json 的才是配置变体（subscriptions.json 是订阅夹具）
		name := strings.TrimSuffix(filepath.Base(v), ".json")
		if _, err := os.Stat(filepath.Join(root, "parity", name+".settings.json")); err != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("testdata/parity 里没有期望配置")
	}

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			store := openFixture(t, root)
			defer store.Close()

			st := Defaults()
			sb, err := os.ReadFile(filepath.Join(root, "parity", name+".settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(sb, &st); err != nil {
				t.Fatal(err)
			}

			got, err := store.BuildConfig(st)
			if err != nil {
				t.Fatal(err)
			}

			wantRaw, err := os.ReadFile(filepath.Join(root, "parity", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal(wantRaw, &want); err != nil {
				t.Fatal(err)
			}
			// 两边都过一遍 JSON 归一化（数字统一成 float64，键序无关）；
			// HOME 路径归一化成 {HOME}（夹具不能带生成机器的绝对路径）
			if diff, ok := equalWithHome(t, got, want, repoAbs, genRepo); !ok {
				t.Errorf("配置与 Python 版不一致:\n%s", diff)
			}
		})
	}
}

// fixtureDB 把夹具 DB 复制到临时目录，并把 {REPO} 占位符替换成本地绝对路径
// （证书指纹要真读文件；夹具本身不能带生成机器的路径）。
func openFixture(t *testing.T, root string) *Store {
	t.Helper()
	repoAbs, err := filepath.Abs(filepath.Join(root, ".."))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(root, "fixture", "panel.db"))
	if err != nil {
		t.Skipf("没有夹具（先跑 python3 tools/mkfixture.py）: %v", err)
	}
	db := filepath.Join(t.TempDir(), "panel.db")
	if err := os.WriteFile(db, src, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(repoAbs, "testdata", "certs", "test.crt")
	key := filepath.Join(repoAbs, "testdata", "certs", "test.key")
	if _, err := store.DB().Exec("UPDATE nodes SET cert_file=?, key_file=?", cert, key); err != nil {
		t.Fatal(err)
	}
	return store
}

// equalWithHome 比较两个值，把仓库根路径归一化成 {REPO} 后逐行比对
// （夹具里存的是 {REPO} 占位符，两边生成时各自带自己的绝对路径）。
// equalWithHome 只把「仓库根」归一化成 {REPO}（HOME、证书路径都在仓库根之下，
// 统一替换才不会出现 {REPO}/xray-error.log 与 {REPO}/testdata/fixture/... 这种粒度不一致）。
func equalWithHome(t *testing.T, got, want any, repos ...string) (string, bool) {
	t.Helper()
	g, _ := json.MarshalIndent(normalize(t, got), "", "  ")
	w, _ := json.MarshalIndent(normalize(t, want), "", "  ")
	gs, ws := string(g), string(w)
	for _, r := range repos {
		if r == "" {
			continue
		}
		gs = strings.ReplaceAll(gs, r, "{REPO}")
		ws = strings.ReplaceAll(ws, r, "{REPO}")
	}
	if gs == ws {
		return "", true
	}
	gl, wl := strings.Split(gs, "\n"), strings.Split(ws, "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var a, b string
		if i < len(gl) {
			a = gl[i]
		}
		if i < len(wl) {
			b = wl[i]
		}
		if a != b {
			return fmt.Sprintf("第 %d 行:\n  go   : %s\n  python: %s", i+1, a, b), false
		}
	}
	return "行数不同", false
}

func normalize(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// firstDiff 逐行比对，报出第一处不同（比整个 JSON 打出来好读）。
func firstDiff(t *testing.T, got, want any) string {
	t.Helper()
	g, _ := json.MarshalIndent(got, "", "  ")
	w, _ := json.MarshalIndent(want, "", "  ")
	gl, wl := strings.Split(string(g), "\n"), strings.Split(string(w), "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var a, b string
		if i < len(gl) {
			a = gl[i]
		}
		if i < len(wl) {
			b = wl[i]
		}
		if a != b {
			return fmt.Sprintf("第 %d 行:\n  go   : %s\n  python: %s", i+1, a, b)
		}
	}
	return "（无差异？）"
}
