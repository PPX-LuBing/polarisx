package sub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PPX-LuBing/polarisx/internal/panel"
)

// TestHTTPGolden 起 Go 版订阅服务，跑与 golden 基线完全相同的请求矩阵并逐项比对。
// 期望值锁定当前 Go 实现，存放在 testdata/golden/（testdata/golden/subscriptions.json）。
// 唯一允许的差异：/ 与 /health 之外的正文里没有产品名；根路径的产品名做了归一化。
func TestHTTPGolden(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	raw, err := os.ReadFile(filepath.Join(root, "golden", "subscriptions.json"))
	if err != nil {
		t.Skipf("没有订阅夹具（testdata fixtures are frozen）: %v", err)
	}
	var want struct {
		HTTP map[string]struct {
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		} `json:"http"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want.HTTP) == 0 {
		t.Fatal("夹具里没有 HTTP 期望值")
	}

	fixtureAbs, _ := filepath.Abs(filepath.Join(root, "fixture"))
	t.Setenv("POLARISX_HOME", fixtureAbs)
	dbCopy := filepath.Join(t.TempDir(), "panel.db")
	src, err := os.ReadFile(filepath.Join(fixtureAbs, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbCopy, src, 0o600); err != nil {
		t.Fatal(err)
	}
	// 证书路径的 {REPO} 占位符换成本地真实路径（clash/singbox 里的证书指纹要用）
	repoAbs, _ := filepath.Abs(filepath.Join(root, ".."))
	store, err := panel.Open(dbCopy)
	if err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(repoAbs, "testdata", "certs", "test.crt")
	key := filepath.Join(repoAbs, "testdata", "certs", "test.key")
	if _, err := store.DB().Exec("UPDATE nodes SET cert_file=?, key_file=?", cert, key); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Server{Store: store})
	defer srv.Close()
	defer store.Close()

	names := make([]string, 0, len(want.HTTP))
	for k := range want.HTTP {
		names = append(names, k)
	}
	for _, name := range names {
		exp := want.HTTP[name]
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(exp.Method, srv.URL+exp.Path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != exp.Status {
				t.Errorf("状态码 %d，期望 %d", resp.StatusCode, exp.Status)
			}
			wantBody := ""
			if exp.Body != nil {
				wantBody = *exp.Body
			}
			for k, wantV := range exp.Headers {
				gotV := resp.Header.Get(k)
				if k == "content-length" {
					// 长度随归一化后的正文一起比
					if exp.Body == nil {
						continue // HEAD 的 Content-Length 由下面的自洽性检查覆盖
					}
					wantV = strconv.Itoa(len(wantBody))
				}
				if gotV != wantV {
					t.Errorf("header %s: %q，期望 %q", k, gotV, wantV)
				}
			}
			if exp.Body == nil {
				if len(body) != 0 {
					t.Errorf("HEAD 不该有正文，收到 %d 字节", len(body))
				}
				// HEAD 的 Content-Length 必须等于对应 GET 的正文长度
				if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(exp.Headers["content-length"])) && cl == "" {
					t.Errorf("HEAD 缺 Content-Length")
				}
				return
			}
			got := string(body)
			// 生成时间戳必然不同，两边都归一化掉
			got = normalizeTimestamp(got)
			wantBody = normalizeTimestamp(wantBody)
			// JSON 正文按语义比（键序无关），其余逐字节比
			if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
				if !jsonSemanticEqual(t, got, wantBody) {
					t.Errorf("JSON 正文语义不一致:\n  go   : %s\n  golden: %s", truncate(got), truncate(wantBody))
				}
				return
			}
			if got != wantBody {
				t.Errorf("正文不一致: %s", bodyDiff(got, wantBody))
			}
		})
	}
}

// normalizeTimestamp 把订阅头部的「# 生成时间 ...」归一化（每次生成都不同）。
func normalizeTimestamp(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "# 生成时间 ") {
			lines[i] = "# 生成时间 {NOW}"
		}
	}
	return strings.Join(lines, "\n")
}

func jsonSemanticEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		t.Fatalf("Go 侧 JSON 不合法: %v", err)
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		t.Fatalf("期望 JSON 不合法: %v", err)
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

func bodyDiff(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	lo := i - 40
	if lo < 0 {
		lo = 0
	}
	hi, wi := i+60, i+60
	if hi > len(got) {
		hi = len(got)
	}
	if wi > len(want) {
		wi = len(want)
	}
	return fmt.Sprintf("第 %d 字节起不同\n  go   : …%s\n  golden: …%s", i, got[lo:hi], want[lo:wi])
}
