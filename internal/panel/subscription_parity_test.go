package panel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestSubscriptionParity 用同一份夹具比对分享链接、订阅载荷、标题与证书指纹。
// 期望值由 tools/mkfixture.py 用 Python 版生成。
func TestSubscriptionParity(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	raw, err := os.ReadFile(filepath.Join(root, "parity", "subscriptions.json"))
	if err != nil {
		t.Skipf("没有订阅夹具（先跑 python3 tools/mkfixture.py）: %v", err)
	}
	var want struct {
		Links    map[string][]string `json:"links"`
		Payloads map[string]struct {
			Body        string `json:"body"`
			ContentType string `json:"content_type"`
		} `json:"payloads"`
		Titles map[string]struct {
			Title       string `json:"title"`
			Disposition string `json:"disposition"`
		} `json:"titles"`
		CertFP struct {
			SHA256       string `json:"sha256"`
			PubKeySHA256 string `json:"pubkey_sha256"`
		} `json:"cert_fp"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	fixtureAbs, _ := filepath.Abs(filepath.Join(root, "fixture"))
	t.Setenv("POLARISX_HOME", fixtureAbs)
	store := openFixture(t, root)
	defer store.Close()
	st := Defaults()

	t.Run("links", func(t *testing.T) {
		for name, wantLinks := range want.Links {
			u, err := store.UserByName(name)
			if err != nil {
				t.Fatalf("取用户 %s: %v", name, err)
			}
			got, err := store.LinksForUser(u, st, true)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(got) != len(wantLinks) {
				t.Errorf("%s: 链接数 %d，期望 %d", name, len(got), len(wantLinks))
			}
			for i := range got {
				if i >= len(wantLinks) {
					break
				}
				if got[i] != wantLinks[i] {
					t.Errorf("%s 第 %d 条不一致: %s", name, i+1, diffStrings(got[i], wantLinks[i]))
				}
			}
		}
	})

	t.Run("payloads", func(t *testing.T) {
		for key, exp := range want.Payloads {
			name, format := splitKey(key)
			u, err := store.UserByName(name)
			if err != nil {
				t.Fatal(err)
			}
			body, ctype, err := store.SubPayload(u, st, format)
			if err != nil {
				t.Fatal(err)
			}
			if body != exp.Body {
				t.Errorf("%s 内容不一致: %s", key, diffStrings(body, exp.Body))
			}
			if ctype != exp.ContentType {
				t.Errorf("%s Content-Type: %q，期望 %q", key, ctype, exp.ContentType)
			}
		}
	})

	t.Run("titles", func(t *testing.T) {
		for name, exp := range want.Titles {
			var s Settings
			if name == "with-cc-asn" {
				s = Defaults()
				s.ServerCC, s.ServerASN, s.ServerOrg = "US", "AS174", "Cogent"
			} else {
				sb, err := os.ReadFile(filepath.Join(root, "parity", name+".settings.json"))
				if err != nil {
					t.Fatal(err)
				}
				s = Defaults()
				if err := json.Unmarshal(sb, &s); err != nil {
					t.Fatal(err)
				}
			}
			if got := ExpandTitle(s); got != exp.Title {
				t.Errorf("%s 标题: %q，期望 %q", name, got, exp.Title)
			}
			if got := ContentDisposition(ExpandTitle(s), ".yaml"); got != exp.Disposition {
				t.Errorf("%s 下载头:\n  go   : %s\n  python: %s", name, got, exp.Disposition)
			}
		}
	})

	t.Run("cert-fingerprint", func(t *testing.T) {
		cert := filepath.Join(root, "certs", "test.crt")
		if _, err := os.Stat(cert); err != nil {
			t.Skipf("没有测试证书: %v", err)
		}
		// Go 用 crypto/x509 原生算，必须与 openssl 的结果一致
		if got := CertSHA256(cert); got != want.CertFP.SHA256 {
			t.Errorf("证书 SHA256:\n  go     : %s\n  openssl: %s", got, want.CertFP.SHA256)
		}
		if got := CertPubKeySHA256(cert); got != want.CertFP.PubKeySHA256 {
			t.Errorf("公钥 SHA256(base64):\n  go     : %s\n  openssl: %s", got, want.CertFP.PubKeySHA256)
		}
	})
}

func splitKey(key string) (string, string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '.' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

func trunc(s string) string {
	if len(s) > 90 {
		return s[:90] + "…"
	}
	return s
}

// diffStrings 定位第一处不同并打印上下文（比整串截断好定位）。
func diffStrings(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := i - 30
	if lo < 0 {
		lo = 0
	}
	hi := i + 50
	if hi > len(a) {
		hi = len(a)
	}
	hi2 := i + 50
	if hi2 > len(b) {
		hi2 = len(b)
	}
	return fmt.Sprintf("第 %d 字节起不同\n  go   : …%s\n  python: …%s", i, a[lo:hi], b[lo:hi2])
}
