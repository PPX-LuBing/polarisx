package panel

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"strings"
)

// Entry 是节点对外的接入点（客户端实际连接地址）。
type Entry struct {
	Host string
	Port int
}

// EntriesOf 返回节点入口列表：entry_host 支持逗号分隔多入口；留空则退回
// server_host / 本机 IP。端口用 entry_port，0 表示跟 port 共用。
func EntriesOf(n Node, st Settings) []Entry {
	raw := n.EntryHost
	if raw == "" {
		raw = st.ServerHost
	}
	if raw == "" {
		raw = guessLocalIP()
	}
	port := n.EntryPort
	if port == 0 {
		port = n.Port
	}
	var out []Entry
	for _, h := range strings.Split(raw, ",") { // 与 Python 一致：只按半角逗号切
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, Entry{Host: h, Port: port})
		}
	}
	return out
}

// DisplayName 节点显示名（remark 优先，可带用户名后缀）。
func DisplayName(n Node, u *User, st Settings) string {
	name := n.Remark
	if name == "" {
		name = n.Tag
	}
	if u != nil && st.NodeNameWithUser {
		return name + "-" + u.Name
	}
	return name
}

// EntryVariant 是一条订阅条目：入口 + 显示名。
type EntryVariant struct {
	Entry Entry
	Name  string
}

// EntryVariants 多入口时展开成多条并加 -1/-2 后缀，单入口保持原名。
func EntryVariants(n Node, st Settings, u *User) []EntryVariant {
	ents := EntriesOf(n, st)
	base := DisplayName(n, u, st)
	if len(ents) == 1 {
		return []EntryVariant{{ents[0], base}}
	}
	out := make([]EntryVariant, 0, len(ents))
	for i, e := range ents {
		out = append(out, EntryVariant{e, fmt.Sprintf("%s-%d", base, i+1)})
	}
	return out
}

// NodesForUser 该用户可用的节点（未绑定 = 全部）。
func (s *Store) NodesForUser(u User, onlyEnabled bool) ([]Node, error) {
	nodes, err := s.Nodes(onlyEnabled)
	if err != nil {
		return nil, err
	}
	ids, err := s.UserNodeIDs(u.ID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nodes, nil
	}
	var out []Node
	for _, n := range nodes {
		if containsID(ids, n.ID) {
			out = append(out, n)
		}
	}
	return out, nil
}

// SSClientPassword SS2022 多用户：客户端密码是「服务端PSK:用户PSK」。
func (s *Store) SSClientPassword(u User, n Node) (string, error) {
	if _, ok := SS2022KeySize[n.Method]; !ok {
		return n.SSKey, nil
	}
	key, err := s.UserNodeKey(u.ID, n)
	if err != nil {
		return "", err
	}
	return n.SSKey + ":" + key, nil
}

