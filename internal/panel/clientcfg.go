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
		key, err := s.UserNodeKey(u.ID, n)
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
		out["protocol"] = "hysteria"
		out["settings"] = map[string]any{"version": 2, "address": addr, "port": port, "auth": key}
	default:
		return nil, fmt.Errorf("不支持的协议: %s", n.Proto)
	}

	stream, err := s.clientStream(n, st)
	if err != nil {
		return nil, err
	}
	if len(stream) > 0 {
		out["streamSettings"] = stream
	}
	return out, nil
}

// clientStream 生成客户端的 streamSettings（与服务端不同：reality 用公钥、tls 用 pin）。
func (s *Store) clientStream(n Node, st Settings) (map[string]any, error) {
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
		stream["hysteriaSettings"] = map[string]any{"version": 2}
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

// SelfTestSkipped 说明为什么有些节点不能用 selftest 测。
//
// hysteria2：Xray 自带的 hy2 **客户端**在当前环境连自家 hy2 服务端都握不上手
// （实测：UUID 形式 auth、原始 base64 auth、base64 解码后的原始字节三种都报
// `auth failed code 404`；同一时刻 mihomo 连同一个服务端一次就通）。
// 所以这不是服务端问题，而是 Xray 客户端侧的限制——如实跳过，不假装失败。
func SelfTestSkipped(n Node) string {
	if n.Proto == "hysteria" {
		return "跳过：Xray 的 hysteria2 客户端无法完成握手（服务端正常，可用 mihomo 等客户端验证）"
	}
	return ""
}

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
