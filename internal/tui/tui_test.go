package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PPX-LuBing/polarisx/internal/panel"
)

// newTestModel 用 parity 夹具建一个模型（探活用桩，别真起 xray）。
func newTestModel(t *testing.T) Model {
	t.Helper()
	root := filepath.Join("..", "..", "testdata")
	src, err := os.ReadFile(filepath.Join(root, "fixture", "panel.db"))
	if err != nil {
		t.Skipf("没有夹具（先跑 python3 tools/mkfixture.py）: %v", err)
	}
	db := filepath.Join(t.TempDir(), "panel.db")
	if err := os.WriteFile(db, src, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := panel.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	st := panel.Defaults()
	m := New(store, st)
	m.Probe = func(panel.Settings, time.Duration) bool { return true }
	return m
}

func key(m Model, s string) Model {
	var msg tea.KeyMsg
	switch s {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestMainMenuView(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	for _, want := range []string{panel.ProductName, panel.Version, "xray 在线",
		"节点 9/10", "用户 3", "总览与自检", "节点管理", "用户管理", "生成配置并重启 xray"} {
		if !strings.Contains(v, want) {
			t.Errorf("主菜单缺少 %q\n%s", want, v)
		}
	}
}

func TestNavigation(t *testing.T) {
	m := newTestModel(t)
	// ↓ ↓ Enter -> 用户管理
	m = key(m, "down")
	m = key(m, "down")
	m = key(m, "enter")
	if m.view != viewUsers {
		t.Fatalf("应进入用户视图，实际 %v", m.view)
	}
	v := m.View()
	for _, want := range []string{"alice", "bob", "carol", "停用"} {
		if !strings.Contains(v, want) {
			t.Errorf("用户视图缺少 %q\n%s", want, v)
		}
	}
	// q 返回主菜单
	m = key(m, "q")
	if m.view != viewMenu {
		t.Fatalf("q 应返回主菜单，实际 %v", m.view)
	}
	// ↑ Enter -> 节点管理
	m = key(m, "down")
	m = key(m, "enter")
	if m.view != viewNodes {
		t.Fatalf("应进入节点视图，实际 %v", m.view)
	}
	v = m.View()
	for _, want := range []string{"reality-tcp", "hy2", "ss2022", "vless-xhttp", "444"} {
		if !strings.Contains(v, want) {
			t.Errorf("节点视图缺少 %q\n%s", want, v)
		}
	}
	// 坏节点要有告警标记
	if !strings.Contains(v, "⚠") {
		t.Errorf("坏节点应显示告警\n%s", v)
	}
}

func TestQuit(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 4; i++ { // 移到「退出」
		m = key(m, "down")
	}
	m = key(m, "enter")
	if !m.Quit() {
		t.Error("选择退出后 Quit() 应为 true")
	}
}

func TestDoctorView(t *testing.T) {
	m := newTestModel(t)
	m = key(m, "enter") // 总览与自检
	if m.view != viewDoctor {
		t.Fatalf("应进入自检视图，实际 %v", m.view)
	}
	v := m.View()
	if !strings.Contains(v, "inbound") {
		t.Errorf("自检应报告可生成的 inbound 数\n%s", v)
	}
	if !strings.Contains(v, "bad-reality") {
		t.Errorf("自检应指出坏节点\n%s", v)
	}
}

// ---------------------------------------------------------------------------
// 写操作：表单驱动的增删改（TUI 之前只能看，这是补上的部分）
// ---------------------------------------------------------------------------

// typeText 往当前字段里逐字符输入。
func typeText(m Model, s string) Model {
	for _, r := range s {
		m = key(m, string(r))
	}
	return m
}

func TestTUIAddNodeViaForm(t *testing.T) {
	m := newTestModel(t)
	m = key(m, "down") // 节点管理
	m = key(m, "enter")
	before := len(mustNodes(t, m))

	m = key(m, "a")
	if m.form == nil {
		t.Fatal("按 a 应打开新增节点表单")
	}
	// 填 tag，其余用默认值（协议 vless / 传输 tcp / 安全 reality）
	m = typeText(m, "tui-test")
	for _, want := range []string{"新增节点", "tag", "协议", "安全", "reality"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("表单里应有 %q\n%s", want, m.View())
		}
	}
	// 光标移到端口字段（tag 后面依次是 协议/传输/安全/端口）
	for i := 0; i < 4; i++ {
		m = key(m, "down")
	}
	m = typeText(m, "8443")
	// 到入口地址
	m = key(m, "down")
	m = typeText(m, "1.2.3.4")
	// 提交（需要走到最后一个字段再回车）
	for i := 0; i < 3; i++ {
		m = key(m, "down")
	}
	m = key(m, "enter")
	if m.form != nil {
		t.Fatalf("提交后表单应关闭，错误: %s", m.form.err)
	}
	nodes := mustNodes(t, m)
	if len(nodes) != before+1 {
		t.Fatalf("节点数应从 %d 变成 %d，实际 %d", before, before+1, len(nodes))
	}
	var created *panel.Node
	for i := range nodes {
		if nodes[i].Tag == "tui-test" {
			created = &nodes[i]
		}
	}
	if created == nil {
		t.Fatal("没找到新建的节点")
	}
	if created.Port != 8443 || created.EntryHost != "1.2.3.4" {
		t.Errorf("字段没填对: %+v", created)
	}
	// reality 的密钥必须自动生成，否则这个节点根本连不上
	if created.Security == "reality" {
		if len(created.PrivKey) != 43 || len(created.PubKey) != 43 || len(created.ShortID) != 8 {
			t.Errorf("reality 密钥应自动生成: priv=%d pub=%d sid=%q",
				len(created.PrivKey), len(created.PubKey), created.ShortID)
		}
	}
}

func TestTUIFormValidationAndCancel(t *testing.T) {
	m := newTestModel(t)
	m = key(m, "down")
	m = key(m, "enter")
	before := len(mustNodes(t, m))

	// 空 tag 应被拦下并留在表单里
	m = key(m, "a")
	for i := 0; i < 6; i++ {
		m = key(m, "down")
	}
	m = key(m, "enter")
	if m.form == nil {
		t.Fatal("校验失败时不该关闭表单")
	}
	if !strings.Contains(m.View(), "tag 不能为空") {
		t.Errorf("应显示校验错误\n%s", m.View())
	}
	// 端口非法也要拦
	m = typeText(m, "abc")
	m = key(m, "down")
	for i := 0; i < 6; i++ {
		m = key(m, "down")
	}
	m = key(m, "enter")
	if m.form == nil {
		t.Fatal("端口非法时不该关闭表单")
	}
	// Esc 取消，什么都不该建
	m = key(m, "esc")
	if m.form != nil {
		t.Fatal("Esc 应关闭表单")
	}
	if got := len(mustNodes(t, m)); got != before {
		t.Errorf("取消后不该新建节点: %d -> %d", before, got)
	}
}

func TestTUIDeleteNodeNeedsConfirm(t *testing.T) {
	m := newTestModel(t)
	m = key(m, "down")
	m = key(m, "enter")
	before := len(mustNodes(t, m))
	tag := mustNodes(t, m)[0].Tag

	// 先按 n：应取消
	m = key(m, "d")
	if m.confirm == "" {
		t.Fatal("删除前应要确认")
	}
	m = key(m, "n")
	if m.confirm != "" || len(mustNodes(t, m)) != before {
		t.Error("按 n 应取消删除")
	}
	// 再按 y：应删掉
	m = key(m, "d")
	m = key(m, "y")
	nodes := mustNodes(t, m)
	if len(nodes) != before-1 {
		t.Fatalf("按 y 应删掉一个，%d -> %d", before, len(nodes))
	}
	for _, n := range nodes {
		if n.Tag == tag {
			t.Errorf("节点 %s 应已被删除", tag)
		}
	}
}

func TestTUIEditUserQuota(t *testing.T) {
	m := newTestModel(t)
	m = key(m, "down")
	m = key(m, "down")
	m = key(m, "enter") // 用户管理
	users, _ := m.Store.Users()
	if len(users) == 0 {
		t.Skip("夹具里没有用户")
	}
	target := users[0]

	m = key(m, "e")
	if m.form == nil {
		t.Fatal("按 e 应打开编辑表单")
	}
	// 清空额度字段再填新值
	for i := 0; i < 8; i++ {
		m = key(m, "backspace")
	}
	m = typeText(m, "7")
	for i := 0; i < 3; i++ {
		m = key(m, "down")
	}
	m = key(m, "enter")
	if m.form != nil {
		t.Fatalf("提交后应关闭: %v", m.form)
	}
	got, err := m.Store.UserByName(target.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.QuotaGB != 7 {
		t.Errorf("额度应改成 7，实际 %g", got.QuotaGB)
	}
}

func TestTUIAddUserDefaultsToAllNodes(t *testing.T) {
	m := newTestModel(t)
	m = key(m, "down")
	m = key(m, "down")
	m = key(m, "enter")
	m = key(m, "a")
	if m.form == nil {
		t.Fatal("按 a 应打开新增用户表单")
	}
	m = typeText(m, "tuiuser")
	for i := 0; i < 3; i++ {
		m = key(m, "down")
	}
	m = key(m, "enter")
	if m.form != nil {
		t.Fatalf("提交后应关闭: %v", m.form)
	}
	u, err := m.Store.UserByName("tuiuser")
	if err != nil {
		t.Fatal(err)
	}
	if u.QuotaGB != 100 {
		t.Errorf("默认额度应为 100，实际 %g", u.QuotaGB)
	}
	if u.Token == "" {
		t.Error("应自动生成订阅 token")
	}
	// 没绑节点 = 全部节点
	nodes, _ := m.Store.NodesForUser(u, true)
	if len(nodes) == 0 {
		t.Error("没绑节点应等于全部启用节点")
	}
}

// mustNodes 取节点列表（测试里出错就直接失败）。
func mustNodes(t *testing.T, m Model) []panel.Node {
	t.Helper()
	nodes, err := m.Store.Nodes(false)
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}
