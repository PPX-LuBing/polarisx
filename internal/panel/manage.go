package panel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NodeFields 可改的节点字段（与 nodes 表列名一致）。
var NodeFields = []string{
	"tag", "remark", "proto", "net", "security", "listen", "port", "enabled",
	"entry_host", "entry_port", "entry_sni", "dest", "sni", "priv_key", "pub_key",
	"short_id", "fingerprint", "spider_x", "path", "ws_host", "grpc_service",
	"cert_file", "key_file", "method", "ss_key", "flow", "hy2_up", "hy2_down",
}

// UserFields 可改的用户字段。
var UserFields = []string{"quota_gb", "expire_at", "note", "enable", "name"}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{2,48}$`)

// NewUUID 生成 uuid4。
func NewUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

// NewToken 生成随机 token（字母数字，n 位）——订阅地址里用它。
func NewToken(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

// NodeByTag 按 tag 取节点。
func (s *Store) NodeByTag(tag string) (Node, error) {
	return scanNode(s.db.QueryRow("SELECT "+nodeCols+" FROM nodes WHERE tag=?", tag))
}

// CreateNode 建节点（tag 唯一，端口必填）。
func (s *Store) CreateNode(n Node) error {
	if n.Tag == "" {
		return fmt.Errorf("必须给节点一个 tag")
	}
	if _, err := s.NodeByTag(n.Tag); err == nil {
		return fmt.Errorf("节点 tag 已存在: %s", n.Tag)
	}
	if alias, ok := ProtoAliases[n.Proto]; ok { // hysteria2 / hy2 -> hysteria
		n.Proto = alias
	}
	if n.Listen == "" {
		n.Listen = "0.0.0.0"
	}
	if n.Fingerprint == "" {
		n.Fingerprint = "chrome"
	}
	if n.SpiderX == "" {
		n.SpiderX = "/"
	}
	if n.Enabled == 0 {
		n.Enabled = 1
	}
	// 空值归一成 none（结构体字面量可能没填）
	if n.Security == "" {
		n.Security = "none"
	}
	if n.Net == "" {
		n.Net = "tcp"
	}
	// trojan/hysteria 的 TLS 是协议的一部分
	if (n.Proto == "trojan" || n.Proto == "hysteria") &&
		(n.Security == "none" || n.Security == "reality") {
		n.Security = "tls"
	}
	_, err := s.db.Exec(`INSERT INTO nodes(tag, remark, proto, net, security, listen, port,
		enabled, entry_host, entry_port, entry_sni, dest, sni, priv_key, pub_key, short_id,
		fingerprint, spider_x, path, ws_host, grpc_service, cert_file, key_file, method,
		ss_key, flow, hy2_up, hy2_down, created)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.Tag, n.Remark, n.Proto, n.Net, n.Security, n.Listen, n.Port, n.Enabled,
		n.EntryHost, n.EntryPort, n.EntrySNI, n.Dest, n.SNI, n.PrivKey, n.PubKey, n.ShortID,
		n.Fingerprint, n.SpiderX, n.Path, n.WSHost, n.GRPCService, n.CertFile, n.KeyFile,
		n.Method, n.SSKey, n.Flow, n.Hy2Up, n.Hy2Down, nowStamp())
	if err != nil {
		return err
	}
	return s.Event("新增节点: "+n.Tag, "info", "")
}

// UpdateNode 改节点字段（pairs 形如 port=443）。
func (s *Store) UpdateNode(tag string, pairs []string) error {
	n, err := s.NodeByTag(tag)
	if err != nil {
		return fmt.Errorf("节点不存在: %s", tag)
	}
	sets, vals, err := buildSets(pairs, NodeFields, map[string]bool{"port": true,
		"entry_port": true, "enabled": true, "hy2_up": true, "hy2_down": true})
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		return fmt.Errorf("没有要改的字段")
	}
	vals = append(vals, n.ID)
	if _, err := s.db.Exec("UPDATE nodes SET "+strings.Join(sets, ", ")+" WHERE id=?", vals...); err != nil {
		return err
	}
	return s.Event("修改节点: "+tag+" "+strings.Join(pairs, " "), "info", "")
}

