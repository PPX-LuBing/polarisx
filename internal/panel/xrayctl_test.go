package panel

import (
	"os"
	"testing"
)

// doctor 的"xray 读不到配置"判断。官方安装脚本建 User=xray，而配置是 0600 root，
// 于是 xray 起不来、日志只有 permission denied —— 这个判断必须准，否则要么误报、
// 要么漏报（漏报时用户看到的还是那句没头没脑的 permission denied）。
func TestModeReadable(t *testing.T) {
	cases := []struct {
		name    string
		mode    os.FileMode
		owner   int
		userUID string
		inGroup bool
		want    bool
	}{
		{"属主可读", 0o600, 1000, "1000", false, true},
		{"属主只写", 0o200, 1000, "1000", false, false},
		{"root 属主 + 0600 + xray 用户", 0o600, 0, "999", false, false}, // 全新装机器就是这个
		{"root 属主 + 0644 + xray 用户", 0o644, 0, "999", false, true},
		{"组内可读", 0o640, 0, "999", true, true},
		{"组内只写", 0o620, 0, "999", true, false},
		{"其他可读", 0o604, 0, "999", false, true},
	}
	for _, c := range cases {
		got := modeReadable(c.mode, c.owner, 0, c.userUID, c.inGroup)
		if got != c.want {
			t.Errorf("%s: mode=%04o owner=%d user=%s inGroup=%v -> %v，期望 %v",
				c.name, c.mode, c.owner, c.userUID, c.inGroup, got, c.want)
		}
	}
}

// 配置不存在 / 没设路径时不该妄下结论。
func TestXrayConfigReadableQuiet(t *testing.T) {
	st := Defaults()
	st.XrayConfig = "/nonexistent/definitely/not/here.json"
	if msg := XrayConfigReadable(st); msg != "" {
		t.Errorf("配置不存在时不该报错，实际: %s", msg)
	}
	st.XrayConfig = ""
	if msg := XrayConfigReadable(st); msg != "" {
		t.Errorf("没设路径时不该报错，实际: %s", msg)
	}
}
