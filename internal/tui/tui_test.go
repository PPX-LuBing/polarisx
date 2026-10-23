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
