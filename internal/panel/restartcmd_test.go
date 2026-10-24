package panel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// restart_cmd 必须走 shell：运维会用 `a && b` 这类串联命令。
// 同时钉住"不要在这一层加黑名单"——信任边界是 settings.json 的属主（root）。
func TestRestartCmdRunsThroughShell(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	marker := filepath.Join(dir, "ran")
	st := Defaults()
	st.XrayConfig = filepath.Join(dir, "config.json")
	// 串联命令 + 重定向：只有经 shell 解释才会产生这个文件
	st.RestartCmd = "true && echo ok > " + marker
	if err := os.WriteFile(st.XrayConfig, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// RestartXray 会等 API（等不到就返回失败），但命令本身应该已经执行过
	_ = RestartXray(st, true)
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("restart_cmd 没被执行（没走 shell？）: %v", err)
	}
	if !strings.Contains(string(b), "ok") {
		t.Errorf("执行结果不对: %q", b)
	}
}
