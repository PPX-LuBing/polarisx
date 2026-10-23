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

	variants, err := filepath.Glob(filepath.Join(root, "parity", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range variants {
		if strings.HasSuffix(v, ".settings.json") {
			continue
		}
		names = append(names, strings.TrimSuffix(filepath.Base(v), ".json"))
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("testdata/parity 里没有期望配置")
	}

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			// 每个变体用夹具 DB 的临时副本，避免测试往 testdata 里写
			dbCopy := filepath.Join(t.TempDir(), "panel.db")
			src, err := os.ReadFile(fixtureDB)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dbCopy, src, 0o600); err != nil {
				t.Fatal(err)
			}

			st := Defaults()
			sb, err := os.ReadFile(filepath.Join(root, "parity", name+".settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(sb, &st); err != nil {
				t.Fatal(err)
			}

			store, err := Open(dbCopy)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
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
			// 两边都过一遍 JSON 归一化（数字统一成 float64，键序无关）
			gotNorm := normalize(t, got)
			if !jsonEqual(gotNorm, want) {
				t.Errorf("配置与 Python 版不一致:\n%s", firstDiff(t, gotNorm, want))
			}
		})
	}
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
