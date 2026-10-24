package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// form 是逐字段输入表单：文本字段用 bubbles/textinput，枚举字段自己处理左右键。
//
// 为什么用组件库而不是手写：手写要自己实现光标移动、删除、粘贴、长文本滚动、
// 以及 Focus/Blink 生命周期——这些都是 textinput 已经做好的事，自己写只会更差。
// 项目方向就是"用现成的库替代手写"，别为了少一个依赖再退回去。
// 只有一处例外：枚举字段（协议/传输/安全）要的是"左右键循环选值"，textinput 没有这个概念，
// 所以那一层仍然自己写，改完再用 SetValue 同步给 textinput。
type form struct {
	title  string
	fields []field
	inputs []textinput.Model
	cur    int
	err    string
	// doneMsg 是提交成功后要显示在状态行的话（值接收者下直接改 Model 会丢）
	doneMsg string
	// done 返回 (是否留在表单, 状态行消息, 错误)。错误非空则留在表单里显示。
	done func(vals []string) (bool, string, error)
	// hint 显示在底部（比如"reality 密钥会自动生成"）
	hint string
}

type field struct {
	label string
	value string
	hint  string
	// pick 非空时，用左右键在这几个值里循环（枚举字段不用手打）
	pick []string
}

func newForm(title string, fields []field, hint string,
	done func([]string) (bool, string, error)) *form {
	inputs := make([]textinput.Model, len(fields))
	for i, fd := range fields {
		input := textinput.New()
		input.Prompt = ""
		input.Placeholder = fd.hint
		input.Width = 48
		input.SetValue(fd.value)
		input.PromptStyle = selStyle
		input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
		input.PlaceholderStyle = dimStyle
		inputs[i] = input
	}
	if len(inputs) > 0 {
		inputs[0].Focus()
	}
	return &form{title: title, fields: fields, inputs: inputs, hint: hint, done: done}
}

// update 处理一个按键，返回是否还留在表单里。
func (f *form) update(key string) bool {
	switch key {
	case "esc":
		return false
	case "up", "shift+tab":
		if f.cur > 0 {
			f.inputs[f.cur].Blur()
			f.cur--
			f.inputs[f.cur].Focus()
		}
	case "down", "tab", "enter":
		if key == "enter" && f.cur == len(f.fields)-1 {
			f.syncValue()
			vals := make([]string, len(f.fields))
			for i, fd := range f.fields {
				vals[i] = strings.TrimSpace(fd.value)
			}
			keep, msg, err := f.done(vals)
			if err != nil {
				f.err = err.Error()
				return true
			}
			f.doneMsg = msg
			return keep
		}
		if f.cur < len(f.fields)-1 {
			f.inputs[f.cur].Blur()
			f.cur++
			f.inputs[f.cur].Focus()
		}
	case "backspace":
		f.updateInput(key)
	case "ctrl+u":
		// 清空当前字段，编辑长地址/备注时比连续退格更快。
		f.inputs[f.cur].SetValue("")
	case "left", "right":
		fd := &f.fields[f.cur]
		if len(fd.pick) > 0 {
			idx := 0
			for i, p := range fd.pick {
				if p == fd.value {
					idx = i
					break
				}
			}
			if key == "left" {
				idx = (idx - 1 + len(fd.pick)) % len(fd.pick)
			} else {
				idx = (idx + 1) % len(fd.pick)
			}
			fd.value = fd.pick[idx]
			f.inputs[f.cur].SetValue(fd.value)
		} else {
			f.updateInput(key)
		}
	default:
		f.updateInput(key)
	}
	f.syncValue()
	return true
}

func (f *form) updateInput(s string) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	switch s {
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "delete":
		msg = tea.KeyMsg{Type: tea.KeyDelete}
	case "left":
		msg = tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	case "home":
		msg = tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		msg = tea.KeyMsg{Type: tea.KeyEnd}
	}
	next, _ := f.inputs[f.cur].Update(msg)
	f.inputs[f.cur] = next
}

func (f *form) syncValue() {
	for i := range f.fields {
		if i < len(f.inputs) && len(f.fields[i].pick) == 0 {
			f.fields[i].value = f.inputs[i].Value()
		}
	}
}

func (f *form) view(width int) string {
	var b strings.Builder
	b.WriteString(pageStyle.Render("编辑器") + "  " + headerStyle.Render(f.title) + "\n")
	b.WriteString(dimStyle.Render("  带 * 的字段需要填写") + "\n\n")
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-14s", fd.label)
		val := fd.value
		if i == f.cur && len(fd.pick) == 0 {
			val = f.inputs[i].View()
		}
		if val == "" {
			val = dimStyle.Render("(" + fd.hint + ")")
		}
		line := "  " + label + " " + val
		if i == f.cur {
			line = selectedRowStyle.Render(selStyle.Render("▸ " + label + " " + val))
		}
		b.WriteString(line + "\n")
	}
	if f.err != "" {
		b.WriteString("\n" + errStyle.Render("  ✗ "+f.err) + "\n")
	}
	if f.hint != "" {
		b.WriteString("\n" + dimStyle.Render("  "+f.hint) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("  ↑↓/Tab 切换 · ←→ 选择 · Ctrl+U 清空 · Enter 提交 · Esc 取消") + "\n")
	inner := max(24, width-6)
	return boxStyle.Width(inner).Render(b.String())
}
