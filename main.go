// PolarisX —— Xray 多协议落地机面板（北极星）。
//
// 与 Python 版 ixpanel 共用同一份 settings.json / panel.db，换二进制即可接管。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PPX-LuBing/polarisx/internal/panel"
	"github.com/PPX-LuBing/polarisx/internal/sub"
	"github.com/PPX-LuBing/polarisx/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if len(os.Args) < 2 {
		// 与 Python 版一致：裸跑且是终端时直接进 TUI
		if isTTY() {
			if err := cmdUI(nil); err != nil {
				fmt.Fprintln(os.Stderr, "错误: "+err.Error())
				os.Exit(1)
			}
			return
		}
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "version", "-v", "--version":
		fmt.Printf("polarisx %s (home=%s)\n", panel.Version, panel.Home())
	case "show":
		err = cmdShow(args)
	case "doctor":
		err = cmdDoctor(args)
	case "sub":
		err = cmdSub(args)
	case "ui":
		err = cmdUI(args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		os.Exit(1)
	}
}

// isTTY 判断标准输出是不是终端（管道/重定向时回退到 usage）。
func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func usage() {
	fmt.Print(`polarisx —— Xray 多协议落地机面板（北极星）

用法: polarisx <子命令> [参数]

  version              显示版本与数据目录
  show [--write]       生成 xray 配置并打印（--write 写到 settings 里的 xray_config）
  sub [--listen ADDR]  启动订阅 HTTP 服务（默认取 settings.sub_listen）
  ui                   打开终端界面（交互式控制台）
  doctor               自检（数据库 / 配置 / 节点体检）
  help                 这份帮助
`)
}

func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	write := fs.Bool("write", false, "写到 settings 里的 xray_config（默认只打印）")
	_ = fs.Parse(args)

	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	store, err := panel.Open(panel.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	cfg, err := store.BuildConfig(st)
	if err != nil {
		return err
	}
	b, err := marshalPretty(cfg)
	if err != nil {
		return err
	}
	if !*write {
		_, err = os.Stdout.Write(b)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.XrayConfig), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(st.XrayConfig, b, 0o600); err != nil {
		return err
	}
	fmt.Printf("已生成 %s\n", st.XrayConfig)
	return nil
}

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	_ = fs.Parse(args)
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	store, err := panel.Open(panel.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	m := tui.New(store, st)
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func cmdSub(args []string) error {
	fs := flag.NewFlagSet("sub", flag.ExitOnError)
	listen := fs.String("listen", "", "监听地址，默认取 settings.sub_listen")
	_ = fs.Parse(args)
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	addr := *listen
	if addr == "" {
		addr = st.SubListen
	}
	srv, err := sub.New(panel.DBPath())
	if err != nil {
		return err
	}
	defer srv.Close()
	return srv.ListenAndServe(addr)
}

func cmdDoctor(args []string) error {
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	fmt.Printf("polarisx %s   home=%s\n", panel.Version, panel.Home())
	if _, err := os.Stat(panel.DBPath()); err != nil {
		fmt.Printf("[!!] 数据库不存在: %s（Python 版接管前请先跑一次 install/deploy）\n", panel.DBPath())
		return nil
	}
	store, err := panel.Open(panel.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	nodes, err := store.Nodes(false)
	if err != nil {
		return err
	}
	users, err := store.Users()
	if err != nil {
		return err
	}
	enabled := 0
	bad := 0
	for _, n := range nodes {
		if n.Enabled != 0 {
			enabled++
		}
		if probs := panel.NodeProblems(n); len(probs) > 0 {
			bad++
			fmt.Printf("[!!] 节点 %s 会被跳过: %s\n", n.Tag, strings.Join(probs, "; "))
		}
	}
	fmt.Printf("[ok] 节点 %d 个（启用 %d），用户 %d 个\n", len(nodes), enabled, len(users))
	fmt.Printf("[ok] 出口 IP 族: %s\n", st.OutboundDomainStrategy)
	fmt.Printf("[ok] Clash 分流预设: %s\n", st.ClashRules)

	cfg, err := store.BuildConfig(st)
	if err != nil {
		return err
	}
	inb := cfg["inbounds"].([]any)
	fmt.Printf("[ok] 可生成配置: %d 个 inbound\n", len(inb))
	for _, i := range inb {
		m := i.(map[string]any)
		fmt.Printf("     %-14s %-9s %s:%v\n", m["tag"], m["protocol"], m["listen"], m["port"])
	}
	return nil
}

func marshalPretty(v any) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}
