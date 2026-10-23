package panel

import (
	"fmt"
)

// SingboxUnsupportedNet sing-box 的 V2Ray Transport 没有 xhttp，这类节点导不出去。
var SingboxUnsupportedNet = map[string]bool{"xhttp": true}

// SingboxRuleSets 规则集（binary/.srs 体积小；经 PROXY 下载，墙内也能取到）。
var SingboxRuleSets = []any{
	map[string]any{"tag": "geosite-cn", "type": "remote", "format": "binary",
		"url":             "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs",
		"download_detour": "PROXY"},
	map[string]any{"tag": "geosite-ads", "type": "remote", "format": "binary",
		"url":             "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs",
		"download_detour": "PROXY"},
	map[string]any{"tag": "geoip-cn", "type": "remote", "format": "binary",
		"url":             "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs",
		"download_detour": "PROXY"},
}

// SingboxDNSDoH 优先 DoH：国内域名走阿里直连，其余走 Cloudflare 并挂 PROXY。
var SingboxDNSDoH = map[string]any{
	"servers": []any{
		map[string]any{"tag": "doh-cn", "type": "https", "server": "223.5.5.5", "path": "/dns-query"},
		map[string]any{"tag": "doh-proxy", "type": "https", "server": "1.1.1.1",
			"path": "/dns-query", "detour": "PROXY"},
	},
	"rules":    []any{map[string]any{"rule_set": "geosite-cn", "server": "doh-cn"}},
	"final":    "doh-proxy",
	"strategy": "prefer_ipv4",
}

// SingboxOutbound 生成一个 sing-box outbound；不支持的传输返回 nil。
func (s *Store) SingboxOutbound(n Node, u User, st Settings, e *Entry, name string) (map[string]any, error) {
	if SingboxUnsupportedNet[n.Net] {
		return nil, nil
	}
	var ent Entry
	if e != nil {
		ent = *e
	} else {
		ents := EntriesOf(n, st)
		if len(ents) == 0 {
			return nil, fmt.Errorf("节点 %s 没有可用入口", n.Tag)
		}
		ent = ents[0]
	}
	if name == "" {
		name = DisplayName(n, &u, st)
	}
	o := map[string]any{"tag": name, "server": ent.Host, "server_port": ent.Port}

	if n.Proto == "hysteria" {
		auth, err := s.UserNodeKey(u.ID, n)
		if err != nil {
			return nil, err
		}
		o["type"] = "hysteria2"
		o["password"] = auth
		if n.Hy2Up != 0 {
			o["up_mbps"] = n.Hy2Up
		}
		if n.Hy2Down != 0 {
			o["down_mbps"] = n.Hy2Down
		}
		tls := map[string]any{"enabled": true,
			"server_name": firstNonEmpty(n.EntrySNI, n.SNI), "alpn": []any{"h3"}}
		// 自签证书用**公钥**指纹（base64），与 Clash 的整证书 hex 不是同一种格式
		if pub := NodeCertPubKeyFP(n); pub != "" {
			tls["certificate_public_key_sha256"] = []any{pub}
		}
		o["tls"] = tls
		return o, nil
	}

	switch n.Proto {
	case "vless":
		o["type"] = "vless"
		o["uuid"] = u.UUID
		if n.Flow != "" {
			o["flow"] = n.Flow
		}
	case "vmess":
		o["type"] = "vmess"
		o["uuid"] = u.UUID
		o["security"] = "auto"
		o["alter_id"] = 0
	case "trojan":
		o["type"] = "trojan"
		o["password"] = u.Password
	case "shadowsocks":
		pw, err := s.SSClientPassword(u, n)
		if err != nil {
			return nil, err
		}
		o["type"] = "shadowsocks"
		o["method"] = n.Method
		o["password"] = pw
	default:
		return nil, nil
	}

	switch n.Net {
	case "ws":
		t := map[string]any{"type": "ws", "path": orSlash(n.Path)}
		if n.WSHost != "" {
			t["headers"] = map[string]any{"Host": n.WSHost}
		}
		o["transport"] = t
	case "httpupgrade":
		t := map[string]any{"type": "httpupgrade", "path": orSlash(n.Path)}
		if n.WSHost != "" {
			t["host"] = n.WSHost
		}
		o["transport"] = t
	case "grpc":
		o["transport"] = map[string]any{"type": "grpc", "service_name": n.GRPCService}
	}

	switch n.Security {
	case "reality":
		o["tls"] = map[string]any{
			"enabled": true, "server_name": n.SNI,
			"utls":    map[string]any{"enabled": true, "fingerprint": orDefault(n.Fingerprint, "chrome")},
			"reality": map[string]any{"enabled": true, "public_key": n.PubKey, "short_id": n.ShortID},
		}
	case "tls":
		tls := map[string]any{"enabled": true,
			"server_name": firstNonEmpty(n.EntrySNI, n.SNI, n.WSHost)}
		if fp := NodeCertFP(n); fp != "" {
			tls["certificate_sha256"] = fp
		}
		o["tls"] = tls
	}
	return o, nil
}

