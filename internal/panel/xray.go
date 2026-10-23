package panel

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DefaultSniffing 与 Python 版一致。
var DefaultSniffing = map[string]any{
	"enabled": true, "destOverride": []any{"http", "tls", "quic"}, "routeOnly": false,
}

// ProtoAliases 把 hysteria2 / hy2 归一成 xray 的 protocol 名 hysteria。
var ProtoAliases = map[string]string{"hysteria2": "hysteria", "hy2": "hysteria"}

// NodeProblems 体检节点：缺关键字段会让整个 xray 起不来（所有节点一起挂），
// 所以有问题的节点在生成配置时直接跳过。
func NodeProblems(n Node) []string {
	var p []string
	if n.Port == 0 {
		p = append(p, "没有监听端口")
	}
	switch n.Proto {
	case "shadowsocks":
		if SS2022KeySize[n.Method] == 0 {
			p = append(p, fmt.Sprintf("shadowsocks 加密方式缺失或不受支持: %q（只支持 SS2022）", n.Method))
		}
		if n.SSKey == "" {
			p = append(p, "shadowsocks 缺少服务端 PSK")
		}
	case "vless":
		if n.Security == "reality" {
			if n.PrivKey == "" {
				p = append(p, "reality 缺少私钥")
			}
			if n.Dest == "" {
				p = append(p, "reality 缺少伪装目标 dest")
			}
		}
		if n.Security == "tls" && n.CertFile == "" {
			p = append(p, "tls 节点没有证书文件（xray 会拒绝加载）")
		}
	case "trojan":
		if n.Security == "tls" && n.CertFile == "" {
			p = append(p, "trojan 节点没有证书文件")
		}
	case "hysteria":
		if n.CertFile == "" || n.KeyFile == "" {
			p = append(p, "hysteria2 节点没有证书文件（TLS 是协议的一部分，cert/key 必填）")
		}
		if n.SNI == "" {
			p = append(p, "hysteria2 节点缺少 sni（客户端要用它校验服务端证书）")
		}
	case "vmess":
		// vmess 没有必填的额外字段
	default:
		p = append(p, fmt.Sprintf("未知协议 %q", n.Proto))
	}
	if n.Proto == "hysteria" {
		if n.Net != "hysteria" {
			p = append(p, fmt.Sprintf("hysteria2 的传输必须是 hysteria（UDP），当前 %q", n.Net))
		}
	} else if n.Proto != "shadowsocks" {
		switch n.Net {
		case "tcp", "ws", "httpupgrade", "xhttp", "grpc":
		default:
			p = append(p, fmt.Sprintf("未知传输 %q", n.Net))
		}
	}
	return p
}

