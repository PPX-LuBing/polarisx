package panel

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// FindXray 找 xray 二进制：settings.xray_bin → 常见路径 → PATH。
func FindXray(st Settings) string {
	if st.XrayBin != "" {
		if _, err := os.Stat(st.XrayBin); err == nil {
			return st.XrayBin
		}
	}
	for _, p := range []string{"/usr/local/bin/xray", "/usr/bin/xray", "/opt/xray/xray"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("xray"); err == nil {
		return p
	}
	return ""
}

// XrayAPI 调一次 xray 的 API 子命令（statsquery / adu / rmu 等）。
func XrayAPI(st Settings, args []string, timeout time.Duration) (int, string, string, error) {
	bin := FindXray(st)
	if bin == "" {
		return -1, "", "", fmt.Errorf("找不到 xray 二进制（设置 xray_bin 或装到 /usr/local/bin/xray）")
	}
	ctx, cancel := contextWithTimeout(timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	rc := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			return -1, out.String(), errb.String(), err
		}
	}
	return rc, out.String(), errb.String(), nil
}

// APIAvailable xray API 是否可达（TUI 状态行与采集都用它）。
func APIAvailable(st Settings, timeout time.Duration) bool {
	rc, _, _, err := XrayAPI(st, []string{"api", "statsquery",
		"--server=" + st.XrayAPI, "-pattern", "user>>>"}, timeout)
	return err == nil && rc == 0
}

// FetchStats 拉 per-user 统计；reset=true 时让 xray 返回「自上次查询以来的增量」。
// 用增量而不是自己做差：xray 重启会让计数器归零，做差会漏记；增量语义下不存在这种错配，
// 两个采集器并发也不会重复计账。
func FetchStats(st Settings, reset bool) (map[string]int64, error) {
	args := []string{"api", "statsquery", "--server=" + st.XrayAPI, "-pattern", "user>>>"}
	if reset {
		args = append(args, "-reset")
	}
	rc, out, errb, err := XrayAPI(st, args, 20*time.Second)
	if err != nil {
		return nil, err
	}
	if rc != 0 {
		return nil, fmt.Errorf("statsquery 失败: %s %s", strings.TrimSpace(out), strings.TrimSpace(errb))
	}
	return ParseStats(out), nil
}

// VerifyConfig 用 `xray -test` 校验配置（校验不过就不该覆盖现有配置）。
func VerifyConfig(st Settings, path string) error {
	bin := FindXray(st)
	if bin == "" {
		return fmt.Errorf("找不到 xray 二进制，无法校验")
	}
	rc, out, errb, err := XrayAPI(st, []string{"run", "-test", "-config", path}, 20*time.Second)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("xray -test 失败: %s %s", strings.TrimSpace(out), strings.TrimSpace(errb))
	}
	return nil
}

// WriteConfig 原子写配置（0600）。verify=true 时先让 xray 校验，不通过就不动现有配置。
func WriteConfig(st Settings, cfg map[string]any, path string, verify bool) error {
	if path == "" {
		path = st.XrayConfig
	}
	body, err := marshalNoEscape(cfg, "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 临时文件必须以 .json 结尾：xray 靠扩展名判断格式
	tmp := path + ".tmp.json"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if verify {
		if err := VerifyConfig(st, tmp); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, path)
}

// RestartXray 按 settings.restart_cmd 重启 xray。
func RestartXray(st Settings, quiet bool) error {
	cmdline := st.RestartCmd
	if cmdline == "" {
		cmdline = "systemctl restart xray"
	}
	// restart_cmd 走 shell 执行，与 Python 版一致（subprocess.run(cmd, shell=True)）：
	// 运维会用 `systemctl restart xray && ...` 这类串联命令，也会用带参数的包装脚本，
	// 拆成 exec(字段[0], 字段[1:]...) 会把它们全部拒掉。
	//
	// 信任边界说明：这个设置只存在 settings.json 里，而该文件是 root:root 0644，
	// 只有 root 能改——能改它的人本来就能执行任意命令，所以这里不需要再做"防注入"。
	// 真要跑不可信的命令，应该先解决"谁能写 settings.json"，而不是在这一层加黑名单。
	ctx, cancel := contextWithTimeout(30 * time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("重启失败 (%s): %s", cmdline, strings.TrimSpace(out.String()))
	}
	return nil
}

// contextWithTimeout 是 context.WithTimeout 的小包装（集中一处便于调整）。
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// XrayConfigReadable 检查 xray 服务的运行身份能不能读到配置文件。
// 读不到时返回一句人话（含怎么修），能读到或判断不了时返回空串。
//
// 为什么值得单独查：官方 Xray-install 脚本建的单元是 User=xray，而面板把配置写成
// 0600 root——xray 于是起不来，日志里只有 "permission denied"，离真正原因很远。
// 全新装一台机器必踩（本仓库踩过）。
func XrayConfigReadable(st Settings) string {
	path := st.XrayConfig
	if path == "" {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "" // 配置还没生成，不是这里的问题
	}
	out, err := exec.Command("systemctl", "show", "-p", "User", "--value", "xray").Output()
	if err != nil {
		return "" // 没有 systemd 或没有 xray 单元，不妄下结论
	}
	who := strings.TrimSpace(string(out))
	if who == "" || who == "root" {
		return ""
	}
	u, err := user.Lookup(who)
	if err != nil {
		return ""
	}
	uid, gid, ok := fileOwner(fi)
	if !ok {
		return ""
	}
	mode := fi.Mode().Perm()
	if modeReadable(mode, uid, gid, u.Uid, gidInGroups(gid, u)) {
		return ""
	}
	return fmt.Sprintf("xray 以 %s 运行，但读不到 %s（权限 %04o，属主 uid=%d）——"+
		"xray 会起不来，日志里只有 permission denied。两种修法：把 %s 的属主/权限放开给 %s，"+
		"或把 /etc/systemd/system/xray.service 的 User 改成 root 后 systemctl daemon-reload && systemctl restart xray",
		who, path, mode, uid, path, who)
}

// modeReadable 判断"以 uid 运行、其主组是否命中文件组"的身份能否读这个文件。
func modeReadable(mode os.FileMode, ownerUID, ownerGID int, userUID string, inGroup bool) bool {
	switch {
	case strconv.Itoa(ownerUID) == userUID:
		return mode&0o400 != 0
	case inGroup:
		return mode&0o040 != 0
	default:
		return mode&0o004 != 0
	}
}

func fileOwner(fi os.FileInfo) (int, int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

func gidInGroups(gid int, u *user.User) bool {
	ids, err := u.GroupIds()
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == strconv.Itoa(gid) {
			return true
		}
	}
	return false
}
