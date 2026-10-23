// Package tui 是 PolarisX 的终端界面（bubbletea + lipgloss）。
//
// 只依赖这两个库：表格/状态框用 lipgloss 自己拼，够用就不引 bubbles。
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PPX-LuBing/polarisx/internal/panel"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250"))
)

// view 是当前页面。
type view int

const (
	viewMenu view = iota
	viewNodes
	viewUsers
	viewDoctor
	viewApply
)

var menuItems = []string{
	"总览与自检",
	"节点管理",
	"用户管理",
	"生成配置并重启 xray",
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
}

// New 构造 TUI 模型。
func New(store *panel.Store, st panel.Settings) Model {
	return Model{Store: store, St: st, width: 80, height: 24}
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
		switch msg.String() {
		case "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "q":
			if m.view == viewMenu {
				m.quit = true
				return m, tea.Quit
			}
			m.view = viewMenu
			m.cursor = 0
			return m, nil
		case "esc", "backspace":
			m.view = viewMenu
			m.cursor = 0
			return m, nil
		case "r":
			m.apiKnown = false
			m.flash = ""
			m.refreshStatus()
			return m, nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "j":
			limit := len(menuItems)
			if m.view == viewMenu && m.cursor < limit-1 {
				m.cursor++
			}
			return m, nil
		case "enter":
			if m.view == viewMenu {
				return m.selectMenu()
			}
			return m, nil
		}
	}
	return m, nil
}

func (m Model) selectMenu() (tea.Model, tea.Cmd) {
	switch m.cursor {
	case 0:
		m.view = viewDoctor
		m.actionOut = m.doctorLines()
	case 1:
		m.view = viewNodes
	case 2:
		m.view = viewUsers
	case 3:
		m.view = viewApply
		m.actionOut = m.applyLines()
	case 4:
		m.quit = true
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) View() string {
	m.refreshStatus()
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n")
	switch m.view {
	case viewMenu:
		b.WriteString(m.menuView())
	case viewNodes:
		b.WriteString(m.nodesView())
	case viewUsers:
		b.WriteString(m.usersView())
	case viewDoctor, viewApply:
		b.WriteString(m.textView())
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
	line2 := strings.Join([]string{
		dot(m.apiKnown && m.apiOK, xray),
		fmt.Sprintf("节点 %d/%d", enabled, len(nodes)),
		fmt.Sprintf("用户 %d", len(users)),
		dimStyle.Render("分流 " + st.ClashRules),
		dimStyle.Render("出口 " + st.OutboundDomainStrategy),
	}, "   ")
	return line1 + "\n" + line2
}

func (m Model) menuView() string {
	var lines []string
	for i, it := range menuItems {
		if i == m.cursor {
			lines = append(lines, selStyle.Render("▸ "+it))
		} else {
			lines = append(lines, "  "+it)
		}
	}
	if m.flash != "" {
		lines = append(lines, "", warnStyle.Render(m.flash))
	}
	return boxStyle.Width(m.width-4).Render(strings.Join(lines, "\n")) + "\n"
}

// table 用 lipgloss 拼对齐表格（列宽自适应内容，够用就不引 bubbles/table）。
func table(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && lipgloss.Width(c) > widths[i] {
				widths[i] = lipgloss.Width(c)
			}
		}
	}
	pad := func(s string, w int) string {
		return s + strings.Repeat(" ", w-lipgloss.Width(s))
	}
	var b strings.Builder
	for i, h := range headers {
		b.WriteString(headerStyle.Render(pad(h, widths[i])))
		if i < len(headers)-1 {
			b.WriteString("  ")
		}
	}
	b.WriteString("\n")
	for _, r := range rows {
		for i := range headers {
			c := ""
			if i < len(r) {
				c = r[i]
			}
			b.WriteString(pad(c, widths[i]))
			if i < len(headers)-1 {
				b.WriteString("  ")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) nodesView() string {
	nodes, _ := m.Store.Nodes(false)
	rows := [][]string{}
	for _, n := range nodes {
		state := okStyle.Render("启用")
		if n.Enabled == 0 {
			state = dimStyle.Render("停用")
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
		if probs := panel.NodeProblems(n); len(probs) > 0 {
			note = errStyle.Render("⚠ " + probs[0])
		}
		rows = append(rows, []string{
			n.Tag, n.Proto, n.Net, n.Security,
			fmt.Sprintf("%d", n.Port),
			fmt.Sprintf("%s:%d%s", entry, port, extra),
			state, note,
		})
	}
	head := fmt.Sprintf("节点 %d 个", len(nodes))
	return headerStyle.Render(head) + "\n\n" +
		table([]string{"tag", "协议", "传输", "安全", "端口", "入口", "状态", "问题"}, rows)
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
		state := okStyle.Render("启用")
		if u.Enable == 0 {
			state = errStyle.Render("停用")
			if u.DisabledReason != "" {
				state += dimStyle.Render(" " + u.DisabledReason)
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
	return headerStyle.Render(fmt.Sprintf("用户 %d 个", len(users))) + "\n\n" +
		table([]string{"用户", "已用(上下行合计)", "额度", "到期", "节点", "状态"}, rows)
}

func (m Model) textView() string {
	if len(m.actionOut) == 0 {
		return dimStyle.Render("(无输出)")
	}
	return strings.Join(m.actionOut, "\n")
}

func (m Model) footer() string {
	hint := "↑↓ 选择 · Enter 确认 · q 退出 · r 刷新"
	if m.view != viewMenu {
		hint = "q/Esc 返回 · r 刷新"
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
