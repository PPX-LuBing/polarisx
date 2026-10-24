package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PPX-LuBing/polarisx/internal/panel"
)

// newTestModel 用 golden 夹具建一个模型（探活用桩，别真起 xray）。
func newTestModel(t *testing.T) Model {
	t.Helper()
	root := filepath.Join("..", "..", "testdata")
	src, err := os.ReadFile(filepath.Join(root, "fixture", "panel.db"))
	if err != nil {
		t.Skipf("没有夹具（testdata fixtures are frozen）: %v", err)
	}
	// TUI 会读写 settings.json（改告警地址等），测试必须指向临时目录。
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	db := filepath.Join(dir, "panel.db")
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

// gotoMenu 按菜单项的名字进入，不要按固定次数下移——菜单加一项就全错位。
func gotoMenu(t *testing.T, m Model, label string) Model {
	t.Helper()
	idx := -1
	for i, item := range menuItems {
		if strings.Contains(item, label) {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("菜单里没有 %q（现有：%v）", label, menuItems)
	}
	if m.view != viewMenu {
		m = key(m, "q")
	}
	m.cursor = 0
	for i := 0; i < idx; i++ {
		m = key(m, "down")
	}
	return key(m, "enter")
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
	case "pgup":
		msg = tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		msg = tea.KeyMsg{Type: tea.KeyPgDown}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestPageNavigation(t *testing.T) {
	m := newTestModel(t)
	m = gotoMenu(t, m, "节点管理")
	m = key(m, "pgdown")
	if m.cursor == 0 {
		t.Fatal("pgdown 应跳过当前表格页")
	}
	m = key(m, "pgup")
	if m.cursor != 0 {
		t.Fatalf("pgup 应返回页首，实际光标 %d", m.cursor)
	}
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
	m = gotoMenu(t, m, "用户管理")
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
	m = gotoMenu(t, m, "节点管理")
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

func TestTUIRepickSNI(t *testing.T) {
	m := newTestModel(t)
	m = gotoMenu(t, m, "节点管理")
	nodes, err := m.Store.Nodes(false)
	if err != nil || len(nodes) == 0 {
		t.Fatal("夹具应包含节点")
	}
	idx := -1
	for i, n := range nodes {
		if n.Security == "reality" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("夹具应包含 REALITY 节点")
	}
	for m.cursor < idx {
		m = key(m, "down")
	}
	original, _ := m.Store.NodeByTag(nodes[idx].Tag)
	m.SNIProbe = func(_ context.Context, host string) panel.SniResult {
		return panel.SniResult{Host: panel.SniCandidates[0], TLS13: true, H2: true, X25519: true, Totalms: float64(len(host))}
	}
	// Drive the action through Bubble Tea's real key path, then execute its command.
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("REALITY 节点应启动 SNI 探测命令")
	}
	msg := cmd()
	model, _ = m.Update(msg)
	m = model.(Model)
	got, err := m.Store.NodeByTag(original.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if got.SNI == original.SNI || got.SNI != panel.SniCandidates[0] {
		t.Fatalf("SNI 未切换: old=%q new=%q", original.SNI, got.SNI)
	}
	if !strings.Contains(got.Dest, panel.SniCandidates[0]+":") {
		t.Fatalf("dest 未随 SNI 切换: %q", got.Dest)
	}
	if !strings.Contains(m.flash, "servername") || !strings.Contains(m.flash, "重新拉取订阅") {
		t.Fatalf("缺少重新拉取订阅提示: %q", m.flash)
	}
}

func TestQuit(t *testing.T) {
	m := newTestModel(t)
	m = gotoMenu(t, m, "退出")
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
	m = gotoMenu(t, m, "节点管理")
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
	m = gotoMenu(t, m, "节点管理")
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
	m = gotoMenu(t, m, "节点管理")
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
	m = gotoMenu(t, m, "用户管理")
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
	m = gotoMenu(t, m, "用户管理")
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

// 用户详情页：这是 TUI 里最实用的一个——直接在终端出二维码，手机扫。
func TestTUIUserDetailAndQR(t *testing.T) {
	m := newTestModel(t)
	m = gotoMenu(t, m, "用户管理")
	m = key(m, "enter") // 第一个用户的详情
	if m.view != viewUserDetail {
		t.Fatalf("回车应进入用户详情，实际 %v", m.view)
	}
	v := m.View()
	for _, want := range []string{"订阅地址", "/sub/", "/clash", "Clash/mihomo YAML", "█", "c 切换格式"} {
		if !strings.Contains(v, want) {
			t.Errorf("详情页缺少 %q", want)
		}
	}
	// 切格式：clash → singbox → base64
	m = key(m, "c")
	if m.qrFormat != "singbox" || !strings.Contains(m.View(), "/singbox") {
		t.Errorf("c 应切到 singbox，实际 %s", m.qrFormat)
	}
	m = key(m, "c")
	if m.qrFormat != "base64" || strings.Contains(m.View(), "/singbox") {
		t.Errorf("再按 c 应切到 base64，实际 %s", m.qrFormat)
	}
	m = key(m, "c")
	if m.qrFormat != "clash" {
		t.Errorf("应循环回 clash，实际 %s", m.qrFormat)
	}
	// 二维码内容必须真的是这个订阅地址（不是画着玩的方块）
	u, err := m.Store.UserByName(m.detailUser)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := panel.SubBaseFor(m.St) + "/sub/" + u.Token + "/clash"
	art, err := panel.RenderQR(wantURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.View(), strings.Split(art, "\n")[4]) {
		t.Error("详情页里的二维码与订阅地址对不上")
	}
}

// 事件与告警页：能看事件、能配 webhook（含格式校验）、能发测试。
func TestTUIEventsAndAlert(t *testing.T) {
	m := newTestModel(t)
	m = gotoMenu(t, m, "事件与告警")
	if m.view != viewEvents {
		t.Fatalf("应进入事件页，实际 %v", m.view)
	}
	if !strings.Contains(m.View(), "告警未配置") {
		t.Errorf("未配置时应提示\n%s", m.View())
	}
	// 没配就发测试 → 应提示先配
	m = key(m, "t")
	if !strings.Contains(m.flash, "先按 w") {
		t.Errorf("未配置时发测试应提示，实际 %q", m.flash)
	}
	// 填一个不合法地址 → 留在表单并报错
	m = key(m, "w")
	if m.form == nil {
		t.Fatal("w 应打开 webhook 表单")
	}
	m = typeText(m, "hooks.example.com/x")
	m = key(m, "enter")
	if m.form == nil || !strings.Contains(m.View(), "http://") {
		t.Errorf("非法地址应被拦下并说明原因\n%s", m.View())
	}
	// 改成合法地址 → 保存
	for i := 0; i < 30; i++ {
		m = key(m, "backspace")
	}
	m = typeText(m, "http://127.0.0.1:9/hook")
	m = key(m, "enter")
	if m.form != nil {
		t.Fatalf("合法地址应保存: %v", m.form.err)
	}
	if !strings.Contains(m.flash, "已设置") {
		t.Errorf("保存后应有提示，实际 %q", m.flash)
	}
	// 落盘了
	st, err := panel.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.AlertWebhook != "http://127.0.0.1:9/hook" {
		t.Errorf("设置没写盘: %q", st.AlertWebhook)
	}
	if !strings.Contains(m.View(), "告警已配置") {
		t.Errorf("页面应显示已配置\n%s", m.View())
	}
}

// 节点自检页：异步实测 + 三种结果（通/不通/跳过）的渲染。
func TestTUISelfTest(t *testing.T) {
	m := newTestModel(t)
	m = gotoMenu(t, m, "节点自检")
	if m.view != viewSelfTest {
		t.Fatalf("应进入自检页，实际 %v", m.view)
	}
	if !strings.Contains(m.View(), "按 t 开始实测") {
		t.Errorf("未实测时应提示按 t\n%s", m.View())
	}

	// 注入假实现：一个通、一个不通、一个跳过（真实现要起 xray，测试里不干这事）
	var gotNodes []panel.Node
	m.SelfTest = func(_ *panel.Store, _ panel.Settings, _ string, nodes []panel.Node,
		_ string, _ int, _ time.Duration) []panel.SelfTestResult {
		gotNodes = nodes
		return []panel.SelfTestResult{
			{Tag: "reality-tcp", Proto: "vless", OK: true, Latency: "42ms", ExitIP: "203.0.113.9"},
			{Tag: "hy2", Proto: "hysteria", Err: "204 探测失败（http=）"},
			{Tag: "ss2022", Proto: "shadowsocks", Skipped: true, Err: "这个协议测不了"},
		}
	}
	// 走真实的按键路径，再执行它返回的命令（异步不能卡界面）
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m = model.(Model)
	if cmd == nil {
		t.Fatal("按 t 应启动自检命令")
	}
	if !m.selfTesting || !strings.Contains(m.View(), "正在实测") {
		t.Errorf("实测期间应显示进行中状态（不能卡住界面）\n%s", m.View())
	}
	model, _ = m.Update(cmd())
	m = model.(Model)
	if m.selfTesting {
		t.Error("收到结果后应结束进行中状态")
	}
	if len(gotNodes) == 0 {
		t.Error("应该把启用的节点交给实测实现")
	}
	v := m.View()
	for _, want := range []string{"✓ 通", "✗ 不通", "— 跳过", "42ms", "203.0.113.9",
		"204 探测失败", "这个协议测不了", "1/2 个节点不通"} {
		if !strings.Contains(v, want) {
			t.Errorf("自检结果缺少 %q\n%s", want, v)
		}
	}
	if !strings.Contains(m.flash, "1/3") {
		t.Errorf("状态行应汇总结果，实际 %q", m.flash)
	}
}

// 全部通过时不该报错，而且要明确说"不在服务端"。
func TestTUISelfTestAllGood(t *testing.T) {
	m := newTestModel(t)
	m = gotoMenu(t, m, "节点自检")
	m.SelfTest = func(_ *panel.Store, _ panel.Settings, _ string, nodes []panel.Node,
		_ string, _ int, _ time.Duration) []panel.SelfTestResult {
		out := make([]panel.SelfTestResult, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, panel.SelfTestResult{Tag: n.Tag, Proto: n.Proto, OK: true, Latency: "10ms"})
		}
		return out
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m = model.(Model)
	model, _ = m.Update(cmd())
	m = model.(Model)
	if !strings.Contains(m.View(), "全部节点可用") {
		t.Errorf("全通时应给出结论\n%s", m.View())
	}
	if !strings.Contains(m.View(), "不在服务端") {
		t.Error("全通时应提示问题不在服务端")
	}
}
