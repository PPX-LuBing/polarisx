package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 免域名订阅：cloudflared 的临时隧道（trycloudflare.com）。
// 域名由 Cloudflare 随机分配、**每次重启 cloudflared 都会变**，所以：
//  1. 域名从 cloudflared 的 metrics 端口 /quicktunnel 取（不解析日志，稳）；
//  2. 定时器每 2 分钟 refresh 一次，把新域名写回 settings.sub_base。
const (
	DefaultTunnelMetrics = "127.0.0.1:19090"
	DefaultTunnelTarget  = "http://127.0.0.1:8080"
	cfRelease            = "https://github.com/cloudflare/cloudflared/releases/latest/download"
)

var cfMirrors = []string{"", "https://ghfast.top/", "https://gh-proxy.com/", "https://ghproxy.net/"}

var cfBinCandidates = []string{"/usr/local/bin/cloudflared", "/usr/bin/cloudflared"}

// FindCloudflared 找 cloudflared：设置 > 常见路径 > PATH。
func FindCloudflared(st Settings) string {
	if p := strings.TrimSpace(st.CloudflaredBin); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, p := range cfBinCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	return ""
}

// DownloadCloudflared 从 GitHub Releases 拉 cloudflared（带镜像回退）装到 /usr/local/bin。
func DownloadCloudflared(ctx context.Context) (string, error) {
	arch := "amd64"
	switch machine := runtimeArch(); machine {
	case "aarch64", "arm64":
		arch = "arm64"
	case "armv7l", "armv6l":
		arch = "arm"
	}
	dest := "/usr/local/bin/cloudflared"
	tmp := dest + ".part"
	var lastErr error
	for _, m := range cfMirrors {
		url := fmt.Sprintf("%s%s/cloudflared-linux-%s", m, cfRelease, arch)
		if err := downloadTo(ctx, url, tmp); err != nil {
			lastErr = err
			continue
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			lastErr = err
			continue
		}
		if err := os.Rename(tmp, dest); err != nil {
			lastErr = err
			continue
		}
		return dest, nil
	}
	return "", fmt.Errorf("下载 cloudflared 失败: %v", lastErr)
}

// QuickTunnelHost 问 cloudflared 的 metrics 端口要临时隧道域名；没起来/没分配就返回 ""。
func QuickTunnelHost(st Settings, timeout time.Duration) string {
	addr := strings.TrimSpace(st.SubTunnelMetrics)
	if addr == "" {
		addr = DefaultTunnelMetrics
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/quicktunnel", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var d struct {
		Hostname string `json:"hostname"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&d) != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(d.Hostname))
	// 只认临时隧道域名：设置里写错端口时可能问到别的东西
	if !strings.HasSuffix(host, ".trycloudflare.com") {
		return ""
	}
	return host
}

// WaitQuickTunnel 等 cloudflared 把域名要回来（首次建链一般 5~20 秒）。
func WaitQuickTunnel(st Settings, seconds int, log func(string)) string {
	if seconds < 1 {
		seconds = 60
	}
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		if host := QuickTunnelHost(st, 4*time.Second); host != "" {
			return host
		}
		if log != nil && i%5 == 0 {
			log("  等隧道域名…（cloudflared 首次建链一般 5~20 秒）")
		}
		time.Sleep(2 * time.Second)
	}
	return ""
}

// TunnelBase 隧道域名对应的订阅前缀。
func TunnelBase(host string) string { return "https://" + host }

// ApplyTunnelBase 把订阅地址切到隧道域名；域名没变就不写盘。返回是否变化。
func ApplyTunnelBase(st *Settings, host string) (bool, error) {
	base := TunnelBase(host)
	changed := st.SubTunnelHost != host || st.SubBase != base
	st.SubTunnelHost = host
	st.SubBase = base
	if !changed {
		return false, nil
	}
	return true, SaveSettings(*st)
}

// TunnelUnits 返回隧道需要的两个 systemd 单元内容（用当前设置渲染，
// 避免「单元里写死的 --metrics/--url 与设置不一致」这种坑）。
func TunnelUnits(st Settings) map[string]string {
	// 设置里写了就用它（哪怕此刻文件还不存在——起不来让 systemd 直白报错，
	// 比悄悄换成另一个路径好）；没写才去探测。
	cf := strings.TrimSpace(st.CloudflaredBin)
	if cf == "" {
		cf = FindCloudflared(st)
	}
	if cf == "" {
		cf = "/usr/local/bin/cloudflared"
	}
	metrics := st.SubTunnelMetrics
	if metrics == "" {
		metrics = DefaultTunnelMetrics
	}
	target := st.SubTunnelTarget
	if target == "" {
		target = DefaultTunnelTarget
	}
	svc := fmt.Sprintf(`[Unit]
Description=PolarisX 免域名订阅（cloudflared 临时隧道 → 本机订阅后端）
Documentation=https://github.com/PPX-LuBing/polarisx
After=network-online.target polarisx-sub.service
Wants=network-online.target

[Service]
Type=simple
# 临时隧道：不需要域名、不需要证书，域名由 Cloudflare 随机分配，每次重启都会变。
# 变了不用管：polarisx-tunnel-refresh.timer 每 2 分钟把新域名写回 sub_base。
ExecStart=%s tunnel --no-autoupdate --edge-ip-version auto --metrics %s --url %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, cf, metrics, target)
	refresh := `[Unit]
Description=PolarisX 免域名订阅：把临时隧道域名刷新到 sub_base
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/polarisx tunnel refresh
`
	timer := `[Unit]
Description=PolarisX 免域名订阅：定时刷新隧道域名

[Timer]
OnBootSec=90
OnUnitActiveSec=120
AccuracySec=10

[Install]
WantedBy=timers.target
`
	return map[string]string{
		"polarisx-tunnel.service":         svc,
		"polarisx-tunnel-refresh.service": refresh,
		"polarisx-tunnel-refresh.timer":   timer,
	}
}

// runtimeArch 读机器架构（避免引 runtime 只为这一处）。
func runtimeArch() string {
	if out, err := exec.Command("uname", "-m").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return "amd64"
}

// downloadTo 下载到文件（curl 优先，回退纯 Go）。
func downloadTo(ctx context.Context, url, dest string) error {
	if _, err := exec.LookPath("curl"); err == nil {
		cmd := exec.CommandContext(ctx, "curl", "-fsSL", "--max-time", "180", "-o", dest, url)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// ListenAddrInUse 判断端口是否已被监听（doctor 用来提示 metrics 冲突）。
func ListenAddrInUse(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// TunnelDir 隧道相关文件的目录（供外部引用）。
func TunnelDir() string { return filepath.Dir(Home()) }
