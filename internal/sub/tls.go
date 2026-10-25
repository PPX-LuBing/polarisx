package sub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/PPX-LuBing/polarisx/internal/panel"
)

// 订阅服务的 TLS 能力。
//
// 设计目标是"能力"而不是"配方"：以前要上 HTTPS 得自己装 nginx + certbot 再抄一份
// 配置，现在 polarisx sub 自己就能跑 TLS——auto 模式用 ACME 自动签发并自动续期，
// 同时自己处理 HTTP-01 挑战和 HTTP→HTTPS 跳转。三种模式：
//
//	off   明文 HTTP（默认；给"前面已有反代"的部署留的口子）
//	auto  ACME（Let's Encrypt）自动签发与续期
//	files 用指定的证书文件（自备证书/CDN 回源等）
//
// 监听端口可配：主生产机的 443 被 xray 的 reality 占着，TLS 就放在 8443，
// ACME 的 HTTP-01 挑战仍然走 80（Let's Encrypt 固定用 80 校验 HTTP-01）。
type TLSMode string

const (
	TLSOff   TLSMode = "off"
	TLSAuto  TLSMode = "auto"
	TLSFiles TLSMode = "files"
)

// TLSSetup 是"怎么起服务"的结果。把读设置、校验与真正的监听分开，方便单测。
type TLSSetup struct {
	Mode       TLSMode
	Listen     string   // 实际监听地址（明文模式即 sub_listen）
	HTTPListen string   // auto 模式：ACME 挑战 + 跳转的明文端口
	Cert, Key  string   // files 模式
	Hosts      []string // auto 模式：允许签发的域名（ACME 不给 IP 签）
	CacheDir   string   // auto 模式：证书缓存目录
	RedirectTo string   // auto 模式：HTTP 跳转目标前缀，如 https://a.example
}

// SetupTLS 根据设置算出监听方式；参数不对就返回人话错误（启动时快速失败）。
func SetupTLS(st panel.Settings, listen string) (TLSSetup, error) {
	mode := TLSMode(strings.ToLower(strings.TrimSpace(st.SubTLS)))
	if mode == "" {
		mode = TLSOff
	}
	if listen == "" {
		listen = st.SubListen
	}
	if listen == "" {
		listen = "0.0.0.0:8080"
	}
	setup := TLSSetup{Mode: mode, Listen: listen}

	switch mode {
	case TLSOff:
		return setup, nil

	case TLSFiles:
		if st.SubTLSCert == "" || st.SubTLSKey == "" {
			return setup, fmt.Errorf("sub_tls=files 需要同时设置 sub_tls_cert 与 sub_tls_key")
		}
		setup.Listen = orDefault(st.SubTLSListen, ":443")
		setup.Cert, setup.Key = st.SubTLSCert, st.SubTLSKey
		return setup, nil

	case TLSAuto:
		hosts := ACMEHosts(st)
		if len(hosts) == 0 {
			return setup, fmt.Errorf("sub_tls=auto 需要域名：请把 sub_base 设成 https://你的域名，" +
				"或把 server_host 设成域名（ACME 不能给 IP 签证书）")
		}
		setup.Hosts = hosts
		setup.Listen = orDefault(st.SubTLSListen, ":443")
		setup.HTTPListen = orDefault(st.SubHTTPListen, ":80")
		setup.CacheDir = ACMECacheDir()
		setup.RedirectTo = redirectBase(hosts[0], setup.Listen)
		return setup, nil
	}
	return setup, fmt.Errorf("未知的 sub_tls 取值 %q（可选 off / auto / files）", st.SubTLS)
}

// ACMEHosts 从设置里推导允许签发的域名：sub_base 的主机名 + server_host（若是域名）。
func ACMEHosts(st panel.Settings) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.TrimSpace(strings.ToLower(h))
		h = strings.TrimSuffix(h, ".")
		if h == "" || seen[h] || net.ParseIP(h) != nil {
			return // ACME 不给 IP 签证书，IP 直接不算
		}
		if strings.ContainsAny(h, "/: ") {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	if st.SubBase != "" {
		if u, err := url.Parse(st.SubBase); err == nil {
			add(u.Hostname())
		}
	}
	add(st.ServerHost)
	return out
}

// ACMECacheDir 是证书缓存目录（放在数据目录下，随备份一起走）。
func ACMECacheDir() string { return filepath.Join(panel.Home(), "acme") }

