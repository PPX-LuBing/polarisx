// Package tui 是 PolarisX 的终端界面，基于 Bubble Tea 官方 bubbles 组件。
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	bubbleskey "github.com/charmbracelet/bubbles/key"
	bubblesTable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PPX-LuBing/polarisx/internal/panel"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).Padding(1, 2)
	headerStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	pageStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	selectedRowStyle = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	statStyle        = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("238")).Padding(0, 1)
)

// view 是当前页面。
type view int

const (
	viewMenu view = iota
	viewNodes
	viewUsers
	viewDoctor
	viewApply
	viewUserDetail
	viewEvents
)

var menuItems = []string{
	"总览与自检",
	"节点管理",
	"用户管理",
	"生成配置并重启 xray",
	"事件与告警",
	"退出",
}

// Model 是 TUI 状态。
type Model struct {
	Store  *panel.Store
	St     panel.Settings
	view   view
	cursor int
	width  int
	height int
	// 状态行缓存（避免每帧都去探 API）
	apiOK    bool
	apiKnown bool
	// Probe 可注入（测试里别真去起 xray 子进程）
	Probe     func(panel.Settings, time.Duration) bool
	flash     string
	actionOut []string
	quit      bool
	// 写操作：表单与删除确认（nil / 空 = 不在该模式）
	form    *form
	confirm string
	onYes   func() error
	// confirmAction identifies actions that need a post-confirmation transition
	// (currently the xray apply action).
	confirmAction string
	// 用户详情页：看订阅地址与二维码
	detailUser string
	qrFormat   string
	qrArt      string
	qrShown    bool
	showHelp   bool
	help       help.Model
	keys       appKeyMap
}

// appKeyMap is shared by the help bubble and the page footer. Keeping the
// bindings in one place prevents the UI from advertising keys that Update does
// not actually handle.
type appKeyMap struct {
	Up, Down, PageUp, PageDown, Top, Bottom, Submit, Back, Help, Quit bubbleskey.Binding
}

func newAppKeyMap() appKeyMap {
	return appKeyMap{
		Up:       bubbleskey.NewBinding(bubbleskey.WithKeys("up", "k"), bubbleskey.WithHelp("↑/k", "上移")),
		Down:     bubbleskey.NewBinding(bubbleskey.WithKeys("down", "j"), bubbleskey.WithHelp("↓/j", "下移")),
		PageUp:   bubbleskey.NewBinding(bubbleskey.WithKeys("pgup", "b"), bubbleskey.WithHelp("b/pgup", "上翻")),
		PageDown: bubbleskey.NewBinding(bubbleskey.WithKeys("pgdown", "f", " "), bubbleskey.WithHelp("f/pgdn", "下翻")),
		Top:      bubbleskey.NewBinding(bubbleskey.WithKeys("home", "g"), bubbleskey.WithHelp("g/home", "顶部")),
		Bottom:   bubbleskey.NewBinding(bubbleskey.WithKeys("end", "G"), bubbleskey.WithHelp("G/end", "底部")),
		Submit:   bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "打开")),
		Back:     bubbleskey.NewBinding(bubbleskey.WithKeys("esc", "q"), bubbleskey.WithHelp("esc/q", "返回")),
		Help:     bubbleskey.NewBinding(bubbleskey.WithKeys("?", "h"), bubbleskey.WithHelp("?", "帮助")),
		Quit:     bubbleskey.NewBinding(bubbleskey.WithKeys("ctrl+c"), bubbleskey.WithHelp("ctrl+c", "退出")),
	}
}

// newHelpModel uses explicit colors instead of bubbles/help's adaptive dark
// palette. The latter is too low contrast on the dark SSH terminals this TUI
// is commonly used from.
func newHelpModel() help.Model {
	h := help.New()
	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	separatorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	h.Styles = help.Styles{
		Ellipsis:       separatorStyle,
		ShortKey:       keyStyle,
		ShortDesc:      descStyle,
		ShortSeparator: separatorStyle,
		FullKey:        keyStyle,
		FullDesc:       descStyle,
		FullSeparator:  separatorStyle,
	}
	h.ShortSeparator = "  •  "
	h.FullSeparator = "    "
	return h
}

func (k appKeyMap) ShortHelp() []bubbleskey.Binding {
	return []bubbleskey.Binding{k.Up, k.Down, k.Submit, k.Back, k.Help}
}

func (k appKeyMap) FullHelp() [][]bubbleskey.Binding {
	return [][]bubbleskey.Binding{{k.Up, k.Down, k.PageUp, k.PageDown}, {k.Top, k.Bottom, k.Submit, k.Back}, {k.Help, k.Quit}}
}