// LinkFor 生成单个节点的分享链接（vless / vmess / trojan / ss / hysteria2）。
func (s *Store) LinkFor(n Node, u User, st Settings, e *Entry, name string) (string, error) {
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
	rmk := pyQuoteStrict(name)

	switch n.Proto {
	case "shadowsocks":
		pw, err := s.SSClientPassword(u, n)
		if err != nil {
			return "", err
		}
		ui := base64.RawURLEncoding.EncodeToString([]byte(n.Method + ":" + pw))
		return fmt.Sprintf("ss://%s@%s:%d#%s", ui, ent.Host, ent.Port, rmk), nil

	case "hysteria":
		auth, err := s.UserNodeKey(u.ID, n)
		if err != nil {
			return "", err
		}
		q := []string{"insecure=0", "sni=" + pyQuote(firstNonEmpty(n.EntrySNI, n.SNI))}
		if n.Hy2Up != 0 {
			q = append(q, fmt.Sprintf("upmbps=%d", n.Hy2Up))
		}
		if n.Hy2Down != 0 {
			q = append(q, fmt.Sprintf("downmbps=%d", n.Hy2Down))
		}
		if fp := NodeCertFP(n); fp != "" {
			// 自签证书：指纹写进链接，客户端免手工配置（两个键都写，不认的会被忽略）
			q = append(q, "pinSHA256="+fp, "hpkp="+fp)
		}
		return fmt.Sprintf("hysteria2://%s@%s:%d?%s#%s", pyQuote(auth), ent.Host, ent.Port,
			strings.Join(q, "&"), rmk), nil

	case "vless":
		q := []string{"encryption=none", "type=" + n.Net}
		switch n.Security {
		case "reality":
			q = append(q, "security=reality", "sni="+pyQuote(n.SNI),
				"fp="+n.Fingerprint, "pbk="+pyQuote(n.PubKey), "sid="+pyQuote(n.ShortID),
				"spx="+pyQuote(orDefault(n.SpiderX, "/")))
			if n.Flow != "" {
				q = append(q, "flow="+n.Flow)
			}
		case "tls":
			q = append(q, "security=tls",
				"sni="+pyQuote(firstNonEmpty(n.EntrySNI, n.SNI, n.WSHost)), "fp="+n.Fingerprint)
			if fp := NodeCertFP(n); fp != "" {
				q = append(q, "pcs="+fp)
			}
		default:
			q = append(q, "security=none")
		}
		q = append(q, transportQuery(n)...)
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s", u.UUID, ent.Host, ent.Port,
			strings.Join(q, "&"), rmk), nil

	case "vmess":
		obj := vmessObj{ // 字段顺序必须与 Python 版一致（vmess 链接是 base64 的 JSON）
			V: "2", PS: name, Add: ent.Host, Port: fmt.Sprint(ent.Port), ID: u.UUID,
			Aid: "0", Scy: "auto", Net: n.Net, Type: "none",
			Host: n.WSHost, Path: n.Path,
			TLS: tlsFlag(n.Security),
			SNI: firstNonEmpty(n.EntrySNI, n.SNI, n.WSHost),
		}
		if n.Security == "tls" {
			obj.PCS = NodeCertFP(n)
		}
		raw, err := marshalNoEscape(obj, "")
		if err != nil {
			return "", err
		}
		raw = []byte(strings.TrimRight(string(raw), "\n"))
		return "vmess://" + base64.StdEncoding.EncodeToString(raw), nil

	case "trojan":
		q := []string{"security=tls", "type=" + n.Net,
			"sni=" + pyQuote(firstNonEmpty(n.EntrySNI, n.SNI, n.WSHost)),
			"fp=" + n.Fingerprint}
		if fp := NodeCertFP(n); fp != "" {
			q = append(q, "pcs="+fp)
		}
		q = append(q, transportQuery(n)...)
		return fmt.Sprintf("trojan://%s@%s:%d?%s#%s", pyQuote(u.Password), ent.Host, ent.Port,
			strings.Join(q, "&"), rmk), nil
	}
	return "", fmt.Errorf("不支持的协议: %s", n.Proto)
}

// vmessObj 的字段顺序 = Python dict 的插入顺序（vmess 链接内容要逐字节一致）。
type vmessObj struct {
	V    string `json:"v"`
	PS   string `json:"ps"`
	Add  string `json:"add"`
	Port string `json:"port"`
	ID   string `json:"id"`
	Aid  string `json:"aid"`
	Scy  string `json:"scy"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni"`
	PCS  string `json:"pcs,omitempty"`
}

func tlsFlag(security string) string {
	if security == "tls" {
		return "tls"
	}
	return ""
}

func transportQuery(n Node) []string {
	switch n.Net {
	case "ws", "httpupgrade":
		return []string{"path=" + pyQuote(orSlash(n.Path)), "host=" + pyQuote(n.WSHost)}
	case "xhttp":
		q := []string{"path=" + pyQuote(orSlash(n.Path)), "mode=auto"}
		if n.WSHost != "" {
			q = append(q, "host="+pyQuote(n.WSHost))
		}
		return q
	case "grpc":
		return []string{"serviceName=" + pyQuote(n.GRPCService)}
	}
	return nil
}

