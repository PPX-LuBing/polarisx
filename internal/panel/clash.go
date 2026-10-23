package panel

import (
	"fmt"
	"strings"
	"time"
)

// ProductName 出现在订阅头部注释里（Python 版是 ixpanel）。
const ProductName = "polarisx"

// yamlQuote 与 Python 版一致：双引号包裹，转义反斜杠与双引号。
func yamlQuote(v any) string {
	s := fmt.Sprint(v)
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// ClashProxy 生成 mihomo 的一条 proxies 条目（Python 版 clash_proxy 的等价物）。
func (s *Store) ClashProxy(n Node, u User, st Settings, e *Entry, name string) (string, error) {
	var ent Entry
	if e != nil {
		ent = *e
	} else {
		ents := EntriesOf(n, st)
		if len(ents) == 0 {
			return "", fmt.Errorf("节点 %s 没有可用入口", n.Tag)
		}
		ent = ents[0]
	}
	if name == "" {
		name = DisplayName(n, &u, st)
	}
	lines := []string{
		"    - name: " + yamlQuote(name),
		"      server: " + yamlQuote(ent.Host),
		fmt.Sprintf("      port: %d", ent.Port),
		"      udp: true",
	}
	wsOpts := func(extra ...string) {
		lines = append(lines, "      ws-opts:", "        path: "+yamlQuote(orSlash(n.Path)))
		if n.WSHost != "" {
			lines = append(lines, "        headers:", "          Host: "+yamlQuote(n.WSHost))
		}
		lines = append(lines, extra...)
	}

	switch n.Proto {
	case "shadowsocks":
		pw, err := s.SSClientPassword(u, n)
		if err != nil {
			return "", err
		}
		lines = append(lines, "      type: ss", "      cipher: "+n.Method,
			"      password: "+yamlQuote(pw))
		return strings.Join(lines, "\n"), nil

	case "hysteria":
		auth, err := s.UserNodeKey(u.ID, n)
		if err != nil {
			return "", err
		}
		lines = append(lines, "      type: hysteria2", "      password: "+yamlQuote(auth),
			"      sni: "+yamlQuote(firstNonEmpty(n.EntrySNI, n.SNI)),
			"      skip-cert-verify: false")
		if n.Hy2Up != 0 {
			lines = append(lines, fmt.Sprintf("      up: \"%d Mbps\"", n.Hy2Up))
		}
		if n.Hy2Down != 0 {
			lines = append(lines, fmt.Sprintf("      down: \"%d Mbps\"", n.Hy2Down))
		}
		if fp := NodeCertFP(n); fp != "" {
			lines = append(lines, "      fingerprint: "+fp)
		}
		return strings.Join(lines, "\n"), nil

	case "vless":
		lines = append(lines, "      type: vless", "      uuid: "+u.UUID,
			"      network: "+n.Net)
		switch n.Net {
		case "ws":
			wsOpts()
		case "httpupgrade":
			wsOpts("        v2ray-http-upgrade: true")
		case "xhttp":
			lines = append(lines, "      xhttp-opts:", "        path: "+yamlQuote(orSlash(n.Path)),
				"        mode: "+yamlQuote("auto"))
			if n.WSHost != "" {
				lines = append(lines, "        host: "+yamlQuote(n.WSHost))
			}
		}
		switch n.Security {
		case "reality":
			lines = append(lines, "      tls: true", "      servername: "+yamlQuote(n.SNI),
				"      client-fingerprint: "+orDefault(n.Fingerprint, "chrome"),
				"      reality-opts:",
				"        public-key: "+yamlQuote(n.PubKey),
				"        short-id: "+yamlQuote(n.ShortID))
			if n.Flow != "" {
				lines = append(lines, "      flow: "+n.Flow)
			}
		case "tls":
			lines = append(lines, "      tls: true",
				"      servername: "+yamlQuote(firstNonEmpty(n.EntrySNI, n.SNI, n.WSHost)),
				"      client-fingerprint: "+orDefault(n.Fingerprint, "chrome"))
			if fp := NodeCertFP(n); fp != "" {
				lines = append(lines, "      fingerprint: "+fp)
			}
		}

	case "vmess":
		lines = append(lines, "      type: vmess", "      uuid: "+u.UUID, "      alterId: 0",
			"      cipher: auto", "      network: "+n.Net)
		if n.Net == "ws" {
			wsOpts()
		}
		if n.Security == "tls" {
			lines = append(lines, "      tls: true",
				"      servername: "+yamlQuote(firstNonEmpty(n.EntrySNI, n.SNI, n.WSHost)))
		}

	case "trojan":
		lines = append(lines, "      type: trojan", "      password: "+yamlQuote(u.Password),
			"      network: "+n.Net, "      tls: true",
			"      sni: "+yamlQuote(firstNonEmpty(n.EntrySNI, n.SNI, n.WSHost)))
		if fp := NodeCertFP(n); fp != "" {
			lines = append(lines, "      fingerprint: "+fp)
		}
		if n.Net == "ws" {
			wsOpts()
		}
	}
	return strings.Join(lines, "\n"), nil
}

// ClashYAML 生成整份 mihomo 订阅（Python 版 clash_yaml 的等价物）。
// now 由调用方传入，便于测试与 Python 版对齐时间戳。
func (s *Store) ClashYAML(u User, st Settings, now time.Time) (string, error) {
	nodes, err := s.NodesForUser(u, true)
	if err != nil {
		return "", err
	}
	var blocks, names []string
	for _, n := range nodes {
		for _, v := range EntryVariants(n, st, &u) {
			b, err := s.ClashProxy(n, u, st, &v.Entry, v.Name)
			if err != nil {
				return "", err
			}
			blocks = append(blocks, b)
			names = append(names, v.Name)
		}
	}
	head := []string{
		fmt.Sprintf("# %s subscription for %s", ProductName, u.Name),
		"# 生成时间 " + now.Format("2006-01-02 15:04:05"),
		"mixed-port: 7890",
		"allow-lan: false",
		"mode: rule",
		"log-level: info",
		"tcp-concurrent: true",
		"unified-delay: true",
		"proxies:",
	}
	body := "    []"
	if len(blocks) > 0 {
		body = strings.Join(blocks, "\n")
	}

	preset := st.ClashRules
	if preset == "" {
		preset = "standard"
	}
	rules, ok := RulePresets[preset]
	if !ok {
		preset, rules = "standard", RulePresets["standard"]
	}
	var expanded []string
	for _, r := range rules {
		if r == "RULE-PORT-PLACEHOLDER" {
			for _, p := range CSV(st.DirectPorts) {
				if isDigits(p) {
					expanded = append(expanded, "DST-PORT,"+p+",DIRECT")
				}
			}
			continue
		}
		expanded = append(expanded, r)
	}
	rules = expanded

	tail := []string{"proxy-groups:", "  - name: PROXY", "    type: select", "    proxies:"}
	if len(names) > 0 {
		for _, n := range names {
			tail = append(tail, "      - "+yamlQuote(n))
		}
	} else {
		tail = append(tail, "      - DIRECT")
	}
	tail = append(tail, "      - DIRECT")

	providers, hasProviders := PresetProviders[preset]
	groups, hasGroups := PresetGroups[preset]

	if hasProviders && len(names) >= 2 {
		// 多节点时给自动选择/故障转移，省得手动切
		tail = append(tail, "  - name: AUTO", "    type: url-test",
			"    url: http://www.gstatic.com/generate_204", "    interval: 300",
			"    tolerance: 50", "    proxies:")
		for _, n := range names {
			tail = append(tail, "      - "+yamlQuote(n))
		}
		tail = append(tail, "  - name: FALLBACK", "    type: fallback",
			"    url: http://www.gstatic.com/generate_204", "    interval: 300", "    proxies:")
		for _, n := range names {
			tail = append(tail, "      - "+yamlQuote(n))
		}
	}
	if hasGroups {
		for _, g := range groups {
			tail = append(tail, "  - name: "+g.Name, "    type: select", "    proxies:")
			for _, m := range g.Members {
				tail = append(tail, "      - "+m)
			}
		}
	}
	if hasProviders {
		tail = append(tail, "rule-providers:")
		for _, p := range providers {
			// 名字里带 ! 之类特殊字符要加引号，否则 YAML 会当成 tag
			key := p.Name
			if !isPlainKey(key) {
				key = "\"" + key + "\""
			}
			tail = append(tail, "  "+key+":", "    type: http",
				"    behavior: "+p.Behavior, "    format: mrs",
				"    url: \""+p.URL+"\"", "    path: ./ruleset/"+p.File,
				"    interval: 86400")
		}
	}
	tail = append(tail, "rules:")
	for _, r := range rules {
		tail = append(tail, "  - "+r)
	}
	tail = append(tail, "")

	return strings.Join(append(append(head, body), tail...), "\n"), nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isPlainKey(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '_' || c == '-') {
			return false
		}
	}
	return true
}