// BuildInbound 把节点 + 用户列表编成 xray inbound。
func (s *Store) BuildInbound(n Node, users []User) (map[string]any, error) {
	clients := make([]any, 0, len(users))
	for _, u := range users {
		switch n.Proto {
		case "vless":
			c := map[string]any{"id": u.UUID, "email": u.Name, "level": 0}
			if n.Flow != "" {
				c["flow"] = n.Flow
			}
			clients = append(clients, c)
		case "vmess":
			clients = append(clients, map[string]any{"id": u.UUID, "email": u.Name, "level": 0,
				"alterId": 0, "security": "auto"})
		case "trojan":
			clients = append(clients, map[string]any{"password": u.Password, "email": u.Name, "level": 0})
		case "shadowsocks":
			key, err := s.UserNodeKey(u.ID, n)
			if err != nil {
				return nil, err
			}
			clients = append(clients, map[string]any{"password": key, "email": u.Name, "level": 0})
		case "hysteria":
			key, err := s.UserNodeKey(u.ID, n) // Hysteria2 每用户每节点一把 PSK 当 auth
			if err != nil {
				return nil, err
			}
			clients = append(clients, map[string]any{"auth": key, "email": u.Name, "level": 0})
		default:
			return nil, fmt.Errorf("不支持的协议: %s", n.Proto)
		}
	}

	listen := n.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}

	if n.Proto == "shadowsocks" {
		return map[string]any{
			"tag": n.Tag, "listen": listen, "port": n.Port, "protocol": "shadowsocks",
			"settings": map[string]any{"network": "tcp,udp", "method": n.Method,
				"password": n.SSKey, "clients": clients},
			"sniffing": cloneSniffing(),
		}, nil
	}

	if n.Proto == "hysteria" {
		return map[string]any{
			"tag": n.Tag, "listen": listen, "port": n.Port, "protocol": "hysteria",
			"settings": map[string]any{"version": 2, "clients": clients},
			"streamSettings": map[string]any{
				"network": "hysteria", "security": "tls",
				"tlsSettings": map[string]any{
					"serverNames": CSV(n.SNI), "alpn": []any{"h3"},
					"certificates": []any{map[string]any{
						"certificateFile": n.CertFile, "keyFile": n.KeyFile}},
				},
			},
			"sniffing": cloneSniffing(),
		}, nil
	}

	stream := map[string]any{"network": n.Net, "security": n.Security}
	switch n.Security {
	case "reality":
		shortIDs := []any{""}
		if n.ShortID != "" {
			shortIDs = []any{n.ShortID}
		}
		stream["realitySettings"] = map[string]any{
			"show": false, "dest": n.Dest, "xver": 0,
			"serverNames": CSV(n.SNI), "privateKey": n.PrivKey, "shortIds": shortIDs,
		}
	case "tls":
		certs := []any{}
		if n.CertFile != "" {
			certs = []any{map[string]any{"certificateFile": n.CertFile, "keyFile": n.KeyFile}}
		}
		serverName := n.SNI
		if serverName == "" {
			serverName = n.WSHost
		}
		stream["tlsSettings"] = map[string]any{
			"serverName": serverName, "certificates": certs,
			"alpn": []any{"h2", "http/1.1"}, "minVersion": "1.2",
		}
	}

	switch n.Net {
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
		xh := map[string]any{"path": orSlash(n.Path), "mode": "auto"}
		if n.WSHost != "" {
			xh["host"] = n.WSHost
		}
		stream["xhttpSettings"] = xh
	case "grpc":
		stream["grpcSettings"] = map[string]any{"serviceName": n.GRPCService}
	}

	settingsBlock := map[string]any{"clients": clients}
	if n.Proto == "vless" {
		settingsBlock["decryption"] = "none"
	}
	return map[string]any{
		"tag": n.Tag, "listen": listen, "port": n.Port, "protocol": n.Proto,
		"settings": settingsBlock, "streamSettings": stream,
		"sniffing": cloneSniffing(), "clientStats": true,
	}, nil
}

// BuildConfig 生成完整的 xray config.json。
func (s *Store) BuildConfig(st Settings) (map[string]any, error) {
	nodes, err := s.Nodes(true)
	if err != nil {
		return nil, err
	}
	inbounds := []any{}
	for _, n := range nodes {
		if probs := NodeProblems(n); len(probs) > 0 {
			continue // 有问题的节点跳过，别陪葬
		}
		users, err := s.ActiveUsersForNode(n)
		if err != nil {
			return nil, err
		}
		if len(users) == 0 {
			continue
		}
		ib, err := s.BuildInbound(n, users)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, ib)
	}

	strategy := st.OutboundDomainStrategy
	if strategy == "" {
		strategy = "UseIPv4"
	}
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning", "access": "none",
			"error": filepath.Join(Home(), "xray-error.log")},
		"api": map[string]any{"tag": "api", "listen": st.XrayAPI,
			"services": []any{"StatsService", "HandlerService", "ReflectionService"}},
		"stats": map[string]any{},
		"policy": map[string]any{
			"levels": map[string]any{"0": map[string]any{
				"statsUserUplink": true, "statsUserDownlink": true}},
			"system": map[string]any{
				"statsInboundUplink": true, "statsInboundDownlink": true,
				"statsOutboundUplink": false, "statsOutboundDownlink": false},
		},
		"inbounds": inbounds,
		// Xray 26.x 起 freedom.settings.domainStrategy 已废弃，值写在 sockopt 上
		"outbounds": []any{
			map[string]any{"protocol": "freedom", "tag": "direct", "settings": map[string]any{},
				"streamSettings": map[string]any{"sockopt": map[string]any{
					"domainStrategy": strategy}}},
			map[string]any{"protocol": "blackhole", "tag": "block"},
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules": []any{map[string]any{"type": "field", "ip": []any{"geoip:private"},
				"outboundTag": "block"}},
		},
	}
	buildWarp(cfg, st)
	buildChain(cfg, st)
	return cfg, nil
}

// buildChain 把指定域名链式转发到另一台落地。
func buildChain(cfg map[string]any, st Settings) {
	if !st.ChainEnable || st.ChainServer == "" || st.ChainPort == 0 {
		return
	}
	srv := map[string]any{"address": st.ChainServer, "port": st.ChainPort,
		"method": orDefault(st.ChainMethod, "2022-blake3-aes-128-gcm")}
	if st.ChainPassword != "" {
		srv["password"] = st.ChainPassword
	}
	appendOutbound(cfg, map[string]any{
		"protocol": "shadowsocks", "tag": "chain",
		"settings": map[string]any{"servers": []any{srv}},
		// 链路对端是家宽动态 IPv4，优先 AAAA 会连不上
		"streamSettings": map[string]any{"sockopt": map[string]any{"domainStrategy": "UseIPv4"}},
	})
	if doms := CSV(st.ChainDomains); len(doms) > 0 {
		insertRoutingRule(cfg, map[string]any{"type": "field",
			"domain": prefixDomains(doms), "outboundTag": "chain"})
	}
}

