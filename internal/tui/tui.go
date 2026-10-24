// Package tui 是 PolarisX 的终端界面（bubbletea + lipgloss）。
//
// 只依赖这两个库：表格/状态框用 lipgloss 自己拼，够用就不引 bubbles。
package tui

import (
	"fmt"
	"strconv"
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
	// 写操作：表单与删除确认（nil / 空 = 不在该模式）
	form    *form
	confirm string
	onYes   func() error
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
		key := msg.String()
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
				}
			case "n", "N", "esc":
				m.flash = "已取消"
			}
			m.confirm, m.onYes = "", nil
			return m, nil
		}
		switch key {
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
			if m.cursor < m.rowCount()-1 {
				m.cursor++
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
			return m, nil
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
		m.view = viewApply
		m.actionOut = m.applyLines()
	case 4:
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
		table([]string{"tag", "协议", "传输", "安全", "端口", "入口", "状态", "问题"}, rows) +
		"\n" + dimStyle.Render("  a 新增 · e 启用/停用 · d 删除 · r 刷新 · Esc 返回")
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