// NodeCertPubKeyFP 节点启用 TLS 时的**公钥**指纹（sing-box 用的格式）。
func NodeCertPubKeyFP(n Node) string {
	if n.Security != "tls" {
		return ""
	}
	return CertPubKeySHA256(n.CertFile)
}

// SingboxConfig 生成整份 sing-box 订阅，返回 (JSON 文本, 被跳过的节点 tag)。
func (s *Store) SingboxConfig(u User, st Settings) (string, []string, error) {
	nodes, err := s.NodesForUser(u, true)
	if err != nil {
		return "", nil, err
	}
	outs := []any{}
	var skipped []string
	for _, n := range nodes {
		for _, v := range EntryVariants(n, st, &u) {
			o, err := s.SingboxOutbound(n, u, st, &v.Entry, v.Name)
			if err != nil {
				return "", nil, err
			}
			if o == nil {
				if !containsStr(skipped, n.Tag) {
					skipped = append(skipped, n.Tag)
				}
				continue
			}
			outs = append(outs, o)
		}
	}
	tags := []any{}
	for _, o := range outs {
		tags = append(tags, o.(map[string]any)["tag"])
	}
	def := any("direct")
	if len(tags) > 0 {
		def = tags[0]
	}
	doc := map[string]any{
		"$schema":  "https://sing-box.app/schema.json",
		"log":      map[string]any{"level": "info", "timestamp": true},
		"inbounds": []any{map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080}},
		"outbounds": append(append([]any{}, outs...), map[string]any{
			"type": "selector", "tag": "PROXY",
			"outbounds": append(append([]any{}, tags...), "direct"), "default": def,
		}, map[string]any{"type": "direct", "tag": "direct"}),
		"route": map[string]any{
			// 1.12+ 要求：没有 domain_resolver 的出站会被 1.14 拒绝加载
			"default_domain_resolver": "doh-proxy",
			"rule_set":                SingboxRuleSets,
			"rules": []any{
				map[string]any{"action": "sniff"},
				map[string]any{"protocol": "dns", "action": "hijack-dns"},
				map[string]any{"ip_is_private": true, "action": "route", "outbound": "direct"},
				map[string]any{"rule_set": "geosite-ads", "action": "reject"},
				map[string]any{"rule_set": "geosite-cn", "action": "route", "outbound": "direct"},
				map[string]any{"rule_set": "geoip-cn", "action": "route", "outbound": "direct"},
			},
			"final": "PROXY",
		},
	}
	if st.SingboxDNS != "system" {
		doc["dns"] = SingboxDNSDoH
	} else {
		delete(doc["route"].(map[string]any), "default_domain_resolver")
	}
	b, err := marshalNoEscape(doc, "  ")
	if err != nil {
		return "", nil, err
	}
	return string(b), skipped, nil
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