// New 构造 TUI 模型。
func New(store *panel.Store, st panel.Settings) Model {
	return Model{Store: store, St: st, width: 80, height: 24, help: newHelpModel(), keys: newAppKeyMap()}
}

// Quit 报告用户是否选择了退出。
func (m Model) Quit() bool { return m.quit }

func (m Model) Init() tea.Cmd { return nil }

func (m *Model) refreshStatus() {
	// 只探一次，之后靠 r 手动刷新（避免每帧起进程）
	if m.apiKnown {
		return
	}
	probe := m.Probe
	if probe == nil {
		probe = panel.APIAvailable
	}
	m.apiOK = probe(m.St, 1500*time.Millisecond)
	m.apiKnown = true
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		key := msg.String()
		// Ctrl+C must work in forms and confirmation dialogs as well as pages.
		if key == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
		}
		// 表单模式：按键全部交给表单
		if m.form != nil {
			if !m.form.update(key) {
				if m.form.doneMsg != "" {
					m.flash = m.form.doneMsg
				}
				m.form = nil
			}
			return m, nil
		}
		// 删除确认
		if m.confirm != "" {
			switch key {
			case "y", "Y":
				if err := m.onYes(); err != nil {
					m.flash = "✗ " + err.Error()
				} else {
					m.flash = "✓ 已完成"
					if m.confirmAction == "apply" {
						m.actionOut = m.applyLines()
						m.view = viewApply
					}
					if m.view == viewNodes || m.view == viewUsers {
						m.cursor = min(m.cursor, max(0, m.rowCount()-1))
					}
				}
				m.confirm, m.onYes, m.confirmAction = "", nil, ""
			case "n", "N", "esc":
				m.flash = "已取消"
				m.confirm, m.onYes, m.confirmAction = "", nil, ""
			}
			return m, nil
		}
		if m.showHelp {
			switch key {
			case "?", "h", "esc", "q":
				m.showHelp = false
			}
			return m, nil
		}
		switch key {
		case "q":
			if m.view == viewMenu {
				m.quit = true
				return m, tea.Quit
			}
			if m.view == viewUserDetail {
				m.view = viewUsers
			} else {
				m.view = viewMenu
			}
			m.cursor = 0
			return m, nil
		case "esc", "backspace":
			if m.view == viewUserDetail {
				m.view = viewUsers
			} else {
				m.view = viewMenu
			}
			m.cursor = 0
			return m, nil
		case "r":
			m.apiKnown = false
			m.flash = ""
			m.qrArt, m.qrShown = "", false
			m.refreshStatus()
			return m, nil
		case "?", "h":
			m.showHelp = true
			return m, nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "j":
			if m.cursor < m.rowCount()-1 {
				m.cursor++
			}
			return m, nil
		case "home", "g":
			m.cursor = 0
			return m, nil
		case "end", "G":
			m.cursor = max(0, m.rowCount()-1)
			return m, nil
		case "c":
			if m.view == viewUserDetail {
				switch m.qrFormat {
				case "clash":
					m.qrFormat = "singbox"
				case "singbox":
					m.qrFormat = "base64"
				default:
					m.qrFormat = "clash"
				}
				m.qrArt = ""
				m.qrShown = false
				return m, nil
			}
			return m, nil
		case "t":
			if m.view == viewEvents {
				return m.sendTestAlert()
			}
			return m, nil
		case "w":
			if m.view == viewEvents {
				return m.startWebhookForm()
			}
			return m, nil
		case "a":
			return m.startAdd()
		case "d":
			return m.startDelete()
		case "e":
			return m.startEdit()
		case "enter":
			if m.view == viewMenu {
				return m.selectMenu()
			}
			if m.view == viewUsers {
				users, _ := m.Store.Users()
				if m.cursor < len(users) {
					m.view = viewUserDetail
					m.detailUser = users[m.cursor].Name
					m.qrFormat = "clash"
					m.qrArt = ""
				}
			}
			return m, nil
		default:
			// 主菜单支持数字快捷键，减少反复移动光标。
			if m.view == viewMenu && len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
				i := int(key[0] - '1')
				if i < len(menuItems) {
					m.cursor = i
					return m.selectMenu()
				}
			}
		}
	}
	return m, nil
}

// rowCount 当前视图里可选的行数（菜单/节点/用户）。
func (m Model) rowCount() int {
	switch m.view {
	case viewNodes:
		nodes, _ := m.Store.Nodes(false)
		return len(nodes)
	case viewUsers:
		users, _ := m.Store.Users()
		return len(users)
	}
	return len(menuItems)
}

