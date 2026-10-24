package panel

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
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

// 根因修复：xray 以非 root 运行时也要能读到配置、能写日志。
// 这个坑会随每次重装/升级 xray 重演（安装脚本会重写单元），所以不能只靠"把单元改成 root"。
func TestEnsureXrayAccess(t *testing.T) {
	origUser, origChown, origChmod := xrayServiceUser, chownFile, chmodPath
	defer func() { xrayServiceUser, chownFile, chmodPath = origUser, origChown, origChmod }()

	type chownCall struct {
		path     string
		uid, gid int
	}
	var chowns []chownCall
	var chmods []string
	chownFile = func(path string, uid, gid int) error {
		chowns = append(chowns, chownCall{path, uid, gid})
		return nil
	}
	chmodPath = func(path string, mode os.FileMode) error {
		chmods = append(chmods, path)
		return nil
	}

	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := Defaults()

	t.Run("以 root 运行则什么都不做", func(t *testing.T) {
		chowns, chmods = nil, nil
		xrayServiceUser = func() string { return "root" }
		EnsureXrayAccess(st, cfgPath)
		if len(chowns) != 0 {
			t.Errorf("root 运行时不该改属主，实际 %v", chowns)
		}
	})

	t.Run("判断不了也不动", func(t *testing.T) {
		chowns, chmods = nil, nil
		xrayServiceUser = func() string { return "" }
		EnsureXrayAccess(st, cfgPath)
		if len(chowns) != 0 {
			t.Errorf("取不到服务用户时不该改属主，实际 %v", chowns)
		}
	})

	t.Run("非 root 用户：配置交给它，日志也交给它", func(t *testing.T) {
		xrayServiceUser = func() string { return currentUserName(t) }
		// Home() 在包里是缓存的（首个测试解析后就不再看 POLARISX_HOME），
		// 所以先把它的权限明确设成 0700，断言才确定。
		if err := os.Chmod(Home(), 0o700); err != nil {
			t.Fatal(err)
		}
		chowns, chmods = nil, nil
		EnsureXrayAccess(st, cfgPath)

		if len(chowns) < 2 {
			t.Fatalf("应至少改配置与日志两个文件的属主，实际 %v", chowns)
		}
		if chowns[0].path != cfgPath {
			t.Errorf("第一个应是配置文件，实际 %s", chowns[0].path)
		}
		foundLog := false
		for _, c := range chowns {
			if strings.HasSuffix(c.path, "xray-error.log") {
				foundLog = true
			}
		}
		if !foundLog {
			t.Error("日志文件也应交给服务用户（否则 xray 写不了日志）")
		}
		foundHome := false
		for _, p := range chmods {
			if p == Home() {
				foundHome = true
			}
		}
		if !foundHome {
			t.Error("数据目录应加可穿越位（否则服务用户进不去写日志）")
		}
		if perm := mustPerm(t, cfgPath); perm != 0o600 {
			t.Errorf("配置权限应保持 0600（最小权限），实际 %04o", perm)
		}
	})

	t.Run("导出到别处不改属主", func(t *testing.T) {
		chowns, chmods = nil, nil
		xrayServiceUser = func() string { return currentUserName(t) }
		out := filepath.Join(dir, "export.json")
		if err := WriteConfig(st, map[string]any{"inbounds": []any{}}, out, false); err != nil {
			t.Fatal(err)
		}
		if len(chowns) != 0 {
			t.Errorf("导出路径不该改属主，实际 %v", chowns)
		}
	})
}

func currentUserName(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.Username == "" || u.Username == "root" {
		t.Skip("拿不到非 root 的当前用户")
	}
	return u.Username
}

func mustPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}