// DeleteNode 删节点（连带用户绑定与每用户密钥）。
func (s *Store) DeleteNode(tag string) error {
	n, err := s.NodeByTag(tag)
	if err != nil {
		return fmt.Errorf("节点不存在: %s", tag)
	}
	for _, q := range []string{"DELETE FROM user_nodes WHERE node_id=?",
		"DELETE FROM user_node_keys WHERE node_id=?", "DELETE FROM nodes WHERE id=?"} {
		if _, err := s.db.Exec(q, n.ID); err != nil {
			return err
		}
	}
	return s.Event("删除节点: "+tag, "warn", "")
}

// CreateUser 建用户（用户名/配额/到期/绑定节点）。
func (s *Store) CreateUser(name string, quotaGB float64, expire, note string, nodeTags []string) (User, error) {
	if !nameRe.MatchString(name) {
		return User{}, fmt.Errorf("用户名只能含字母数字 _ . @ - ，长度 2-48")
	}
	if _, err := s.UserByName(name); err == nil {
		return User{}, fmt.Errorf("用户已存在: %s", name)
	}
	uuid, err := NewUUID()
	if err != nil {
		return User{}, err
	}
	token, err := NewToken(16)
	if err != nil {
		return User{}, err
	}
	pw, err := NewToken(20)
	if err != nil {
		return User{}, err
	}
	res, err := s.db.Exec(`INSERT INTO users(name, uuid, password, token, quota_gb, used_up,
		used_down, expire_at, enable, note, disabled_reason, created, last_reset)
		VALUES (?,?,?,?,?,0,0,?,1,?,'',?,'')`,
		name, uuid, pw, token, quotaGB, expire, note, nowStamp())
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	for _, t := range nodeTags {
		n, err := s.NodeByTag(strings.TrimSpace(t))
		if err != nil {
			return User{}, fmt.Errorf("节点不存在: %s", t)
		}
		if _, err := s.db.Exec("INSERT OR REPLACE INTO user_nodes(user_id, node_id) VALUES (?,?)",
			id, n.ID); err != nil {
			return User{}, err
		}
	}
	if err := s.Event("新增用户: "+name, "info", name); err != nil {
		return User{}, err
	}
	return s.UserByName(name)
}

// UpdateUser 改用户字段（pairs 形如 quota_gb=100）。
func (s *Store) UpdateUser(name string, pairs []string) error {
	u, err := s.UserByName(name)
	if err != nil {
		return fmt.Errorf("用户不存在: %s", name)
	}
	sets, vals, err := buildSets(pairs, UserFields, nil)
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		return fmt.Errorf("没有要改的字段")
	}
	vals = append(vals, u.ID)
	if _, err := s.db.Exec("UPDATE users SET "+strings.Join(sets, ", ")+" WHERE id=?", vals...); err != nil {
		return err
	}
	// 改 enable 时清掉停用原因（与 Python 版一致）
	for _, p := range pairs {
		if strings.HasPrefix(p, "enable=") {
			if _, err := s.db.Exec("UPDATE users SET disabled_reason='' WHERE id=?", u.ID); err != nil {
				return err
			}
		}
	}
	return s.Event("修改用户: "+name+" "+strings.Join(pairs, " "), "info", name)
}

// DeleteUser 删用户（连带绑定与每用户密钥）。
func (s *Store) DeleteUser(name string) error {
	u, err := s.UserByName(name)
	if err != nil {
		return fmt.Errorf("用户不存在: %s", name)
	}
	for _, q := range []string{"DELETE FROM user_nodes WHERE user_id=?",
		"DELETE FROM user_node_keys WHERE user_id=?", "DELETE FROM users WHERE id=?"} {
		if _, err := s.db.Exec(q, u.ID); err != nil {
			return err
		}
	}
	return s.Event("删除用户: "+name, "warn", name)
}

// BindUserNodes 覆盖式设置用户的节点绑定（空 = 不限制，全部节点）。
func (s *Store) BindUserNodes(name string, nodeTags []string) error {
	u, err := s.UserByName(name)
	if err != nil {
		return fmt.Errorf("用户不存在: %s", name)
	}
	if _, err := s.db.Exec("DELETE FROM user_nodes WHERE user_id=?", u.ID); err != nil {
		return err
	}
	for _, t := range nodeTags {
		n, err := s.NodeByTag(strings.TrimSpace(t))
		if err != nil {
			return fmt.Errorf("节点不存在: %s", t)
		}
		if _, err := s.db.Exec("INSERT OR REPLACE INTO user_nodes(user_id, node_id) VALUES (?,?)",
			u.ID, n.ID); err != nil {
			return err
		}
	}
	return nil
}

