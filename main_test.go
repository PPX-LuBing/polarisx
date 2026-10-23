package main

import (
	"reflect"
	"strings"
	"testing"
)

// Go 的 flag 遇到第一个位置参数就停止解析，所以位置参数与 flag 的顺序都要能处理。
// `tunnel on --download` 这种「动作词在前、选项在后」必须能解析出选项。
// Go 的 flag 遇到第一个位置参数就停止解析，不特殊处理就会静默丢掉 --download。
func TestTunnelActionThenFlags(t *testing.T) {
	action, rest := splitNameFirst([]string{"on", "--download", "--wait", "30"})
	if action != "on" {
		t.Fatalf("动作词应为 on，实际 %q", action)
	}
	joined := strings.Join(rest, " ")
	if !strings.Contains(joined, "--download") || !strings.Contains(joined, "--wait") {
		t.Errorf("选项应保留，实际 %v", rest)
	}
	if got, _ := splitNameFirst([]string{"status"}); got != "status" {
		t.Errorf("无选项时应得到 status，实际 %q", got)
	}
}

func TestSplitNameFirst(t *testing.T) {
	cases := []struct {
		in    []string
		name  string
		flags []string
	}{
		{[]string{"alice", "--quota", "1024"}, "alice", []string{"--quota", "1024"}},
		{[]string{"--quota", "5", "bob"}, "bob", []string{"--quota", "5"}},
		{[]string{"--quota=5", "carol", "--note", "x y"}, "carol",
			[]string{"--quota=5", "--note", "x y"}},
		{[]string{"dave"}, "dave", nil},
		{[]string{"--expire", "2027-01-01", "erin", "--nodes", "a,b"}, "erin",
			[]string{"--expire", "2027-01-01", "--nodes", "a,b"}},
	}
	for _, c := range cases {
		name, flags := splitNameFirst(c.in)
		if name != c.name {
			t.Errorf("%v -> 名字 %q，期望 %q", c.in, name, c.name)
		}
		if !reflect.DeepEqual(flags, c.flags) {
			t.Errorf("%v -> flags %v，期望 %v", c.in, flags, c.flags)
		}
	}
}
