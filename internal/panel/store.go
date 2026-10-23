package panel

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"

	_ "modernc.org/sqlite" // 纯 Go 驱动：无 cgo，才能编出单静态二进制
)

// schema 与 Python 版逐字一致（含注释掉的列语义），保证两边读写同一份 panel.db。
const schema = `
CREATE TABLE IF NOT EXISTS nodes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    tag         TEXT UNIQUE NOT NULL,
    remark      TEXT DEFAULT '',
    proto       TEXT NOT NULL,
    net         TEXT NOT NULL,
    security    TEXT NOT NULL,
    listen      TEXT DEFAULT '0.0.0.0',
    port        INTEGER NOT NULL,
    enabled     INTEGER DEFAULT 1,
    entry_host  TEXT DEFAULT '',
    entry_port  INTEGER DEFAULT 0,
    entry_sni   TEXT DEFAULT '',
    dest        TEXT DEFAULT '',
    sni         TEXT DEFAULT '',
    priv_key    TEXT DEFAULT '',
    pub_key     TEXT DEFAULT '',
    short_id    TEXT DEFAULT '',
    fingerprint TEXT DEFAULT 'chrome',
    spider_x    TEXT DEFAULT '/',
    path        TEXT DEFAULT '',
    ws_host     TEXT DEFAULT '',
    grpc_service TEXT DEFAULT '',
    cert_file   TEXT DEFAULT '',
    key_file    TEXT DEFAULT '',
    method      TEXT DEFAULT '',
    ss_key      TEXT DEFAULT '',
    flow        TEXT DEFAULT '',
    hy2_up      INTEGER DEFAULT 0,
    hy2_down    INTEGER DEFAULT 0,
    created     TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS users (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT UNIQUE NOT NULL,
    uuid        TEXT NOT NULL,
    password    TEXT NOT NULL,
    token       TEXT UNIQUE NOT NULL,
    quota_gb    REAL DEFAULT 0,
    used_up     INTEGER DEFAULT 0,
    used_down   INTEGER DEFAULT 0,
    expire_at   TEXT DEFAULT '',
    enable      INTEGER DEFAULT 1,
    note        TEXT DEFAULT '',
    disabled_reason TEXT DEFAULT '',
    last_reset  TEXT DEFAULT '',
    created     TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS user_nodes (
    user_id     INTEGER NOT NULL,
    node_id     INTEGER NOT NULL,
    PRIMARY KEY (user_id, node_id)
);

CREATE TABLE IF NOT EXISTS user_node_keys (
    user_id     INTEGER NOT NULL,
    node_id     INTEGER NOT NULL,
    key         TEXT NOT NULL,
    PRIMARY KEY (user_id, node_id)
);

CREATE TABLE IF NOT EXISTS raw_stats (
    email       TEXT PRIMARY KEY,
    up          INTEGER DEFAULT 0,
    down        INTEGER DEFAULT 0,
    updated     TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT DEFAULT '',
    level       TEXT DEFAULT 'info',
    user_name   TEXT DEFAULT '',
    message     TEXT DEFAULT ''
);
`

// migrations 老库缺的列（Python 版 add 字典的等价物）。
var migrations = []struct {
	table string
	cols  [][2]string // {列名, 定义}
}{
	{"nodes", [][2]string{{"method", "TEXT DEFAULT ''"}, {"ss_key", "TEXT DEFAULT ''"},
		{"hy2_up", "INTEGER DEFAULT 0"}, {"hy2_down", "INTEGER DEFAULT 0"}}},
	{"users", [][2]string{{"disabled_reason", "TEXT DEFAULT ''"}, {"note", "TEXT DEFAULT ''"},
		{"last_reset", "TEXT DEFAULT ''"}}},
}

// Store 是 panel.db 的访问层。
type Store struct{ db *sql.DB }

// Open 打开（必要时创建）panel.db 并建表/补列。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // 单机面板，串行最省心
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("建表失败: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层连接（统计/额度等模块要用）。
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	for _, m := range migrations {
		have := map[string]bool{}
		rows, err := s.db.Query("PRAGMA table_info(" + m.table + ")")
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			have[name] = true
		}
		rows.Close()
		for _, c := range m.cols {
			if have[c[0]] {
				continue
			}
			if _, err := s.db.Exec("ALTER TABLE " + m.table + " ADD COLUMN " + c[0] + " " + c[1]); err != nil {
				return fmt.Errorf("补列 %s.%s 失败: %w", m.table, c[0], err)
			}
		}
	}
	return nil
}

// Node 是一台落地机的入站节点（对应 nodes 表）。
type Node struct {
	ID                                  int64
	Tag, Remark, Proto, Net, Security   string
	Listen                              string
	Port, Enabled, EntryPort            int
	EntryHost, EntrySNI                 string
	Dest, SNI, PrivKey, PubKey, ShortID string
	Fingerprint, SpiderX                string
	Path, WSHost, GRPCService           string
	CertFile, KeyFile                   string
	Method, SSKey, Flow                 string
	Hy2Up, Hy2Down                      int
	Created                             string
}

// User 是一个订阅用户（对应 users 表）。
type User struct {
	ID                              int64
	Name, UUID, Password, Token     string
	QuotaGB                         float64
	UsedUp, UsedDown                int64
	ExpireAt                        string
	Enable                          int
	Note, DisabledReason, LastReset string
	Created                         string
}

