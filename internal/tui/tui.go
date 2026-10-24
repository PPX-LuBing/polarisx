// Package tui 是 PolarisX 的终端界面，基于 Bubble Tea 官方 bubbles 组件。
package tui

import (
	"context"
	"fmt"
	"net"
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
	// Status badges stay on one line. Mature Charmbracelet UIs use a flat
	// status bar rather than a bordered box for every metric, which preserves
	// vertical space on SSH terminals.
	statStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("236")).Padding(0, 1)
	menuItemStyle = lipgloss.NewStyle().PaddingLeft(1).
			Border(lipgloss.Border{Left: " "}, false, false, false, true)
	menuActiveStyle = menuItemStyle.BorderStyle(lipgloss.Border{Left: "┃"}).
			BorderForeground(lipgloss.Color("81")).Bold(true).Foreground(lipgloss.Color("81"))
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
	viewSelfTest
	viewTraffic
	viewSettings
	viewTunnel
)

var menuItems = []string{
	"总览与自检",
	"节点自检（实测连通性）",
	"节点管理",
	"用户管理",
	"生成配置并重启 xray",
	"事件与告警",
	"流量统计",
	"设置",
	"免域名隧道",
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
	Probe    func(panel.Settings, time.Duration) bool
	SNIProbe panel.SniProbeFunc
	// SelfTest 可注入（测试里不必真起 xray）；nil 时用 panel 的真实实现
	SelfTest panel.SelfTestFunc
	flash    string
	// 自检页状态
	selfTesting bool
	selfResults []panel.SelfTestResult
	// 隧道页状态（读 metrics 端口要几秒，所以异步）
	tunnelState *panel.TunnelState
	tunnelBusy  bool
	tunnelLog   []string
	// 流量页：数据在进页面/刷新时算好，不在 View 里查库（View 每次按键都会跑）
	traffic24 []panel.TrafficPoint
	traffic7  []panel.TrafficPoint
	traffic30 []panel.TrafficPoint
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

type sniPickMsg struct {
	tag  string
	best panel.SniResult
	err  error
}

func (m Model) repickSNI() (tea.Model, tea.Cmd) {
	nodes, _ := m.Store.Nodes(false)
	if m.cursor >= len(nodes) {
		return m, nil
	}
	n := nodes[m.cursor]
	if n.Security != "reality" {
		m.flash = "⚠ 只有 REALITY 节点可以重新挑 SNI"
		return m, nil
	}
	m.flash = "正在实测 SNI 候选，请稍候…"
	probe := m.SNIProbe
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if probe == nil {
			cf := panel.LoadCFRanges(ctx, 20*time.Second)
			probe = func(ctx context.Context, host string) panel.SniResult {
				return panel.ProbeTLS(ctx, host, 6*time.Second, cf)
			}
		}
		best, _, err := panel.BestSNI(ctx, panel.SniCandidates, probe, 8)
		return sniPickMsg{tag: n.Tag, best: best, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tunnelMsg:
		m.tunnelBusy = false
		m.tunnelLog = msg.lines
		if msg.action == "status" {
			m.tunnelState = msg.state
			return m, nil
		}
		if msg.err != nil {
			m.flash = "✗ 隧道" + msg.action + "失败: " + msg.err.Error()
		} else {
			m.flash = "✓ 隧道" + msg.action + "完成"
		}
		// 动作之后重新读一次状态，界面才是最新的
		return m, m.loadTunnelStatus()
	case selfTestMsg:
		m.selfTesting = false
		if msg.err != nil {
			m.flash = "✗ 自检失败: " + msg.err.Error()
			return m, nil
		}
		m.selfResults = msg.results
		ok := 0
		for _, r := range msg.results {
			if r.OK {
				ok++
			}
		}
		m.flash = "✓ 实测完成：" + strconv.Itoa(ok) + "/" + strconv.Itoa(len(msg.results)) + " 个节点通"
		return m, nil
	case sniPickMsg:
		if msg.err != nil {
			m.flash = "✗ SNI 实测失败: " + msg.err.Error()
			return m, nil
		}
		n, err := m.Store.NodeByTag(msg.tag)
		if err != nil {
			m.flash = "✗ 更新 SNI 失败: " + err.Error()
			return m, nil
		}
		destPort := 443
		if _, port, splitErr := net.SplitHostPort(n.Dest); splitErr == nil {
			if parsed, parseErr := strconv.Atoi(port); parseErr == nil && parsed > 0 && parsed <= 65535 {
				destPort = parsed
			}
		}
		if err := m.Store.UpdateNode(msg.tag, panel.SniNodeUpdates(n, msg.best, destPort)); err != nil {
			m.flash = "✗ 更新 SNI 失败: " + err.Error()
			return m, nil
		}
		m.flash = fmt.Sprintf("✓ 已切换 servername 为 %s；客户端需要重新拉取订阅后才会生效", msg.best.Host)
		return m, nil
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
				if st, err := panel.LoadSettings(); err == nil {
					m.St = st
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
			if st, err := panel.LoadSettings(); err == nil {
				m.St = st
			}
			if m.view == viewTraffic {
				m.loadTraffic()
			}
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
		case "pgup", "b":
			m.cursor = max(0, m.cursor-max(1, m.height-13))
			return m, nil
		case "pgdown", "f", " ":
			m.cursor = min(max(0, m.rowCount()-1), m.cursor+max(1, m.height-13))
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
		case "x":
			if m.view == viewTunnel {
				if m.tunnelBusy {
					return m, nil
				}
				return m.tunnelOff()
			}
			return m, nil
		case "u":
			if m.view == viewTunnel {
				if m.tunnelBusy {
					return m, nil
				}
				return m.tunnelRefresh()
			}
			return m, nil
		case "t":
			if m.view == viewEvents {
				return m.sendTestAlert()
			}
			if m.view == viewSelfTest {
				return m.startSelfTest()
			}
			return m, nil
		case "w":
			if m.view == viewEvents {
				return m.startWebhookForm()
			}
			return m, nil
		case "s":
			if m.view == viewTunnel {
				if m.tunnelBusy {
					return m, nil
				}
				return m.tunnelOn()
			}
			if m.view == viewNodes {
				return m.repickSNI()
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
			if m.view == viewSettings {
				return m.startEditSetting()
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
	case viewSettings:
		return len(editableSettings())
	}
	return len(menuItems)
}

func (m Model) selectMenu() (tea.Model, tea.Cmd) {
	switch m.cursor {
	case 0:
		m.view = viewDoctor
		m.actionOut = m.doctorLines()
	case 1:
		m.view = viewSelfTest
		m.cursor = 0
	case 2:
		m.view = viewNodes
		m.cursor = 0
	case 3:
		m.view = viewUsers
		m.cursor = 0
	case 4:
		m.confirm = "生成配置并重启 xray？（会影响当前连接）"
		m.confirmAction = "apply"
		m.onYes = func() error { return nil }
	case 5:
		m.view = viewEvents
		m.cursor = 0
		m.actionOut = nil
	case 6:
		m.view = viewTraffic
		m.loadTraffic()
	case 7:
		m.view = viewSettings
		m.cursor = 0
	case 8:
		m.view = viewTunnel
		m.tunnelLog = nil
		return m, m.loadTunnelStatus()
	case 9:
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
	case viewSelfTest:
		b.WriteString(m.selfTestView())
	case viewTraffic:
		b.WriteString(m.trafficView())
	case viewSettings:
		b.WriteString(m.settingsView())
	case viewTunnel:
		b.WriteString(m.tunnelView())
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
			lines = append(lines, menuActiveStyle.Render("▸ "+label))
		} else {
			lines = append(lines, menuItemStyle.Render(label))
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
		rows = append(rows, "1-9         直接打开菜单项")
	case viewNodes:
		rows = append(rows, "a 新增   e 编辑   s 重选 SNI   d 删除   选择行后操作")
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
		"\n" + dimStyle.Render("  ↑↓/jk 选择 · a 新增 · e 编辑 · s 重选 SNI · d 删除 · Esc 返回")
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
		hint += "  ·  a 新增  ·  e 编辑  ·  s 重选 SNI  ·  d 删除  ·  r 刷新"
	case viewUsers:
		hint += "  ·  Enter 详情  ·  a 新增  ·  e 编辑  ·  d 删除  ·  r 刷新"
	case viewUserDetail:
		hint += "  ·  c 切换二维码  ·  r 刷新"
	case viewEvents:
		hint += "  ·  w webhook  ·  t 测试告警  ·  r 刷新"
	case viewSelfTest:
		hint += "  ·  t 开始实测  ·  r 刷新"
	case viewTraffic:
		hint += "  ·  r 刷新"
	case viewSettings:
		hint += "  ·  Enter 修改  ·  r 刷新"
	case viewTunnel:
		hint += "  ·  s 开启  ·  x 关闭  ·  u 刷新域名  ·  r 重读状态"
	case viewMenu:
		hint += "  ·  1-9 快捷打开"
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
			{label: "tag", hint: "唯一标识，如 polarisx-reality"},
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

// ---------------------------------------------------------------------------
// 节点自检页：在本机实测节点能不能真的出去
// ---------------------------------------------------------------------------

type selfTestMsg struct {
	results []panel.SelfTestResult
	err     error
}

// startSelfTest 起一个后台任务跑实测（每个节点要等握手 + 探测，会阻塞好几秒，
// 所以必须异步，不能卡住界面）。
func (m Model) startSelfTest() (tea.Model, tea.Cmd) {
	st := m.currentSettings()
	run := m.SelfTest
	// 注入了实现就不再检查二进制：调用方已经接管了"怎么测"
	if run == nil && panel.FindXray(st) == "" {
		m.flash = "✗ 找不到 xray 二进制（设置 xray_bin 或装到 /usr/local/bin/xray）"
		return m, nil
	}
	nodes, err := m.Store.Nodes(true)
	if err != nil {
		m.flash = "✗ 读节点失败: " + err.Error()
		return m, nil
	}
	if len(nodes) == 0 {
		m.flash = "⚠ 没有启用的节点"
		return m, nil
	}
	m.selfTesting = true
	m.selfResults = nil
	m.flash = "正在实测 " + strconv.Itoa(len(nodes)) + " 个节点…"
	store := m.Store
	return m, func() tea.Msg {
		if run != nil {
			return selfTestMsg{results: run(store, st, panel.FindXray(st), nodes, "", 18080, 25*time.Second)}
		}
		return selfTestMsg{results: panel.SelfTestAll(store, st, panel.FindXray(st),
			nodes, "", 18080, 25*time.Second)}
	}
}

func (m Model) selfTestView() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("节点自检") + "\n\n")
	b.WriteString(dimStyle.Render("  在本机起一个 xray 客户端，经它的 socks 端口访问外网；") + "\n")
	b.WriteString(dimStyle.Render("  确认节点真的能出去，并且出口 IP 就是这台落地机。") + "\n\n")

	if m.selfTesting {
		b.WriteString(warnStyle.Render("  正在实测，请稍候…（每个节点最长约 25 秒，Esc 可返回）") + "\n")
		return b.String()
	}
	if len(m.selfResults) == 0 {
		b.WriteString("  " + dimStyle.Render("按 t 开始实测") + "\n")
		return b.String()
	}

	bad, skipped, tested := 0, 0, 0
	for _, r := range m.selfResults {
		if r.Skipped {
			skipped++
			continue
		}
		tested++
		if !r.OK {
			bad++
		}
	}
	for _, r := range m.selfResults {
		mark, style := "✓ 通", okStyle
		switch {
		case r.Skipped:
			mark, style = "— 跳过", dimStyle
		case !r.OK:
			mark, style = "✗ 不通", errStyle
		}
		line := "  " + style.Render(mark) + "  " + r.Tag + dimStyle.Render("（"+r.Proto+"）")
		if r.Latency != "" {
			line += "  " + r.Latency
		}
		if r.ExitIP != "" {
			line += "  " + dimStyle.Render("出口 "+r.ExitIP)
		}
		b.WriteString(line + "\n")
		if r.Err != "" {
			b.WriteString("        " + errStyle.Render(r.Err) + "\n")
		}
	}
	b.WriteString("\n")
	switch {
	case bad > 0:
		b.WriteString(errStyle.Render("  "+strconv.Itoa(bad)+"/"+strconv.Itoa(tested)+" 个节点不通") + "\n")
	case skipped > 0:
		b.WriteString(warnStyle.Render("  全部可用（"+strconv.Itoa(skipped)+" 个跳过）") + "\n")
	default:
		b.WriteString(okStyle.Render("  全部节点可用") + "\n")
	}
	b.WriteString(dimStyle.Render("  若客户端仍超时，问题在客户端或本地网络，不在服务端。") + "\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// 流量统计页：近 24 小时 / 7 天 / 30 天的用量与迷你图
// ---------------------------------------------------------------------------

// loadTraffic 取三个时间窗的数据。View() 每次按键都会跑，所以查询只在这里做。
func (m *Model) loadTraffic() {
	now := time.Now()
	m.traffic24, _ = m.Store.TrafficSeries(now, 24)
	m.traffic7, _ = m.Store.TrafficSeries(now, 24*7)
	m.traffic30, _ = m.Store.TrafficSeries(now, 24*30)
}

func (m Model) trafficView() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("流量统计") + "\n\n")
	// TrafficSeries 会把没有流量的空桶也返回（图表才连得上），所以"有没有数据"
	// 要看总量而不是长度。
	if up, down := panel.TrafficTotal(m.traffic24); up+down == 0 {
		b.WriteString("  " + dimStyle.Render("最近 24 小时没有流量记录（有流量后这里会出现曲线）") + "\n")
		return b.String()
	}

	// 近 24 小时：迷你图 + 峰值标记
	vals := make([]int64, len(m.traffic24))
	peak, peakAt := int64(0), 0
	for i, p := range m.traffic24 {
		vals[i] = p.Up + p.Down
		if vals[i] > peak {
			peak, peakAt = vals[i], i
		}
	}
	fmt.Fprintf(&b, "  %s  %s\n", headerStyle.Render("近 24 小时"), panel.TrafficSummary(m.traffic24))
	b.WriteString("  " + selStyle.Render(panel.Sparkline(vals)) + "\n")
	if peak > 0 {
		b.WriteString("  " + strings.Repeat(" ", peakAt) + warnStyle.Render("↑峰值 "+
			panel.HumanBytes(peak)) + "\n")
	}
	fmt.Fprintf(&b, "  %s\n\n", dimStyle.Render(m.traffic24[0].Label+" … "+
		m.traffic24[len(m.traffic24)-1].Label))

	// 7 天 / 30 天：只要合计
	if len(m.traffic7) > 0 {
		fmt.Fprintf(&b, "  %-12s %s\n", "近 7 天", panel.TrafficSummary(m.traffic7))
	}
	if len(m.traffic30) > 0 {
		fmt.Fprintf(&b, "  %-12s %s\n", "近 30 天", panel.TrafficSummary(m.traffic30))
	}
	if len(m.traffic7) > 0 {
		vals7 := make([]int64, len(m.traffic7))
		for i, p := range m.traffic7 {
			vals7[i] = p.Up + p.Down
		}
		b.WriteString("\n  " + dimStyle.Render("近 7 天（按小时）") + "\n  " +
			dimStyle.Render(panel.Sparkline(vals7)) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("  数据来自采集器每小时的增量；只保留最近 30 天。") + "\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// 设置页：把最常用的几项做成可编辑，带校验
// ---------------------------------------------------------------------------

// editableSetting 描述一个可在 TUI 里改的设置项。
type editableSetting struct {
	key   string // settings.json 里的键名
	label string
	hint  string
	pick  []string // 非空 = 枚举，用左右键选
	// check 返回错误则拒绝保存（空值等业务规则放这里，不要只靠 pick 限制）
	check func(v string) error
}

// editableSettings 是 TUI 里开放的设置项。故意只放"改了不会把服务弄坏"的：
// xray 路径、重启命令这类留着用 CLI 改，改错代价太大。
func editableSettings() []editableSetting {
	return []editableSetting{
		{key: "server_host", label: "落地机地址",
			hint: "节点没单独配入口地址时用它；填域名或 IP"},
		{key: "sub_base", label: "订阅地址前缀",
			hint: "客户端拿到的订阅链接前缀，如 http://1.2.3.4:61887",
			check: func(v string) error {
				if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
					return fmt.Errorf("要以 http:// 或 https:// 开头（留空则用落地机地址拼）")
				}
				return nil
			}},
		{key: "quota_reset_day", label: "每月重置日",
			hint: "1-28，避免 29-31 在短月里跳过", pick: []string{
				"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14",
				"15", "16", "17", "18", "19", "20", "21", "22", "23", "24", "25", "26", "27", "28"}},
		{key: "clash_rules", label: "分流预设", pick: []string{"myclash", "community"},
			hint: "订阅里 Clash 的分流规则集"},
		{key: "outbound_domain_strategy", label: "出口 IP 族",
			pick: []string{"UseIPv4", "AsIs", "UseIP", "UseIPv6"},
			hint: "改了要重新生成 xray 配置才生效"},
		{key: "sub_title", label: "订阅名", hint: "留空用默认名"},
	}
}

// settingValue 读当前值（统一转成字符串，表单只认字符串）。
func settingValue(st panel.Settings, key string) string {
	switch key {
	case "server_host":
		return st.ServerHost
	case "sub_base":
		return st.SubBase
	case "quota_reset_day":
		return strconv.Itoa(st.QuotaResetDay)
	case "clash_rules":
		return st.ClashRules
	case "outbound_domain_strategy":
		return st.OutboundDomainStrategy
	case "sub_title":
		return st.SubTitle
	}
	return ""
}

// applySetting 写回一项设置（只认白名单里的键，别的一律拒绝）。
func applySetting(st *panel.Settings, key, val string) error {
	switch key {
	case "server_host":
		st.ServerHost = val
	case "sub_base":
		st.SubBase = val
	case "quota_reset_day":
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 || n > 28 {
			return fmt.Errorf("重置日只能是 1-28（避免 29-31 在短月里跳过）")
		}
		st.QuotaResetDay = n
	case "clash_rules":
		st.ClashRules = val
	case "outbound_domain_strategy":
		st.OutboundDomainStrategy = val
	case "sub_title":
		st.SubTitle = val
	default:
		return fmt.Errorf("不支持在 TUI 里改 %s", key)
	}
	return nil
}

func (m Model) settingsView() string {
	st := m.currentSettings()
	items := editableSettings()
	var b strings.Builder
	b.WriteString(headerStyle.Render("设置") + "\n\n")
	for i, it := range items {
		cursor := "  "
		if i == m.cursor {
			cursor = selStyle.Render("▸ ")
		}
		val := settingValue(st, it.key)
		if strings.TrimSpace(val) == "" {
			val = dimStyle.Render("(空)")
		}
		fmt.Fprintf(&b, "  %s%-12s %s\n", cursor, it.label, val)
		fmt.Fprintf(&b, "      %s\n", dimStyle.Render(it.hint))
	}
	b.WriteString("\n" + dimStyle.Render("  只开放这几项：xray 路径、重启命令这类改错代价大，用 CLI 改。") + "\n")
	b.WriteString(dimStyle.Render("  改「出口 IP 族」后要跑一次 apply（主菜单第 5 项）才对 xray 生效。") + "\n")
	return b.String()
}

func (m Model) startEditSetting() (tea.Model, tea.Cmd) {
	items := editableSettings()
	if m.cursor >= len(items) {
		return m, nil
	}
	it := items[m.cursor]
	st := m.currentSettings()
	m.form = newForm("修改设置 · "+it.label, []field{
		{label: it.key, value: settingValue(st, it.key), hint: it.hint, pick: it.pick},
	}, "留空表示清空这一项", func(v []string) (bool, string, error) {
		val := strings.TrimSpace(v[0])
		if it.check != nil {
			if err := it.check(val); err != nil {
				return true, "", err
			}
		}
		st, err := panel.LoadSettings()
		if err != nil {
			return true, "", err
		}
		if err := applySetting(&st, it.key, val); err != nil {
			return true, "", err
		}
		if err := panel.SaveSettings(st); err != nil {
			return true, "", err
		}
		msg := "✓ 已保存 " + it.label
		if it.key == "outbound_domain_strategy" {
			msg += "（需跑一次 apply 才对 xray 生效）"
		}
		return false, msg, nil
	})
	return m, nil
}

// ---------------------------------------------------------------------------
// 免域名隧道页
// ---------------------------------------------------------------------------

type tunnelMsg struct {
	action string
	state  *panel.TunnelState
	lines  []string
	err    error
}

// loadTunnelStatus 读状态（要连 metrics 端口，可能几秒，所以异步）。
func (m Model) loadTunnelStatus() tea.Cmd {
	st := m.currentSettings()
	return func() tea.Msg {
		s := panel.TunnelStatus(st, 4*time.Second)
		return tunnelMsg{action: "status", state: &s}
	}
}

func (m Model) tunnelOn() (tea.Model, tea.Cmd) {
	st := m.currentSettings()
	store := m.Store
	m.tunnelBusy = true
	m.tunnelLog = nil
	m.flash = "正在开启隧道…（要等 cloudflared 起来并分配域名）"
	return m, func() tea.Msg {
		var lines []string
		say := func(s string) { lines = append(lines, s) }
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		// TUI 里不自动下载 cloudflared：那是上百 MB 的下载，交给 CLI 显式做
		st2, err := panel.TunnelOn(ctx, store, st, false, 60, say)
		_ = st2
		return tunnelMsg{action: "on", lines: lines, err: err}
	}
}

func (m Model) tunnelOff() (tea.Model, tea.Cmd) {
	st := m.currentSettings()
	store := m.Store
	m.tunnelBusy = true
	m.tunnelLog = nil
	m.flash = "正在关闭隧道…"
	return m, func() tea.Msg {
		var lines []string
		say := func(s string) { lines = append(lines, s) }
		_, err := panel.TunnelOff(store, st, say)
		return tunnelMsg{action: "off", lines: lines, err: err}
	}
}

func (m Model) tunnelRefresh() (tea.Model, tea.Cmd) {
	st := m.currentSettings()
	store := m.Store
	m.tunnelBusy = true
	m.flash = "正在刷新隧道域名…"
	return m, func() tea.Msg {
		var lines []string
		say := func(s string) { lines = append(lines, s) }
		_, _, err := panel.TunnelRefresh(store, st, say)
		return tunnelMsg{action: "refresh", lines: lines, err: err}
	}
}

func (m Model) tunnelView() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("免域名隧道") + "\n\n")
	b.WriteString(dimStyle.Render("  用 Cloudflare 临时隧道给订阅套一个域名：没有自己的域名也能给客户端一个 https 地址。") + "\n")
	b.WriteString(dimStyle.Render("  临时域名每次重启都会变，订阅地址跟着变——客户端需要重新拉取订阅。") + "\n\n")

	if m.tunnelBusy {
		b.WriteString(warnStyle.Render("  处理中，请稍候…") + "\n")
		for _, l := range m.tunnelLog {
			b.WriteString("    " + l + "\n")
		}
		return b.String()
	}
	s := m.tunnelState
	if s == nil {
		b.WriteString("  " + dimStyle.Render("按 r 读取当前状态") + "\n")
		return b.String()
	}
	state := errStyle.Render("关闭")
	if s.Enabled {
		state = okStyle.Render("开启")
	}
	fmt.Fprintf(&b, "  %-14s %s\n", "隧道", state)
	fmt.Fprintf(&b, "  %-14s %s\n", "cloudflared", firstOr(s.Cloudflared,
		errStyle.Render("找不到（用 `polarisx tunnel on --download` 装）")))
	fmt.Fprintf(&b, "  %-14s %s\n", "隧道指向", s.Target)
	fmt.Fprintf(&b, "  %-14s %s\n", "metrics", s.Metrics)
	fmt.Fprintf(&b, "  %-14s %s\n", "当前域名", firstOr(s.LiveHost, dimStyle.Render("取不到（服务没跑？）")))
	fmt.Fprintf(&b, "  %-14s %s\n", "订阅里的域名", firstOr(s.RecordedHost, dimStyle.Render("(空)")))
	fmt.Fprintf(&b, "  %-14s %s\n", "订阅地址前缀", s.SubBase)
	for _, u := range panel.TunnelUnitNames() {
		if v, ok := s.Units[u]; ok {
			fmt.Fprintf(&b, "  %-14s %s\n", u, firstOr(v, "unknown"))
		}
	}
	if s.Warn != "" {
		b.WriteString("\n  " + warnStyle.Render("⚠ "+s.Warn) + "\n")
	}
	if !s.Enabled && s.Cloudflared == "" {
		b.WriteString("\n" + dimStyle.Render("  先装 cloudflared：polarisx tunnel on --download") + "\n")
	}
	for _, l := range m.tunnelLog {
		b.WriteString("  " + dimStyle.Render(l) + "\n")
	}
	return b.String()
}

// firstOr 返回 s，空则返回占位（避免到处写 if）。
func firstOr(s, placeholder string) string {
	if strings.TrimSpace(s) == "" {
		return placeholder
	}
	return s
}