// buildSets 把 k=v 列表编成 SQL 片段；intFields 里的键按整数写入。
func buildSets(pairs, allowed []string, intFields map[string]bool) ([]string, []any, error) {
	allow := map[string]bool{}
	for _, a := range allowed {
		allow[a] = true
	}
	var sets []string
	var vals []any
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, nil, fmt.Errorf("参数格式: key=value")
		}
		if !allow[k] {
			return nil, nil, fmt.Errorf("字段不可改: %s（可改: %s）", k, strings.Join(allowed, " / "))
		}
		switch {
		case intFields[k]:
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, nil, fmt.Errorf("%s 需要整数: %q", k, v)
			}
			vals = append(vals, n)
		case k == "quota_gb":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("quota_gb 需要数字: %q", v)
			}
			vals = append(vals, f)
		case k == "enable":
			if truthy(v) {
				vals = append(vals, 1)
			} else {
				vals = append(vals, 0)
			}
		default:
			vals = append(vals, v)
		}
		sets = append(sets, k+"=?")
	}
	return sets, vals, nil
}

// KickUser 把用户从运行中的 xray 摘掉（在线连接一并断开）。
func (s *Store) KickUser(st Settings, u User) error {
	nodes, err := s.NodesForUser(u, false)
	if err != nil {
		return err
	}
	var lastErr error
	for _, n := range nodes {
		rc, out, errb, err := XrayAPI(st, []string{"api", "rmu",
			"--server=" + st.XrayAPI, "-tag=" + n.Tag, u.Name}, 10*timeSecond)
		if err != nil || rc != 0 {
			lastErr = fmt.Errorf("rmu %s@%s 失败: %s %s %v", u.Name, n.Tag,
				strings.TrimSpace(out), strings.TrimSpace(errb), err)
		}
	}
	return lastErr
}

// SyncXrayUsers 写配置并用 adu 动态补用户（失败则回退重启），避免改用户时断开所有连接。
func (s *Store) SyncXrayUsers(st Settings) error {
	cfg, err := s.BuildConfig(st)
	if err != nil {
		return err
	}
	if err := WriteConfig(st, cfg, "", st.VerifyConfig); err != nil {
		return err
	}
	if !APIAvailable(st, 3*timeSecond) {
		return RestartXray(st, true)
	}
	nodes, err := s.Nodes(true)
	if err != nil {
		return err
	}
	ok := true
	for _, n := range nodes {
		if len(NodeProblems(n)) > 0 {
			continue
		}
		users, err := s.ActiveUsersForNode(n)
		if err != nil {
			return err
		}
		if len(users) == 0 {
			continue
		}
		inbound, err := s.BuildInbound(n, users)
		if err != nil {
			return err
		}
		// adu 要的是「完整 config 形状」：{"inbounds": [<完整 inbound>]}
		payload, err := marshalNoEscape(map[string]any{"inbounds": []any{inbound}}, "  ")
		if err != nil {
			return err
		}
		tmp := filepath.Join(Home(), "_adu_"+n.Tag+".json")
		if err := os.WriteFile(tmp, payload, 0o600); err != nil {
			return err
		}
		rc, out, errb, err := XrayAPI(st, []string{"api", "adu",
			"--server=" + st.XrayAPI, tmp}, 10*timeSecond)
		os.Remove(tmp)
		if err != nil || rc != 0 {
			ok = false
			_ = out
			_ = errb
		}
	}
	if !ok {
		return RestartXray(st, true)
	}
	return nil
}

// Apply 写配置（按需校验）并让运行中的 xray 生效。
func (s *Store) Apply(st Settings, restart bool) error {
	if restart {
		return s.SyncXrayUsers(st)
	}
	cfg, err := s.BuildConfig(st)
	if err != nil {
		return err
	}
	return WriteConfig(st, cfg, "", st.VerifyConfig)
}

func nowStamp() string { return timeNow().Format("2006-01-02 15:04:05") }

func timeNow() time.Time { return time.Now() }

const timeSecond = 10 * time.Second