// ACMECertExpiry 从缓存里读出已签发证书的到期时间；还没签过则返回零值。
func ACMECertExpiry(host string) (time.Time, error) {
	cache := autocert.DirCache(ACMECacheDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := cache.Get(ctx, strings.ToLower(host))
	if err != nil {
		return time.Time{}, err
	}
	// autocert 的 DirCache 把「私钥 + 证书链」写在同一个文件里，且**私钥在前**。
	// 所以不能只解第一块（那是私钥，拿去 ParseCertificate 必然失败），要往后找
	// CERTIFICATE 块。注意：这里只读到 NotAfter，绝不把私钥内容带出去。
	var certDER []byte
	rest := b
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			certDER = block.Bytes
			break
		}
	}
	if certDER == nil {
		return time.Time{}, fmt.Errorf("缓存里没有 CERTIFICATE 块")
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

// redirectBase 拼出 HTTP 跳转的目标前缀（443 不带端口，其它端口带上）。
func redirectBase(host, listen string) string {
	port := portOf(listen)
	if port == "" || port == "443" {
		return "https://" + host
	}
	return "https://" + net.JoinHostPort(host, port)
}

func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return strings.TrimPrefix(addr, ":")
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// Serve 按设置决定监听方式。
func (s *Server) Serve(st panel.Settings, listen string) error {
	setup, err := SetupTLS(st, listen)
	if err != nil {
		return err
	}
	switch setup.Mode {
	case TLSFiles:
		slog.Info("订阅服务启动（自带证书）", "addr", setup.Listen, "cert", setup.Cert)
		srv := s.newServer(setup.Listen, nil)
		return srv.ListenAndServeTLS(setup.Cert, setup.Key)
	case TLSAuto:
		return s.serveAuto(setup)
	default:
		slog.Info("订阅服务启动（明文 HTTP）", "addr", setup.Listen)
		return s.newServer(setup.Listen, nil).ListenAndServe()
	}
}

// serveAuto 起 ACME：TLS 监听 + 明文端口做挑战与跳转。
func (s *Server) serveAuto(setup TLSSetup) error {
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(setup.CacheDir),
		HostPolicy: autocert.HostWhitelist(setup.Hosts...),
		// 新版 ACME 客户端默认就是这个；显式写上免得将来默认值变了行为跟着变
		Client: &acme.Client{DirectoryURL: autocert.DefaultACMEDirectory},
	}
	// 明文端口：ACME 的 HTTP-01 挑战由 Manager 处理，其余一律跳到 HTTPS。
	// （Let's Encrypt 校验 HTTP-01 固定走 80，所以这个端口必须能被公网访问。）
	if setup.HTTPListen != "" {
		redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, setup.RedirectTo+r.URL.RequestURI(), http.StatusMovedPermanently)
		})
		go func() {
			srv := &http.Server{
				Addr:              setup.HTTPListen,
				Handler:           m.HTTPHandler(redirect),
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      30 * time.Second,
			}
			slog.Info("ACME 挑战与跳转", "addr", setup.HTTPListen, "跳转到", setup.RedirectTo)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("ACME 明文端口启动失败（挑战会失败，但 HTTPS 仍在跑）", "err", err)
			}
		}()
	}
	slog.Info("订阅服务启动（ACME 自动证书）", "addr", setup.Listen, "域名", strings.Join(setup.Hosts, ","), "缓存", setup.CacheDir)
	tlsCfg := m.TLSConfig()
	tlsCfg.GetCertificate = sniFallback(m.GetCertificate, setup.Hosts)
	srv := s.newServer(setup.Listen, tlsCfg)
	return srv.ListenAndServeTLS("", "")
}

// newServer 统一超时等参数（明文与 TLS 共用）。
// sniFallback 包一层 autocert 的 GetCertificate，让客户端**不发 SNI** 时也能握手成功。
//
// autocert 在 ClientHelloInfo.ServerName 为空时直接返回 "missing server name"，
// 握手失败——真踩到过：订阅在非 443 端口时部分客户端不发 SNI，于是"拉不出来订阅"。
// 只配了一个域名时（本项目的常态）缺 SNI 就用它兜底；配了多个域名则无法判断该给
// 哪张证书，保持原行为。
func sniFallback(inner func(*tls.ClientHelloInfo) (*tls.Certificate, error), hosts []string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if len(hosts) != 1 {
		return inner
	}
	only := hosts[0]
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hello.ServerName == "" {
			withName := *hello
			withName.ServerName = only
			return inner(&withName)
		}
		return inner(hello)
	}
}

func (s *Server) newServer(addr string, tlsCfg *tls.Config) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
