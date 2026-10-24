package panel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 免域名订阅隧道（Cloudflare 临时隧道）的开启/关闭/刷新。
//
// 这段逻辑原先只写在 main.go 的 cmdTunnel 里，TUI 也要用，所以提到 panel 共用一份。
// 输出走 say 回调：CLI 打印到 stdout，TUI 收进界面。

// TunnelState 是隧道当前状态（只读展示用）。
type TunnelState struct {
	Enabled      bool
	Cloudflared  string // cloudflared 路径，空 = 找不到
	Target       string
	Metrics      string
	LiveHost     string // 从 metrics 端口读到的当前域名
	RecordedHost string // 写进订阅的域名
	SubBase      string
	HasSystemd   bool
	Units        map[string]string // 单元名 -> active 状态
	// Warn 是状态本身能看出来的问题（域名对不上等）
	Warn string
}

// TunnelStatus 收集隧道状态。liveTimeout 用于读 metrics 端口，传 0 表示不读。
func TunnelStatus(st Settings, liveTimeout time.Duration) TunnelState {
	s := TunnelState{
		Enabled:      st.SubTunnel,
		Cloudflared:  FindCloudflared(st),
		Target:       firstNonEmpty(st.SubTunnelTarget, DefaultTunnelTarget),
		Metrics:      firstNonEmpty(st.SubTunnelMetrics, DefaultTunnelMetrics),
		RecordedHost: st.SubTunnelHost,
		SubBase:      SubBaseFor(st),
		HasSystemd:   HasSystemd(),
	}
	if liveTimeout > 0 {
		s.LiveHost = QuickTunnelHost(st, liveTimeout)
	}
	if s.HasSystemd {
		s.Units = map[string]string{}
		for _, u := range TunnelUnitNames() {
			s.Units[u] = Systemctl("is-active", u)
		}
	}
	if st.SubTunnel {
		switch {
		// 订阅里没写域名：不看 metrics 也能判断，先报这个
		case strings.TrimSpace(st.SubTunnelHost) == "":
			s.Warn = "订阅地址还没切到隧道：跑一次「刷新隧道域名」"
		// 读到了实时域名但与订阅里写的不一致：说明 cloudflared 重启过
		case s.LiveHost != "" && s.LiveHost != st.SubTunnelHost:
			s.Warn = "域名对不上：跑一次「刷新隧道域名」更新订阅地址"
		}
	}
	return s
}

// TunnelUnitNames 隧道相关的 systemd 单元。
func TunnelUnitNames() []string {
	return []string{"polarisx-tunnel.service", "polarisx-tunnel-refresh.timer"}
}

// HasSystemd 判断本机是否用 systemd 管理服务。
func HasSystemd() bool {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return true
	}
	return exec.Command("systemctl", "is-system-running").Run() == nil
}