func (m Model) selectMenu() (tea.Model, tea.Cmd) {
	switch m.cursor {
	case 0:
		m.view = viewDoctor
		m.actionOut = m.doctorLines()
	case 1:
		m.view = viewNodes
		m.cursor = 0
	case 2:
		m.view = viewUsers
		m.cursor = 0
	case 3:
		m.confirm = "生成配置并重启 xray？（会影响当前连接）"
		m.confirmAction = "apply"
		m.onYes = func() error { return nil }
	case 4:
		m.view = viewEvents
		m.cursor = 0
		m.actionOut = nil
	case 5:
		m.quit = true
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) View() string {
	if m.form != nil {
		return m.header() + "\n" + m.form.view(m.width)
	}
	if m.confirm != "" {
		return m.header() + "\n\n" + warnStyle.Render("  "+m.confirm) +
			"\n\n" + dimStyle.Render("  y 确认 · n 取消") + "\n"
	}
	m.refreshStatus()
	if m.showHelp {
		return m.header() + "\n\n" + m.helpView() + "\n" + m.footer()
	}
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	switch m.view {
	case viewMenu:
		b.WriteString(m.menuView())
	case viewNodes:
		b.WriteString(m.nodesView())
	case viewUsers:
		b.WriteString(m.usersView())
	case viewUserDetail:
		b.WriteString(m.userDetailView())
	case viewEvents:
		b.WriteString(m.eventsView())
	case viewDoctor, viewApply:
		b.WriteString(m.textView())
	}
	if m.flash != "" {
		b.WriteString("\n\n")
		b.WriteString(noticeStyle(m.flash))
	}
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) header() string {
	st := m.St
	dot := func(ok bool, s string) string {
		if ok {
			return okStyle.Render("● " + s)
		}
		return errStyle.Render("● " + s)
	}
	xray := "xray 未探测"
	if m.apiKnown {
		if m.apiOK {
			xray = "xray 在线"
		} else {
			xray = "xray 离线"
		}
	}
	nodes, _ := m.Store.Nodes(false)
	users, _ := m.Store.Users()
	enabled := 0
	for _, n := range nodes {
		if n.Enabled != 0 {
			enabled++
		}
	}
	line1 := fmt.Sprintf("%s  %s",
		titleStyle.Render(fmt.Sprintf("%s %s", panel.ProductName, panel.Version)),
		dimStyle.Render(panel.Home()))
	status := dot(m.apiKnown && m.apiOK, xray)
	line2 := strings.Join([]string{
		status,
		statStyle.Render(fmt.Sprintf("节点 %d/%d", enabled, len(nodes))),
		statStyle.Render(fmt.Sprintf("用户 %d", len(users))),
		dimStyle.Render("分流 " + st.ClashRules),
		dimStyle.Render("出口 " + st.OutboundDomainStrategy),
	}, "  ")
	return line1 + "\n" + line2
}

func (m Model) menuView() string {
	var lines []string
	lines = append(lines, pageStyle.Render("工作台"), dimStyle.Render("选择一个区域开始管理"), "")
	for i, it := range menuItems {
		label := fmt.Sprintf("%d  %s", i+1, it)
		if i == m.cursor {
			lines = append(lines, selectedRowStyle.Render(selStyle.Render("▸ "+label)))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	return boxStyle.Width(max(24, m.width-4)).Render(strings.Join(lines, "\n")) + "\n"
}

func noticeStyle(s string) string {
	if strings.HasPrefix(s, "✗") {
		return boxStyle.BorderForeground(lipgloss.Color("203")).Render(errStyle.Render(s))
	}
	if strings.HasPrefix(s, "⚠") {
		return boxStyle.BorderForeground(lipgloss.Color("214")).Render(warnStyle.Render(s))
	}
	return boxStyle.BorderForeground(lipgloss.Color("42")).Render(okStyle.Render(s))
}

func (m Model) helpView() string {
	h := m.helpModel()
	rows := []string{pageStyle.Render("快捷键"), h.View(m.keys), "", pageStyle.Render("当前页面")}
	switch m.view {
	case viewMenu:
		rows = append(rows, "1-6         直接打开菜单项")
	case viewNodes:
		rows = append(rows, "a 新增   e 编辑   d 删除   选择行后操作")
	case viewUsers:
		rows = append(rows, "Enter 详情/二维码   a 新增   e 编辑   d 删除")
	case viewUserDetail:
		rows = append(rows, "c 切换二维码格式（Clash / sing-box / base64）")
	case viewEvents:
		rows = append(rows, "w 设置 webhook   t 发送测试告警")
	case viewDoctor, viewApply:
		rows = append(rows, "q / Esc 返回工作台")
	}
	return boxStyle.Width(max(32, m.width-4)).Render(strings.Join(rows, "\n"))
}

func (m Model) helpModel() help.Model {
	h := m.help
	h.Width = max(24, m.width-4)
	return h
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m Model) renderTable(columns []bubblesTable.Column, rows [][]string) string {
	converted := make([]bubblesTable.Row, len(rows))
	for i, row := range rows {
		converted[i] = bubblesTable.Row(row)
	}
	height := max(4, m.height-13)
	t := bubblesTable.New(
		bubblesTable.WithColumns(columns),
		bubblesTable.WithRows(converted),
		bubblesTable.WithFocused(true),
		bubblesTable.WithWidth(max(32, m.width-4)),
		bubblesTable.WithHeight(height),
	)
	t.SetCursor(m.cursor)
	t.SetStyles(bubblesTable.Styles{
		Header:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Padding(0, 1),
		Cell:     lipgloss.NewStyle().Padding(0, 1),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")).Background(lipgloss.Color("236")).Padding(0, 1),
	})
	return t.View()
}

func nodeColumns(width int) []bubblesTable.Column {
	if width < 100 {
		// Keep the warning marker in the tag column and hide lower-priority
		// columns on narrow SSH windows instead of clipping the whole table.
		return []bubblesTable.Column{
			{Title: "tag", Width: 11}, {Title: "协议", Width: 6}, {Title: "传输", Width: 0},
			{Title: "安全", Width: 0}, {Title: "端口", Width: 5}, {Title: "入口", Width: 10},
			{Title: "状态", Width: 6}, {Title: "问题", Width: 0},
		}
	}
	if width < 140 {
		return []bubblesTable.Column{
			{Title: "tag", Width: 13}, {Title: "协议", Width: 9}, {Title: "传输", Width: 8},
			{Title: "安全", Width: 8}, {Title: "端口", Width: 6}, {Title: "入口", Width: 14},
			{Title: "状态", Width: 7}, {Title: "问题", Width: 12},
		}
	}
	return []bubblesTable.Column{
		{Title: "tag", Width: 16}, {Title: "协议", Width: 12}, {Title: "传输", Width: 10},
		{Title: "安全", Width: 10}, {Title: "端口", Width: 7}, {Title: "入口", Width: 24},
		{Title: "状态", Width: 10}, {Title: "问题", Width: 28},
	}
}

func userColumns(width int) []bubblesTable.Column {
	if width < 100 {
		return []bubblesTable.Column{
			{Title: "用户", Width: 11}, {Title: "已用", Width: 9}, {Title: "额度", Width: 14},
			{Title: "到期", Width: 10}, {Title: "节点", Width: 5}, {Title: "状态", Width: 10},
		}
	}
	if width < 140 {
		return []bubblesTable.Column{
			{Title: "用户", Width: 12}, {Title: "已用", Width: 10}, {Title: "额度", Width: 16},
			{Title: "到期", Width: 10}, {Title: "节点", Width: 5}, {Title: "状态", Width: 12},
		}
	}
	return []bubblesTable.Column{
		{Title: "用户", Width: 16}, {Title: "已用(上下行合计)", Width: 20}, {Title: "额度", Width: 20},
		{Title: "到期", Width: 14}, {Title: "节点", Width: 7}, {Title: "状态", Width: 24},
	}
}

func (m Model) nodesView() string {
	nodes, _ := m.Store.Nodes(false)
	rows := [][]string{}
	for _, n := range nodes {
		state := "启用"
		if n.Enabled == 0 {
			state = "停用"
		}
		// 多入口节点只显示第一个，多的用 +N 提示
		ents := panel.EntriesOf(n, m.St)
		entry, port, extra := "(无入口)", n.Port, ""
		if len(ents) > 0 {
			entry, port = ents[0].Host, ents[0].Port
			if len(ents) > 1 {
				extra = fmt.Sprintf(" +%d", len(ents)-1)
			}
		}
		note := ""
		tag := n.Tag
		if probs := panel.NodeProblems(n); len(probs) > 0 {
			// Prefix the marker so it survives bubbles/table truncation on
			// narrow terminals; appending it would truncate the useful signal.
			tag = "⚠ " + tag
			// Keep a compact warning marker visible even when the table truncates
			// the full diagnostic on a narrow SSH terminal.
			note = "⚠ " + probs[0]
			if len(note) > 12 {
				note = "⚠ " + probs[0][:min(len(probs[0]), 9)]
			}
		}
		rows = append(rows, []string{
			tag, n.Proto, n.Net, n.Security,
			fmt.Sprintf("%d", n.Port),
			fmt.Sprintf("%s:%d%s", entry, port, extra),
			state, note,
		})
	}
	head := fmt.Sprintf("节点 %d 个", len(nodes))
	return pageStyle.Render(head) + "\n" +
		m.renderTable(nodeColumns(m.width), rows) +
		"\n" + dimStyle.Render("  ↑↓/jk 选择 · a 新增 · e 编辑 · d 删除 · Esc 返回")
}

func (m Model) usersView() string {
	users, _ := m.Store.Users()
	rows := [][]string{}
	for _, u := range users {
		used := panel.UsedBytes(u)
		quota := panel.QuotaBytes(u, m.St)
		q := "不限"
		if quota > 0 {
			q = fmt.Sprintf("%s (%.0f%%)", panel.HumanBytes(quota),
				float64(used)/float64(quota)*100)
		}
		state := "启用"
		if u.Enable == 0 {
			state = "停用"
			if u.DisabledReason != "" {
				state += " " + u.DisabledReason
			}
		}
		nodes, _ := m.Store.NodesForUser(u, true)
		expire := u.ExpireAt
		if expire == "" {
			expire = "不过期"
		}
		rows = append(rows, []string{
			u.Name, panel.HumanBytes(used), q, expire,
			fmt.Sprintf("%d", len(nodes)), state,
		})
	}
	return pageStyle.Render(fmt.Sprintf("用户 %d 个", len(users))) + "\n" +
		m.renderTable(userColumns(m.width), rows) +
		"\n" + dimStyle.Render("  ↑↓/jk 选择 · Enter 详情 · a 新增 · e 编辑 · d 删除 · Esc 返回")
}

func (m Model) textView() string {
	if len(m.actionOut) == 0 {
		return dimStyle.Render("(无输出)")
	}
	return boxStyle.Width(max(24, m.width-4)).Render(strings.Join(m.actionOut, "\n"))
}

func (m Model) footer() string {
	hint := m.helpModel().View(m.keys)
	switch m.view {
	case viewNodes:
		hint += "  ·  a 新增  ·  e 编辑  ·  d 删除  ·  r 刷新"
	case viewUsers:
		hint += "  ·  Enter 详情  ·  a 新增  ·  e 编辑  ·  d 删除  ·  r 刷新"
	case viewUserDetail:
		hint += "  ·  c 切换二维码  ·  r 刷新"
	case viewEvents:
		hint += "  ·  w webhook  ·  t 测试告警  ·  r 刷新"
	case viewMenu:
		hint += "  ·  1-6 快捷打开"
	default:
		hint += "  ·  r 刷新"
	}
	return dimStyle.Render(hint)
}

func (m *Model) doctorLines() []string {
	out := []string{}
	nodes, _ := m.Store.Nodes(false)
	users, _ := m.Store.Users()
	bad := 0
	for _, n := range nodes {
		if probs := panel.NodeProblems(n); len(probs) > 0 {
			bad++
			out = append(out, errStyle.Render("⚠ ")+fmt.Sprintf("%s: %s", n.Tag,
				strings.Join(probs, "; ")))
		}
	}
	if bad == 0 {
		out = append(out, okStyle.Render("✓ 所有节点体检通过"))
	}
	out = append(out, okStyle.Render("✓ ")+fmt.Sprintf("节点 %d 个 / 用户 %d 个", len(nodes), len(users)))
	if b := panel.FindXray(m.St); b != "" {
		out = append(out, okStyle.Render("✓ ")+"xray: "+b)
	} else {
		out = append(out, errStyle.Render("⚠ ")+"找不到 xray 二进制")
	}
	cfg, err := m.Store.BuildConfig(m.St)
	if err != nil {
		out = append(out, errStyle.Render("⚠ ")+"配置生成失败: "+err.Error())
	} else {
		inb, _ := cfg["inbounds"].([]any)
		out = append(out, okStyle.Render("✓ ")+fmt.Sprintf("可生成配置: %d 个 inbound", len(inb)))
	}
	return out
}

func (m *Model) applyLines() []string {
	out := []string{}
	cfg, err := m.Store.BuildConfig(m.St)
	if err != nil {
		return []string{errStyle.Render("配置生成失败: ") + err.Error()}
	}
	verify := m.St.VerifyConfig
	if err := panel.WriteConfig(m.St, cfg, "", verify); err != nil {
		return []string{errStyle.Render("写配置失败: ") + err.Error()}
	}
	out = append(out, okStyle.Render("✓ ")+"已写入 "+m.St.XrayConfig)
	if err := panel.RestartXray(m.St, false); err != nil {
		out = append(out, errStyle.Render("⚠ ")+err.Error())
	} else {
		out = append(out, okStyle.Render("✓ ")+"已重启 xray")
	}
	return out
}

// ---------------------------------------------------------------------------
// 写操作：建/改/删节点与用户
//
// 表单字段刻意只收"必须手填"的部分：reality 的密钥对与 shortId 由 panel 自动生成，
// 用户不填节点就等于绑全部节点（与 CLI 语义一致）。
// ---------------------------------------------------------------------------

func (m Model) startAdd() (tea.Model, tea.Cmd) {
	switch m.view {
	case viewNodes:
		m.form = newForm("新增节点", []field{
			{label: "tag", hint: "唯一标识，如 ix-reality"},
			{label: "协议", value: "vless", pick: []string{"vless", "vmess", "trojan", "shadowsocks", "hysteria2"}},
			{label: "传输", value: "tcp", pick: []string{"tcp", "ws", "httpupgrade", "xhttp", "grpc", "hysteria"}},
			{label: "安全", value: "reality", pick: []string{"reality", "tls", "none"}},
			{label: "端口", hint: "如 443"},
			{label: "入口地址", hint: "客户端连的地址，如 1.2.3.4"},
			{label: "SNI", hint: "留空=自动实测挑选（reality/tls）"},
		}, "reality 的密钥对与 shortId 会自动生成；SNI 留空则在本机实测挑选",
			func(v []string) (bool, string, error) {
				if v[0] == "" {
					return true, "", fmt.Errorf("tag 不能为空")
				}
				port, err := strconv.Atoi(v[4])
				if err != nil || port <= 0 || port > 65535 {
					return true, "", fmt.Errorf("端口要是 1-65535 的数字")
				}
				n := panel.Node{Tag: v[0], Proto: v[1], Net: v[2], Security: v[3],
					Port: port, EntryHost: v[5], SNI: v[6]}
				if n.Security == "reality" {
					priv, pub, sid, err := panel.GenerateRealityKeys()
					if err != nil {
						return true, "", err
					}
					n.PrivKey, n.PubKey, n.ShortID = priv, pub, sid
					if n.Dest == "" && n.SNI != "" {
						n.Dest = n.SNI + ":443"
					}
				}
				if err := m.Store.CreateNode(n); err != nil {
					return true, "", err
				}
				return false, "✓ 已新增节点 " + n.Tag + "（记得生成配置并重启）", nil
			})
	case viewUsers:
		var tags []string
		if nodes, err := m.Store.Nodes(true); err == nil {
			for _, n := range nodes {
				tags = append(tags, n.Tag)
			}
		}
		m.form = newForm("新增用户", []field{
			{label: "用户名", hint: "字母数字 _ . @ - ，2-48 位"},
			{label: "额度(GB)", value: "100", hint: "0=不限"},
			{label: "到期", hint: "YYYY-MM-DD，留空=不限"},
			{label: "节点", hint: "逗号分隔；留空=全部节点（可选: " + strings.Join(tags, ",") + "）"},
		}, "留空节点 = 绑全部节点（与 CLI 一致）",
			func(v []string) (bool, string, error) {
				if v[0] == "" {
					return true, "", fmt.Errorf("用户名不能为空")
				}
				quota := 0.0
				if v[1] != "" {
					q, err := strconv.ParseFloat(v[1], 64)
					if err != nil || q < 0 {
						return true, "", fmt.Errorf("额度要是不小于 0 的数字")
					}
					quota = q
				}
				var nt []string
				for _, t := range strings.Split(v[3], ",") {
					if t = strings.TrimSpace(t); t != "" {
						nt = append(nt, t)
					}
				}
				u, err := m.Store.CreateUser(v[0], quota, v[2], "", nt)
				if err != nil {
					return true, "", err
				}
				return false, fmt.Sprintf("✓ 已新增用户 %s（订阅 token=%s…）", u.Name, u.Token[:8]), nil
			})
	default:
		m.flash = "这个页面没有新增操作"
	}
	return m, nil
}

func (m Model) startEdit() (tea.Model, tea.Cmd) {
	switch m.view {
	case viewNodes:
		nodes, _ := m.Store.Nodes(false)
		if m.cursor >= len(nodes) {
			return m, nil
		}
		n := nodes[m.cursor]
		m.form = newForm("编辑节点 "+n.Tag, []field{
			{label: "端口", value: fmt.Sprintf("%d", n.Port)},
			{label: "入口地址", value: n.EntryHost},
			{label: "SNI", value: n.SNI},
			{label: "启用", value: map[bool]string{true: "1", false: "0"}[n.Enabled == 1],
				pick: []string{"1", "0"}},
		}, "回车提交；改完记得生成配置并重启",
			func(v []string) (bool, string, error) {
				port, err := strconv.Atoi(v[0])
				if err != nil || port <= 0 || port > 65535 {
					return true, "", fmt.Errorf("端口要是 1-65535 的数字")
				}
				pairs := []string{fmt.Sprintf("port=%d", port), "entry_host=" + v[1],
					"sni=" + v[2], "enabled=" + v[3]}
				if n.Security == "reality" && v[2] != "" {
					pairs = append(pairs, "dest="+v[2]+":443")
				}
				if err := m.Store.UpdateNode(n.Tag, pairs); err != nil {
					return true, "", err
				}
				return false, "✓ 已更新节点 " + n.Tag, nil
			})
	case viewUsers:
		users, _ := m.Store.Users()
		if m.cursor >= len(users) {
			return m, nil
		}
		u := users[m.cursor]
		m.form = newForm("编辑用户 "+u.Name, []field{
			{label: "额度(GB)", value: fmt.Sprintf("%g", u.QuotaGB)},
			{label: "到期", value: u.ExpireAt},
			{label: "启用", value: map[bool]string{true: "1", false: "0"}[u.Enable == 1],
				pick: []string{"1", "0"}},
			{label: "备注", value: u.Note},
		}, "停用会同时摘掉在线连接（xray api rmu）",
			func(v []string) (bool, string, error) {
				q, err := strconv.ParseFloat(v[0], 64)
				if err != nil || q < 0 {
					return true, "", fmt.Errorf("额度要是不小于 0 的数字")
				}
				pairs := []string{fmt.Sprintf("quota_gb=%g", q), "expire_at=" + v[1],
					"enable=" + v[2], "note=" + v[3]}
				if err := m.Store.UpdateUser(u.Name, pairs); err != nil {
					return true, "", err
				}
				if v[2] == "0" {
					if uu, err := m.Store.UserByName(u.Name); err == nil {
						_ = m.Store.DisableUser(m.St, uu, "管理员手动停用", true)
					}
				}
				return false, "✓ 已更新用户 " + u.Name, nil
			})
	default:
		m.flash = "这个页面没有编辑操作"
	}
	return m, nil
}

func (m Model) startDelete() (tea.Model, tea.Cmd) {
	switch m.view {
	case viewNodes:
		nodes, _ := m.Store.Nodes(false)
		if m.cursor >= len(nodes) {
			return m, nil
		}
		tag := nodes[m.cursor].Tag
		m.confirm = fmt.Sprintf("删除节点 %s？（订阅里的该节点会消失）", tag)
		m.onYes = func() error { return m.Store.DeleteNode(tag) }
	case viewUsers:
		users, _ := m.Store.Users()
		if m.cursor >= len(users) {
			return m, nil
		}
		name := users[m.cursor].Name
		m.confirm = fmt.Sprintf("删除用户 %s？（其订阅地址立即失效）", name)
		m.onYes = func() error { return m.Store.DeleteUser(name) }
	default:
		m.flash = "这个页面没有删除操作"
	}
	return m, nil
}

// currentSettings 取最新设置：值接收者下改 m.St 会丢，而且设置随时可能被 CLI 改过。
func (m Model) currentSettings() panel.Settings {
	if st, err := panel.LoadSettings(); err == nil {
		return st
	}
	return m.St
}

// ---------------------------------------------------------------------------
// 用户详情页：看订阅地址与二维码（手机直接扫，省得复制粘贴）
// ---------------------------------------------------------------------------

func (m Model) userDetailView() string {
	u, err := m.Store.UserByName(m.detailUser)
	if err != nil {
		return errStyle.Render("  用户不存在: "+m.detailUser) + "\n"
	}
	quota := panel.QuotaBytes(u, m.St)
	used := panel.UsedBytes(u)
	var b strings.Builder
	b.WriteString(headerStyle.Render("用户 "+u.Name) + "\n\n")
	quotaText := "不限"
	if quota > 0 {
		quotaText = panel.HumanBytes(quota)
	}
	fmt.Fprintf(&b, "  %-10s %s / %s\n", "用量", panel.HumanBytes(used), quotaText)
	expire := u.ExpireAt
	if expire == "" {
		expire = "不限"
	}
	fmt.Fprintf(&b, "  %-10s %s\n", "到期", expire)
	state := okStyle.Render("启用")
	if u.Enable != 1 {
		state = warnStyle.Render("停用（" + u.DisabledReason + "）")
	}
	fmt.Fprintf(&b, "  %-10s %s\n\n", "状态", state)

	// 订阅地址：c 键在 clash / sing-box / base64 之间切
	suffix, label := "/clash", "Clash/mihomo YAML"
	switch m.qrFormat {
	case "singbox":
		suffix, label = "/singbox", "sing-box JSON"
	case "base64":
		suffix, label = "", "base64（老客户端）"
	}
	url := panel.SubBaseFor(m.St) + "/sub/" + u.Token + suffix
	fmt.Fprintf(&b, "  %s（按 c 切换格式）\n", dimStyle.Render("订阅地址 · "+label))
	b.WriteString("  " + url + "\n\n")

	// 二维码按需生成（不每帧重算）
	if !m.qrShown {
		art, err := panel.RenderQR(url)
		if err != nil {
			m.qrArt = errStyle.Render("  二维码生成失败: " + err.Error())
		} else {
			m.qrArt = art
		}
		m.qrShown = true
	}
	b.WriteString(m.qrArt)
	b.WriteString("\n" + dimStyle.Render("  c 切换格式 · r 刷新 · Esc 返回"))
	return b.String()
}

// ---------------------------------------------------------------------------
// 事件与告警页
// ---------------------------------------------------------------------------

func (m Model) eventsView() string {
	st := m.currentSettings()
	var b strings.Builder
	hook := strings.TrimSpace(st.AlertWebhook)
	b.WriteString(headerStyle.Render("事件与告警") + "\n\n")
	if hook == "" {
		b.WriteString("  " + warnStyle.Render("告警未配置") +
			"　按 w 填一个 webhook（用量过 80% 或被停用时通知）\n\n")
	} else {
		fmt.Fprintf(&b, "  %s %s\n\n", okStyle.Render("告警已配置"), hook)
	}
	evs, err := m.Store.Events(20)
	if err != nil {
		return b.String() + errStyle.Render("  读事件失败: "+err.Error()) + "\n"
	}
	if len(evs) == 0 {
		b.WriteString(dimStyle.Render("  还没有事件") + "\n")
	} else {
		b.WriteString("  " + dimStyle.Render("最近 20 条事件（新的在上）") + "\n")
		for _, e := range evs {
			mark := "·"
			switch e.Level {
			case "warn":
				mark = warnStyle.Render("!")
			case "error":
				mark = errStyle.Render("×")
			}
			who := ""
			if e.UserName != "" {
				who = " " + e.UserName
			}
			fmt.Fprintf(&b, "  %s %s%s %s\n", mark, dimStyle.Render(e.TS), who, e.Message)
		}
	}
	b.WriteString("\n" + dimStyle.Render("  w 设置 webhook · t 发测试消息 · r 刷新 · Esc 返回"))
	return b.String()
}

func (m Model) startWebhookForm() (tea.Model, tea.Cmd) {
	m.form = newForm("设置告警 webhook", []field{
		{label: "webhook", value: m.currentSettings().AlertWebhook,
			hint: "留空=关闭告警；支持 Slack/Discord/飞书那类收 JSON 的地址"},
	}, "消息体带 text 字段，也带结构化 events；采集时用量过 80% 或被停用会发",
		func(v []string) (bool, string, error) {
			url := strings.TrimSpace(v[0])
			if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
				return true, "", fmt.Errorf("要以 http:// 或 https:// 开头（否则会静默失败）")
			}
			st, err := panel.LoadSettings()
			if err != nil {
				return true, "", err
			}
			st.AlertWebhook = url
			if err := panel.SaveSettings(st); err != nil {
				return true, "", err
			}
			if url == "" {
				return false, "✓ 已关闭用量告警", nil
			}
			return false, "✓ 已设置告警地址（按 t 可发测试消息）", nil
		})
	return m, nil
}

func (m Model) sendTestAlert() (tea.Model, tea.Cmd) {
	st := m.currentSettings()
	if strings.TrimSpace(st.AlertWebhook) == "" {
		m.flash = "还没配 webhook，先按 w"
		return m, nil
	}
	evs := []panel.AlertEvent{{Kind: "warn", User: "（测试）",
		Message: "来自 PolarisX TUI 的测试告警",
		At:      time.Now().Format("2006-01-02 15:04:05")}}
	if err := panel.SendAlert(st, evs, 10*time.Second); err != nil {
		m.flash = "✗ 发送失败: " + err.Error()
		return m, nil
	}
	m.flash = "✓ 测试消息已发出"
	return m, nil
}