// buildWarp 生成 WARP 出站（优先本地 SOCKS，否则 xray 原生 wireguard 链式出站）。
func buildWarp(cfg map[string]any, st Settings) {
	if !st.WarpEnable {
		return
	}
	doms := CSV(st.WarpDomains)
	if host, port, ok := splitHostPort(st.WarpSocks); ok {
		appendOutbound(cfg, map[string]any{"protocol": "socks", "tag": "warp",
			"settings": map[string]any{"servers": []any{map[string]any{
				"address": host, "port": port}}}})
		if len(doms) > 0 {
			insertRoutingRule(cfg, map[string]any{"type": "field",
				"domain": prefixDomains(doms), "outboundTag": "warp"})
		}
		return
	}
	if st.WarpPrivateKey == "" {
		return
	}
	var addr []any
	for _, c := range []struct{ key, cidr string }{
		{st.WarpAddressV4, "/32"}, {st.WarpAddressV6, "/128"}} {
		v := strings.TrimSpace(c.key)
		if v == "" {
			continue
		}
		if !strings.Contains(v, "/") {
			v += c.cidr
		}
		addr = append(addr, v)
	}
	if len(addr) == 0 {
		return
	}
	wgSettings := map[string]any{
		"secretKey": st.WarpPrivateKey,
		"address":   addr,
		"peers": []any{map[string]any{
			"publicKey":  orDefault(st.WarpPeerKey, "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="),
			"allowedIPs": []any{"0.0.0.0/0", "::/0"},
			"endpoint":   orDefault(st.WarpEndpoint, "engage.cloudflareclient.com:2408"),
		}},
		"mtu": 1280,
	}
	if r := ints(st.WarpReserved); len(r) == 3 {
		wgSettings["reserved"] = r
	}
	appendOutbound(cfg, map[string]any{"protocol": "wireguard", "tag": "warp",
		"settings": wgSettings})
	// 新版 Xray 用 sockopt.dialerProxy 做链式出站（旧的 proxySettings 已移除）
	appendOutbound(cfg, map[string]any{"protocol": "freedom", "tag": "warp-IPv4",
		"settings": map[string]any{}, "streamSettings": map[string]any{"sockopt": map[string]any{
			"dialerProxy": "warp", "domainStrategy": "UseIPv4"}}})
	appendOutbound(cfg, map[string]any{"protocol": "freedom", "tag": "warp-IPv6",
		"settings": map[string]any{}, "streamSettings": map[string]any{"sockopt": map[string]any{
			"dialerProxy": "warp", "domainStrategy": "UseIPv6"}}})
	if len(doms) > 0 {
		insertRoutingRule(cfg, map[string]any{"type": "field",
			"domain": prefixDomains(doms), "outboundTag": "warp-IPv4"})
	}
}

// ---- 小工具 ----

func cloneSniffing() map[string]any {
	m := make(map[string]any, len(DefaultSniffing))
	for k, v := range DefaultSniffing {
		m[k] = v
	}
	return m
}

func orSlash(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func prefixDomains(doms []string) []any {
	out := make([]any, 0, len(doms))
	for _, d := range doms {
		out = append(out, "domain:"+d)
	}
	return out
}

func ints(s string) []int {
	var out []int
	for _, f := range strings.Fields(strings.ReplaceAll(s, ",", " ")) {
		var n int
		if _, err := fmt.Sscanf(f, "%d", &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func splitHostPort(s string) (string, int, bool) {
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return "", 0, false
	}
	var port int
	if _, err := fmt.Sscanf(s[i+1:], "%d", &port); err != nil || port == 0 {
		return "", 0, false
	}
	return s[:i], port, true
}

func outbounds(cfg map[string]any) []any {
	return cfg["outbounds"].([]any)
}

func appendOutbound(cfg map[string]any, o map[string]any) {
	cfg["outbounds"] = append(outbounds(cfg), o)
}

func insertRoutingRule(cfg map[string]any, rule map[string]any) {
	r := cfg["routing"].(map[string]any)
	rules := r["rules"].([]any)
	r["rules"] = append([]any{rule}, rules...)
}
