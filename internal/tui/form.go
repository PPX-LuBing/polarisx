package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// form 是一个极简的逐字段输入表单。
//
// 为什么不引 bubbles/textinput：这里只需要"几个单行字段 + 上下切换 + 回车提交"，
// 一个 80 行的结构体就够了；引组件库还得处理它的 Focus/Blink 生命周期。
type form struct {
	title  string
	fields []field
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
	return &form{title: title, fields: fields, hint: hint, done: done}
}

// update 处理一个按键，返回是否还留在表单里。
func (f *form) update(key string) bool {
	switch key {
	case "esc":
		return false
	case "up", "shift+tab":
		if f.cur > 0 {
			f.cur--
		}
	case "down", "tab", "enter":
		if key == "enter" && f.cur == len(f.fields)-1 {
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
			f.cur++
		}
	case "backspace":
		v := f.fields[f.cur].value
		if v != "" {
			f.fields[f.cur].value = v[:len(v)-1]
		}
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
		}
	default:
		// 可打印字符
		if len(key) == 1 && key >= " " {
			f.fields[f.cur].value += key
		}
	}
	return true
}

func (f *form) view(width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(f.title) + "\n\n")
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-14s", fd.label)
		val := fd.value
		if val == "" {
			val = dimStyle.Render("(" + fd.hint + ")")
		}
		line := "  " + label + " " + val
		if i == f.cur {
			line = selStyle.Render("▸ " + label + " " + val)
		}
		b.WriteString(line + "\n")
	}
	if f.err != "" {
		b.WriteString("\n" + errStyle.Render("  ✗ "+f.err) + "\n")
	}
	if f.hint != "" {
		b.WriteString("\n" + dimStyle.Render("  "+f.hint) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("  ↑↓ 切换字段 · ←→ 选枚举 · 回车提交 · Esc 取消") + "\n")
	return lipgloss.NewStyle().Width(width - 4).Render(b.String())
}
