package sub

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PPX-LuBing/polarisx/internal/panel"
)

// TLS 能力：off / auto / files 三种模式的判定与校验（纯逻辑）。
func TestSetupTLS(t *testing.T) {
	base := panel.Defaults()
	base.SubListen = "0.0.0.0:8080"

	t.Run("默认是明文", func(t *testing.T) {
		s, err := SetupTLS(base, "")
		if err != nil {
			t.Fatal(err)
		}
		if s.Mode != TLSOff || s.Listen != "0.0.0.0:8080" {
			t.Errorf("默认应为明文且沿用 sub_listen，实际 %+v", s)
		}
	})

	t.Run("显式 off 也用给定地址", func(t *testing.T) {
		s, err := SetupTLS(base, "127.0.0.1:9000")
		if err != nil {
			t.Fatal(err)
		}
		if s.Mode != TLSOff || s.Listen != "127.0.0.1:9000" {
			t.Errorf("实际 %+v", s)
		}
	})

	t.Run("files 缺证书要报错", func(t *testing.T) {
		st := base
		st.SubTLS = "files"
		if _, err := SetupTLS(st, ""); err == nil {
			t.Error("只设了模式没给证书，应该报错")
		}
	})

	t.Run("files 完整配置", func(t *testing.T) {
		st := base
		st.SubTLS, st.SubTLSCert, st.SubTLSKey = "files", "/x/c.crt", "/x/c.key"
		s, err := SetupTLS(st, "")
		if err != nil {
			t.Fatal(err)
		}
		if s.Mode != TLSFiles || s.Cert != "/x/c.crt" || s.Key != "/x/c.key" {
			t.Errorf("实际 %+v", s)
		}
		if s.Listen != ":443" {
			t.Errorf("files 模式应改用 sub_tls_listen（默认 :443），实际 %q", s.Listen)
		}
	})

	t.Run("auto 没有域名要报错", func(t *testing.T) {
		st := base
		st.SubTLS = "auto"
		st.SubBase = "http://203.0.113.9:61887" // 只有 IP，ACME 签不了
		st.ServerHost = "203.0.113.9"
		if _, err := SetupTLS(st, ""); err == nil {
			t.Error("没有域名时 auto 应该报错（而不是去给 IP 申请证书）")
		}
	})

	t.Run("auto 完整配置（含 8443 这种非 443 端口）", func(t *testing.T) {
		st := base
		st.SubTLS = "auto"
		st.SubBase = "https://Sub.Example.COM:8443"
		st.ServerHost = "sub.example.com" // 与 sub_base 同域，应去重
		st.SubTLSListen = ":8443"
		s, err := SetupTLS(st, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Hosts) != 1 || s.Hosts[0] != "sub.example.com" {
			t.Errorf("域名应去重并小写，实际 %v", s.Hosts)
		}
		if s.Listen != ":8443" || s.HTTPListen != ":80" {
			t.Errorf("监听地址不对：%+v", s)
		}
		if s.RedirectTo != "https://sub.example.com:8443" {
			t.Errorf("非 443 端口跳转要带端口，实际 %q", s.RedirectTo)
		}
		if !strings.HasSuffix(s.CacheDir, "acme") {
			t.Errorf("证书缓存应在数据目录下，实际 %q", s.CacheDir)
		}
	})

	t.Run("443 的跳转不带端口", func(t *testing.T) {
		st := base
		st.SubTLS = "auto"
		st.SubBase = "https://sub.example.com"
		st.SubTLSListen = ":443"
		s, err := SetupTLS(st, "")
		if err != nil {
			t.Fatal(err)
		}
		if s.RedirectTo != "https://sub.example.com" {
			t.Errorf("443 不该带端口，实际 %q", s.RedirectTo)
		}
	})

	t.Run("未知取值要报错", func(t *testing.T) {
		st := base
		st.SubTLS = "是的"
		if _, err := SetupTLS(st, ""); err == nil {
			t.Error("未知模式应该报错，而不是悄悄按明文跑")
		}
	})
}