// Systemctl 跑一条 systemctl 并返回去空白后的输出（失败也不报错，调用方按需判断）。
func Systemctl(args ...string) string {
	out, _ := exec.Command("systemctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// InstallTunnelUnits 写入并启用隧道单元（需要 root）。
func InstallTunnelUnits(st Settings, say func(string)) error {
	if !HasSystemd() {
		cf := firstNonEmpty(FindCloudflared(st), "cloudflared")
		if say != nil {
			say("本机没有 systemd —— 自己常驻这条命令：")
			say(fmt.Sprintf("  %s tunnel --no-autoupdate --edge-ip-version auto --metrics %s --url %s",
				cf, firstNonEmpty(st.SubTunnelMetrics, DefaultTunnelMetrics),
				firstNonEmpty(st.SubTunnelTarget, DefaultTunnelTarget)))
		}
		return nil
	}
	for name, body := range TunnelUnits(st) {
		if err := os.WriteFile("/etc/systemd/system/"+name, []byte(body), 0o644); err != nil {
			return err
		}
	}
	Systemctl("daemon-reload")
	state := firstNonEmpty(Systemctl("enable", "--now",
		"polarisx-tunnel.service", "polarisx-tunnel-refresh.timer"), "已启用")
	if say != nil {
		say("systemd: " + state)
	}
	return nil
}

// TunnelOn 开启隧道：必要时下载 cloudflared，写单元，等域名，再把订阅地址切过去。
// download=false 且找不到 cloudflared 时返回可执行的提示。
func TunnelOn(ctx context.Context, s *Store, st Settings, download bool, waitSec int,
	say func(string)) (Settings, error) {
	original := st
	cf := FindCloudflared(st)
	if cf == "" && download {
		say("下载 cloudflared…")
		var err error
		cf, err = DownloadCloudflared(ctx)
		if err != nil {
			return st, err
		}
		say("已装到 " + cf)
	}
	if cf == "" {
		return st, fmt.Errorf("没找到 cloudflared。三选一：\n" +
			"  1) polarisx tunnel on --download   # 从 GitHub 下载（带镜像回退）\n" +
			"  2) apt-get install -y cloudflared  # Debian/Ubuntu\n" +
			"  3) polarisx set cloudflared_bin=/path/to/cloudflared")
	}
	st.CloudflaredBin = cf
	// Do not persist an enabled tunnel until it has a live hostname. A failed
	// startup must not leave subscriptions pointing at a tunnel that is down.
	if st.SubTunnelPrevBase == "" {
		st.SubTunnelPrevBase = st.SubBase
	}
	st.SubTunnel = true
	if err := InstallTunnelUnits(st, say); err != nil {
		return st, err
	}
	host := WaitQuickTunnel(st, waitSec, say)
	if host == "" {
		rollbackTunnelSettings(original)
		return st, fmt.Errorf("没等到隧道域名（cloudflared 没起来 / metrics 端口不对）。" +
			"起来后再跑一次「刷新隧道域名」")
	}
	if _, err := ApplyTunnelBase(&st, host); err != nil {
		rollbackTunnelSettings(original)
		return st, err
	}
	if s != nil {
		_ = s.Event("开启免域名订阅隧道: "+TunnelBase(host), "info", "")
	}
	say("订阅地址已切到: " + SubBaseFor(st))
	say("临时隧道域名每次重启 cloudflared 都会变，refresh 定时器每 2 分钟自动跟上")
	return st, nil
}

func rollbackTunnelSettings(st Settings) {
	// If it was already enabled, leave its existing unit running; a failed
	// refresh must not turn a previously healthy tunnel into a stopped one.
	if !st.SubTunnel && HasSystemd() {
		_ = exec.Command("systemctl", "disable", "--now",
			"polarisx-tunnel.service", "polarisx-tunnel-refresh.timer").Run()
	}
	_ = SaveSettings(st)
}

// TunnelOff 关闭隧道并把订阅地址还原成开启前的前缀。
func TunnelOff(s *Store, st Settings, say func(string)) (Settings, error) {
	if HasSystemd() {
		state := firstNonEmpty(Systemctl("disable", "--now",
			"polarisx-tunnel.service", "polarisx-tunnel-refresh.timer"), "已停用")
		if say != nil {
			say("systemd: " + state)
		}
	}
	prev := strings.TrimSpace(st.SubTunnelPrevBase)
	st.SubTunnel = false
	st.SubTunnelHost = ""
	st.SubTunnelPrevBase = ""
	st.SubBase = prev
	if err := SaveSettings(st); err != nil {
		return st, err
	}
	if s != nil {
		_ = s.Event("关闭免域名订阅隧道，订阅地址还原为 "+firstNonEmpty(prev, "(本机监听地址)"), "info", "")
	}
	if say != nil {
		say("隧道已关，订阅地址还原为: " + SubBaseFor(st))
	}
	return st, nil
}

// TunnelRefresh 从 metrics 端口读当前域名并同步到订阅地址。
func TunnelRefresh(s *Store, st Settings, say func(string)) (Settings, bool, error) {
	host := QuickTunnelHost(st, 4*time.Second)
	if host == "" {
		return st, false, fmt.Errorf("取不到隧道域名（cloudflared 没跑？）")
	}
	changed, err := ApplyTunnelBase(&st, host)
	if err != nil {
		return st, false, err
	}
	if changed {
		if s != nil {
			_ = s.Event("隧道域名刷新: "+TunnelBase(host), "info", "")
		}
		say("订阅地址已更新: " + SubBaseFor(st))
	} else {
		say("隧道域名没变: " + host)
	}
	return st, changed, nil
}
