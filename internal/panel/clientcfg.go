package panel

import (
	"fmt"
)

// BuildClientOutbound 按节点+用户生成**客户端出站**配置（服务端入站的镜像）。
//
// 用途是 selftest：在本机起一个 xray 客户端，从这台机器实测"这个节点到底通不通"。
// 有了它就能一条命令分清「服务端坏了」还是「你的网络/客户端问题」——否则只能靠手工拼配置。
func (s *Store) BuildClientOutbound(n Node, u User, st Settings) (map[string]any, error) {
	e := Entry{Host: firstNonEmpty(CSV(n.EntryHost)...), Port: n.EntryPort}
	if e.Port == 0 {
		e.Port = n.Port
	}
	if e.Host == "" {
		e.Host = st.ServerHost
	}
	if e.Host == "" {
		return nil, fmt.Errorf("节点 %s 没配入口地址（entry_host / server_host 都是空）", n.Tag)
	}
	addr, port := e.Host, e.Port
	if port == 0 {
		port = n.Port
	}

	out := map[string]any{"tag": "selftest-" + n.Tag}
	hysteriaAuth := ""
	switch n.Proto {
	case "vless":
		user := map[string]any{"id": u.UUID, "encryption": "none"}
		if n.Flow != "" {
			user["flow"] = n.Flow
		}
		out["protocol"] = "vless"
		out["settings"] = map[string]any{"vnext": []any{map[string]any{
			"address": addr, "port": port, "users": []any{user}}}}
	case "vmess":
		out["protocol"] = "vmess"
		out["settings"] = map[string]any{"vnext": []any{map[string]any{
			"address": addr, "port": port, "users": []any{map[string]any{
				"id": u.UUID, "alterId": 0, "security": "auto"}}}}}
	case "trojan":
		out["protocol"] = "trojan"
		out["settings"] = map[string]any{"servers": []any{map[string]any{
			"address": addr, "port": port, "password": u.Password}}}
	case "shadowsocks":
		// SS2022 的**客户端**密码是「服务端PSK:用户PSK」，不是裸的用户 PSK。
		// 这里原先用 UserNodeKey（裸的用户 PSK），于是自检对 SS2022 节点永远
		// 认证失败；clash/singbox/订阅链接都走 SSClientPassword，所以只有自检错。
		key, err := s.SSClientPassword(u, n)
		if err != nil {
			return nil, err
		}
		// method 原样用：订阅链接里就是 `2022-blake3-aes-128-gcm`（别去掉 2022- 前缀）
		out["protocol"] = "shadowsocks"
		out["settings"] = map[string]any{"servers": []any{map[string]any{
			"address": addr, "port": port, "method": n.Method, "password": key}}}
	case "hysteria":
		key, err := s.UserNodeKey(u.ID, n)
		if err != nil {
			return nil, err
		}
		// 注意：Xray 出站 settings 里**没有 auth 字段**（只有 server/address/port）。
		// hy2 的 auth 属于传输层，必须写进 streamSettings.hysteriaSettings.auth，
		// 否则客户端会发空 auth，服务端回 `auth failed code 404`。
		// （这个是读 Xray 源码确认的：transport/internet/hysteria/config.proto 的 Config.auth）
		hysteriaAuth = key
		out["protocol"] = "hysteria"
		out["settings"] = map[string]any{"version": 2, "address": addr, "port": port}
	default:
		return nil, fmt.Errorf("不支持的协议: %s", n.Proto)
	}

	stream, err := s.clientStream(n, st, hysteriaAuth)
	if err != nil {
		return nil, err
	}
	if len(stream) > 0 {
		out["streamSettings"] = stream
	}
	return out, nil
}

// clientStream 生成客户端的 streamSettings（与服务端不同：reality 用公钥、tls 用 pin）。
func (s *Store) clientStream(n Node, st Settings, hysteriaAuth string) (map[string]any, error) {
	network := n.Net
	if network == "" {
		network = "tcp"
	}
	stream := map[string]any{"network": network}
	switch n.Security {
	case "reality":
		sni := firstNonEmpty(append(CSV(n.SNI), n.WSHost)...)
		if n.PubKey == "" || sni == "" {
			return nil, fmt.Errorf("reality 节点缺公钥或 SNI，无法作为客户端连接")
		}
		fp := n.Fingerprint
		if fp == "" {
			fp = "chrome"
		}
		spx := n.SpiderX
		if spx == "" {
			spx = "/"
		}
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{
			"serverName": sni, "fingerprint": fp, "publicKey": n.PubKey,
			"shortId": n.ShortID, "spiderX": spx,
		}
	case "tls":
		sni := firstNonEmpty(append(CSV(n.SNI), n.WSHost)...)
		tls := map[string]any{"serverName": sni}
		if network == "hysteria" {
			tls["alpn"] = []any{"h3"}
		} else {
			tls["alpn"] = []any{"h2", "http/1.1"}
		}
		// 自签证书要 pin：Xray 26 已移除 allowInsecure（这是实测踩过的坑）
		if n.CertFile != "" {
			if fp := CertSHA256(n.CertFile); fp != "" {
				tls["pinnedPeerCertSha256"] = fp
			}
		}
		stream["security"] = "tls"
		stream["tlsSettings"] = tls
	}
	switch network {
	case "ws":
		ws := map[string]any{"path": orSlash(n.Path)}
		if n.WSHost != "" {
			ws["host"] = n.WSHost
		}
		stream["wsSettings"] = ws
	case "httpupgrade":
		hu := map[string]any{"path": orSlash(n.Path)}
		if n.WSHost != "" {
			hu["host"] = n.WSHost
		}
		stream["httpupgradeSettings"] = hu
	case "xhttp":
		x := map[string]any{"path": orSlash(n.Path)}
		if n.WSHost != "" {
			x["host"] = n.WSHost
		}
		stream["xhttpSettings"] = x
	case "grpc":
		stream["grpcSettings"] = map[string]any{"serviceName": n.GRPCService}
	case "hysteria":
		// 客户端的 auth 在这里（不在出站 settings 里）
		hs := map[string]any{"version": 2}
		if hysteriaAuth != "" {
			hs["auth"] = hysteriaAuth
		}
		stream["hysteriaSettings"] = hs
	}
	return stream, nil
}

// SelfTestResult 是单个节点的自检结果。
type SelfTestResult struct {
	Tag     string `json:"tag"`
	Proto   string `json:"proto"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Latency string `json:"latency,omitempty"`
	ExitIP  string `json:"exit_ip,omitempty"`
	Err     string `json:"err,omitempty"`
}

// SelfTestSkipped 说明为什么某些节点不能用 selftest 测。
//
// 目前没有需要跳过的协议。曾经以为 hysteria2 不行——那是**我的配置 bug**：
// hy2 的 auth 属于传输层（streamSettings.hysteriaSettings.auth），Xray 出站的
// settings 里根本没有 auth 字段，所以客户端一直在发空 auth，服务端回 404。
// 读 Xray 源码（transport/internet/hysteria/config.proto 的 Config.auth）确认后已修。
func SelfTestSkipped(n Node) string { return "" }

// SelfTestConfig 组装"本机起客户端测这个节点"用的完整 xray 配置。
func (s *Store) SelfTestConfig(n Node, u User, st Settings, socksPort int) (map[string]any, error) {
	out, err := s.BuildClientOutbound(n, u, st)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "socks", "listen": "127.0.0.1", "port": socksPort, "protocol": "socks",
			"settings": map[string]any{"udp": true, "auth": "noauth"},
		}},
		"outbounds": []any{out},
	}, nil
}