func TestACMEHosts(t *testing.T) {
	st := panel.Defaults()
	st.SubBase = "https://A.Example.com:8443/sub/x"
	st.ServerHost = "a.example.com." // 同域（大小写/末尾点不同）→ 去重
	if got := ACMEHosts(st); len(got) != 1 || got[0] != "a.example.com" {
		t.Errorf("实际 %v", got)
	}

	st.SubBase = ""
	st.ServerHost = "203.0.113.9" // 纯 IP：ACME 不给签，应被排除
	if got := ACMEHosts(st); len(got) != 0 {
		t.Errorf("IP 不该进白名单，实际 %v", got)
	}

	st.SubBase = "http://b.example.com"
	st.ServerHost = "c.example.com"
	if got := ACMEHosts(st); len(got) != 2 {
		t.Errorf("应同时收 sub_base 与 server_host 的域名，实际 %v", got)
	}
}

// files 模式真起一个 HTTPS 服务，用仓库里的自签证书验证能取到订阅。
func TestServeFilesTLS(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	fixtureAbs, _ := filepath.Abs(filepath.Join(root, "fixture"))
	src, err := os.ReadFile(filepath.Join(fixtureAbs, "panel.db"))
	if err != nil {
		t.Skipf("没有订阅夹具: %v", err)
	}
	t.Setenv("POLARISX_HOME", fixtureAbs)
	dbCopy := filepath.Join(t.TempDir(), "panel.db")
	if err := os.WriteFile(dbCopy, src, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := panel.Open(dbCopy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	users, err := store.Users()
	if err != nil || len(users) == 0 {
		t.Fatalf("夹具里应有用户: %v", err)
	}
	token := users[0].Token

	// 证书自己生成：仓库里的 test.key 被 .gitignore 排除（私钥不入库），
	// CI 上并不存在——依赖它会让测试"本地过、CI 挂"。
	certFile, keyFile := selfSignedCert(t)

	st := panel.Defaults()
	st.SubTLS, st.SubTLSCert, st.SubTLSKey = "files", certFile, keyFile
	st.SubTLSListen = freeAddr(t)

	srv := &Server{Store: store}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(st, "") }()
	defer func() {
		select {
		case err := <-errCh:
			if err != nil && !strings.Contains(err.Error(), "closed") {
				t.Errorf("Serve 返回: %v", err)
			}
		case <-time.After(100 * time.Millisecond):
		}
	}()

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	url := fmt.Sprintf("https://%s/sub/%s/clash", st.SubTLSListen, token)
	var resp *http.Response
	for i := 0; i < 50; i++ { // 等监听就绪
		resp, err = client.Get(url)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("HTTPS 请求失败（files 模式没起来？）: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("HTTPS 取订阅应 200，实际 %d", resp.StatusCode)
	}
	if len(body) == 0 {
		t.Error("HTTPS 取到的订阅是空的")
	}
	if !resp.ProtoAtLeast(2, 0) && resp.Proto != "HTTP/1.1" {
		t.Logf("协议: %s", resp.Proto)
	}
}

// freeAddr 找一个空闲端口（测完即释放；测试里够用）。
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// selfSignedCert 生成一对自签证书（写进临时目录），让 files 模式能真跑起来。
func selfSignedCert(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "t.crt")
	keyFile := filepath.Join(dir, "t.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// autocert 的缓存文件是「私钥在前、证书在后」的单个 PEM——只解第一块会拿到私钥。
// 这个测试用同样的排列钉住解析逻辑（真机上就是因为这个把"已签发"显示成"尚未签发"）。
func TestACMECertExpiryParsesKeyFirstPEM(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "cache.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	// 私钥在前，证书在后——与 autocert.DirCache 的写法一致
	blob := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)

	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme", "cache.example.com"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	exp, err := ACMECertExpiry("cache.example.com")
	if err != nil {
		t.Fatalf("应能从「私钥在前」的缓存里读到证书: %v", err)
	}
	if !exp.Equal(tmpl.NotAfter) {
		t.Errorf("到期时间 = %v，期望 %v", exp, tmpl.NotAfter)
	}
}