const nodeCols = `id, tag, remark, proto, net, security, listen, port, enabled, entry_host,
entry_port, entry_sni, dest, sni, priv_key, pub_key, short_id, fingerprint, spider_x,
path, ws_host, grpc_service, cert_file, key_file, method, ss_key, flow, hy2_up, hy2_down, created`

const userCols = `id, name, uuid, password, token, quota_gb, used_up, used_down, expire_at,
enable, note, disabled_reason, last_reset, created`

func scanNode(sc interface{ Scan(...any) error }) (Node, error) {
	var n Node
	err := sc.Scan(&n.ID, &n.Tag, &n.Remark, &n.Proto, &n.Net, &n.Security, &n.Listen, &n.Port,
		&n.Enabled, &n.EntryHost, &n.EntryPort, &n.EntrySNI, &n.Dest, &n.SNI, &n.PrivKey,
		&n.PubKey, &n.ShortID, &n.Fingerprint, &n.SpiderX, &n.Path, &n.WSHost, &n.GRPCService,
		&n.CertFile, &n.KeyFile, &n.Method, &n.SSKey, &n.Flow, &n.Hy2Up, &n.Hy2Down, &n.Created)
	return n, err
}

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	err := sc.Scan(&u.ID, &u.Name, &u.UUID, &u.Password, &u.Token, &u.QuotaGB, &u.UsedUp,
		&u.UsedDown, &u.ExpireAt, &u.Enable, &u.Note, &u.DisabledReason, &u.LastReset, &u.Created)
	return u, err
}

// Nodes 返回节点列表（enabledOnly=true 时只要启用的），按 id 排序。
func (s *Store) Nodes(enabledOnly bool) ([]Node, error) {
	q := "SELECT " + nodeCols + " FROM nodes"
	if enabledOnly {
		q += " WHERE enabled=1"
	}
	q += " ORDER BY id"
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Users 返回全部用户，按 id 排序。
func (s *Store) Users() ([]User, error) {
	rows, err := s.db.Query("SELECT " + userCols + " FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserByName 按 name 或 token 查用户（与 Python 版 get_user 一致）。
func (s *Store) UserByName(nameOrToken string) (User, error) {
	return scanUser(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE name=? OR token=?",
		nameOrToken, nameOrToken))
}

// UserNodeIDs 返回该用户被绑定的节点 id（空 = 不限制，全部节点生效）。
func (s *Store) UserNodeIDs(userID int64) ([]int64, error) {
	rows, err := s.db.Query("SELECT node_id FROM user_nodes WHERE user_id=?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ActiveUsersForNode 返回该节点上应生效的用户（启用 + 未被节点绑定排除）。
// 注意：额度/过期不在这里判断——Python 版靠 enforce 主动禁用用户来落地。
func (s *Store) ActiveUsersForNode(n Node) ([]User, error) {
	users, err := s.Users()
	if err != nil {
		return nil, err
	}
	var out []User
	for _, u := range users {
		if u.Enable == 0 {
			continue
		}
		ids, err := s.UserNodeIDs(u.ID)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 && !containsID(ids, n.ID) {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// Event 是 events 表里的一行。
type Event struct {
	ID       int64  `json:"id"`
	TS       string `json:"ts"`
	Level    string `json:"level"`
	UserName string `json:"user_name"`
	Message  string `json:"message"`
}

// Events 取最近 n 条事件（倒序）。n<=0 时给个合理默认。
func (s *Store) Events(n int) ([]Event, error) {
	if n <= 0 {
		n = 30
	}
	rows, err := s.db.Query(
		"SELECT id, ts, level, user_name, message FROM events ORDER BY id DESC LIMIT ?", n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TS, &e.Level, &e.UserName, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventCount 事件总数（status 里显示）。
func (s *Store) EventCount() int {
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	return n
}

// UserNodeKey 取（或生成）某用户在某节点上的专属 PSK，SS2022 与 Hysteria2 共用这份存储。
func (s *Store) UserNodeKey(userID int64, n Node) (string, error) {
	var key string
	err := s.db.QueryRow("SELECT key FROM user_node_keys WHERE user_id=? AND node_id=?",
		userID, n.ID).Scan(&key)
	if err == nil {
		return key, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	size := SS2022KeySize[n.Method]
	if size == 0 {
		size = 32
	}
	key, err = genPSK(size)
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec("INSERT OR REPLACE INTO user_node_keys(user_id, node_id, key) VALUES (?,?,?)",
		userID, n.ID, key); err != nil {
		return "", err
	}
	return key, nil
}

// SS2022KeySize 是 SS2022 各加密方式的密钥字节数。
var SS2022KeySize = map[string]int{
	"2022-blake3-aes-128-gcm":       16,
	"2022-blake3-aes-256-gcm":       32,
	"2022-blake3-chacha20-poly1305": 32,
}

// genPSK 等价于 openssl rand -base64 <n>，与 shadowsocks-rust 兼容。
func genPSK(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// GenPSK 导出给命令层用（建节点/建用户时要生成密钥）。
func GenPSK(n int) (string, error) { return genPSK(n) }