// LinksForUser 该用户所有节点、所有入口的分享链接。
func (s *Store) LinksForUser(u User, st Settings, onlyEnabled bool) ([]string, error) {
	nodes, err := s.NodesForUser(u, onlyEnabled)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range nodes {
		// ponytail: 与 Python 版一致——这里不体检节点（缺私钥的 reality 节点照样发链接），
		// 也不看用户 enable（由订阅 HTTP 层拦）。两边都该收紧，属于独立的一次行为变更。
		for _, v := range EntryVariants(n, st, &u) {
			l, err := s.LinkFor(n, u, st, &v.Entry, v.Name)
			if err != nil {
				return nil, err
			}
			out = append(out, l)
		}
	}
	return out, nil
}

// SubPayload 生成订阅内容与 Content-Type。fmt: base64（默认）/ plain / json。
func (s *Store) SubPayload(u User, st Settings, format string) (string, string, error) {
	links, err := s.LinksForUser(u, st, true)
	if err != nil {
		return "", "", err
	}
	switch format {
	case "plain":
		return strings.Join(links, "\n") + "\n", "text/plain; charset=utf-8", nil
	case "json":
		body, err := marshalNoEscape(struct {
			Name  string   `json:"name"`
			Links []string `json:"links"`
		}{u.Name, links}, "  ")
		if err != nil {
			return "", "", err
		}
		return string(body), "application/json; charset=utf-8", nil
	default:
		body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n") + "\n"))
		return body + "\n", "text/plain; charset=utf-8", nil
	}
}

// ExpandTitle 展开订阅标题模板：{cc} {asn} {org} {host}，缺值占位符清掉。
func ExpandTitle(st Settings) string {
	t := st.SubTitle
	if t == "" {
		t = SubTitleDefault
	}
	for _, kv := range [][2]string{
		{"{cc}", st.ServerCC}, {"{asn}", st.ServerASN},
		{"{org}", st.ServerOrg}, {"{host}", st.ServerHost}} {
		t = strings.ReplaceAll(t, kv[0], kv[1])
	}
	for strings.Contains(t, "··") {
		t = strings.ReplaceAll(t, "··", "·")
	}
	for strings.Contains(t, "· ") {
		t = strings.ReplaceAll(t, "· ", "·")
	}
	t = strings.Trim(t, " ·-")
	if t == "" {
		return "定制"
	}
	return t
}

// ContentDisposition 下载文件名：ASCII 兜底 + RFC 5987 的 UTF-8 名（客户端显示中文名）。
func ContentDisposition(title, ext string) string {
	return fmt.Sprintf(`attachment; filename="custom%s"; filename*=UTF-8''%s`, ext, pyQuote(title))
}

// ---- 证书指纹 ----

// CertSHA256 整张证书（DER）的 SHA256，大写十六进制无冒号——与 openssl
// `x509 -fingerprint -sha256` 一致。用标准库算，不依赖 openssl 命令。
func CertSHA256(path string) string {
	cert, err := parseCert(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// CertPubKeySHA256 证书公钥的 SHA256（base64）——sing-box 的
// certificate_public_key_sha256 要这个格式，跟上面那张证书指纹不是一回事。
func CertPubKeySHA256(path string) string {
	cert, err := parseCert(path)
	if err != nil {
		return ""
	}
	der, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// NodeCertFP 节点启用 TLS 时的服务端证书指纹，否则空。
func NodeCertFP(n Node) string {
	if n.Security != "tls" {
		return ""
	}
	return CertSHA256(n.CertFile)
}

func parseCert(path string) (*x509.Certificate, error) {
	if path == "" {
		return nil, fmt.Errorf("空证书路径")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("不是 PEM 证书: %s", path)
	}
	return x509.ParseCertificate(blk.Bytes)
}

// ---- 小工具 ----

// pyQuote 等价于 Python 的 urllib.parse.quote(s)（默认 safe="/"）：保留
// A-Za-z0-9_.-~ 与 /，其余按 UTF-8 逐字节 %XX 大写。
func pyQuote(s string) string { return pyQuoteSafe(s, "/") }

// pyQuoteStrict 等价于 quote(s, safe="")——只有分享链接的 #remark 用它。
func pyQuoteStrict(s string) string { return pyQuoteSafe(s, "") }

func pyQuoteSafe(s, safe string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-~"
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 || strings.IndexByte(safe, c) >= 0 {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func guessLocalIP() string {
	c, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}
