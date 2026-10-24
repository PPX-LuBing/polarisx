package panel

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 节点自检：在本机起一个 xray 客户端，经它的 socks 端口访问外网，确认这个节点
// 真的能出去、并且出口 IP 就是这台落地机。
//
// 这段逻辑原先只存在于 main.go（CLI 专用），TUI 也要用，所以提到 panel 来共用一份。
// 命令行输出与退出码仍由 main.go 负责，这里只负责"测"。

// SelfTestFunc 让调用方注入实测实现（TUI 测试里不必真起 xray）。
type SelfTestFunc func(s *Store, st Settings, bin string, nodes []Node,
	userName string, basePort int, timeout time.Duration) []SelfTestResult

// PickUserForNode 挑一个能用这个节点的用户（默认第一个启用的）。
// 显式指定 name 时按名字找，找不到就报错——不要悄悄换人，否则测出来的结论对不上。
func PickUserForNode(s *Store, n Node, name string) (User, error) {
	if name != "" {
		u, err := s.UserByName(name)
		if err != nil {
			return User{}, fmt.Errorf("用户不存在: %s", name)
		}
		return u, nil
	}
	users, err := s.Users()
	if err != nil {
		return User{}, err
	}
	for _, u := range users {
		if u.Enable != 1 {
			continue
		}
		ns, err := s.NodesForUser(u, true)
		if err != nil {
			continue
		}
		for _, x := range ns {
			if x.Tag == n.Tag {
				return u, nil
			}
		}
	}
	return User{}, fmt.Errorf("没有可用这个节点的启用用户")
}

// SelfTestNode 实测单个节点：先用 xray -test 校验客户端配置，再起客户端跑 204 探活与出口 IP。
func SelfTestNode(s *Store, st Settings, bin string, n Node, u User,
	socksPort int, timeout time.Duration) SelfTestResult {
	res := SelfTestResult{Tag: n.Tag, Proto: n.Proto}
	cfg, err := s.SelfTestConfig(n, u, st, socksPort)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	body, err := MarshalIndent(cfg)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	dir, err := os.MkdirTemp("", "polarisx-selftest-")
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "client.json")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		res.Err = err.Error()
		return res
	}
	// 先让 xray 自己校验一遍：配置写错时这一步的报错比"握手失败"清楚得多
	if out, err := exec.Command(bin, "-test", "-config", cfgPath).CombinedOutput(); err != nil {
		res.Err = "客户端配置没通过 xray 校验: " + strings.TrimSpace(string(out))
		return res
	}
	cmd := exec.Command(bin, "run", "-config", cfgPath)
	if err := cmd.Start(); err != nil {
		res.Err = err.Error()
		return res
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	proxy := fmt.Sprintf("socks5h://127.0.0.1:%d", socksPort)
	// 等 socks 端口起来
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), 300*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	// 1) 204 探活（拿延迟）
	start := time.Now()
	code, err := curlThrough(proxy, "http://www.gstatic.com/generate_204", timeout)
	if err != nil || code != "204" {
		res.Err = fmt.Sprintf("204 探测失败（http=%s）: %v", code, err)
		return res
	}
	res.Latency = fmt.Sprintf("%dms", time.Since(start).Milliseconds())
	// 2) 出口 IP（确认真的从落地机出去，而不是直连）
	if ip, err := curlBody(proxy, "https://api.ipify.org", timeout); err == nil {
		res.ExitIP = strings.TrimSpace(ip)
	}
	res.OK = true
	return res
}

// SelfTestAll 按顺序实测多个节点，每个节点用一个递增的 socks 端口。
// 挑不到用户、或协议明确测不了（SelfTestSkipped）时不真测，如实记下原因。
func SelfTestAll(s *Store, st Settings, bin string, nodes []Node,
	userName string, basePort int, timeout time.Duration) []SelfTestResult {
	var results []SelfTestResult
	port := basePort
	for _, n := range nodes {
		u, err := PickUserForNode(s, n, userName)
		if err != nil {
			results = append(results, SelfTestResult{Tag: n.Tag, Proto: n.Proto, Err: err.Error()})
			port++
			continue
		}
		if why := SelfTestSkipped(n); why != "" {
			results = append(results, SelfTestResult{Tag: n.Tag, Proto: n.Proto, Skipped: true, Err: why})
			port++
			continue
		}
		results = append(results, SelfTestNode(s, st, bin, n, u, port, timeout))
		port++
	}
	return results
}

func curlThrough(proxy, url string, timeout time.Duration) (string, error) {
	return runCurl(proxy, url, timeout, true)
}

func curlBody(proxy, url string, timeout time.Duration) (string, error) {
	return runCurl(proxy, url, timeout, false)
}

func runCurl(proxy, url string, timeout time.Duration, codeOnly bool) (string, error) {
	args := []string{"-s", "--max-time", fmt.Sprintf("%d", int(timeout.Seconds())), "-x", proxy}
	if codeOnly {
		args = append(args, "-o", os.DevNull, "-w", "%{http_code}")
	}
	args = append(args, url)
	out, err := exec.Command("curl", args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
