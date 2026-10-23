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
	"time"

	"github.com/PPX-LuBing/polarisx/internal/panel"
	"github.com/PPX-LuBing/polarisx/internal/sub"
	"github.com/PPX-LuBing/polarisx/internal/tui"
	"github.com/PPX-LuBing/polarisx/internal/web"

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
	case "web":
		err = cmdWeb(args)
	case "node":
		err = cmdNode(args)
	case "user":
		err = cmdUser(args)
	case "apply":
		err = cmdApply(args)
	case "set":
		err = cmdSet(args)
	case "collect":
		err = cmdCollect(args)
	case "migrate":
		err = cmdMigrate(args)
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
  web [--listen ADDR]  启动内嵌 Web 面板（默认 127.0.0.1:8090，含 /sub/ 订阅）
  node <add|list|set|del>    节点管理
  user <add|list|set|del|link>  用户与订阅管理
  apply [--no-restart] 生成配置并让运行中的 xray 生效（adu，失败回退重启）
  set k=v [k=v ...]    改设置（键名与 settings.json 一致）
  collect [--no-kick]  采集一次流量并做额度检查（给 systemd 定时器用）
  migrate              检查能否直接接管 Python 版的数据目录（只读）
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

func cmdNode(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: polarisx node <add|list|set|del>")
	}
	sub, rest := args[0], args[1:]
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	switch sub {
	case "list":
		nodes, err := store.Nodes(false)
		if err != nil {
			return err
		}
		fmt.Printf("%-4s %-14s %-12s %-8s %-10s %-7s %-6s %s\n",
			"id", "tag", "proto", "net", "security", "port", "状态", "入口")
		for _, n := range nodes {
			state := "启用"
			if n.Enabled == 0 {
				state = "停用"
			}
			entry := n.EntryHost
			if entry == "" {
				entry = "(本机)"
			}
			fmt.Printf("%-4d %-14s %-12s %-8s %-10s %-7d %-6s %s:%d\n",
				n.ID, n.Tag, n.Proto, n.Net, n.Security, n.Port, state, entry,
				firstNonZero(n.EntryPort, n.Port))
			if probs := panel.NodeProblems(n); len(probs) > 0 {
				fmt.Printf("     ⚠ %s\n", strings.Join(probs, "; "))
			}
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("node add", flag.ExitOnError)
		var n panel.Node
		fs.StringVar(&n.Tag, "tag", "", "节点 tag（唯一）")
		fs.StringVar(&n.Remark, "remark", "", "显示名")
		fs.StringVar(&n.Proto, "proto", "vless", "vless/vmess/trojan/shadowsocks/hysteria2")
		fs.StringVar(&n.Net, "net", "tcp", "tcp/ws/httpupgrade/xhttp/grpc/hysteria")
		fs.StringVar(&n.Security, "security", "none", "reality/tls/none")
		fs.IntVar(&n.Port, "port", 0, "监听端口")
		fs.StringVar(&n.Listen, "listen", "0.0.0.0", "监听地址")
		fs.StringVar(&n.EntryHost, "entry-host", "", "客户端连接地址（逗号分隔多入口）")
		fs.IntVar(&n.EntryPort, "entry-port", 0, "客户端连接端口，0=同 port")
		fs.StringVar(&n.EntrySNI, "entry-sni", "", "入口层 SNI")
		fs.StringVar(&n.SNI, "sni", "", "reality/tls 的 SNI")
		fs.StringVar(&n.Dest, "dest", "", "reality 伪装目标 host:port")
		fs.StringVar(&n.PrivKey, "priv-key", "", "reality 私钥")
		fs.StringVar(&n.PubKey, "pub-key", "", "reality 公钥")
		fs.StringVar(&n.ShortID, "short-id", "", "reality shortId")
		fs.StringVar(&n.Fingerprint, "fingerprint", "chrome", "客户端指纹")
		fs.StringVar(&n.Path, "path", "", "ws/xhttp 路径")
		fs.StringVar(&n.WSHost, "ws-host", "", "ws/xhttp Host")
		fs.StringVar(&n.GRPCService, "grpc-service", "", "grpc serviceName")
		fs.StringVar(&n.CertFile, "cert", "", "证书文件")
		fs.StringVar(&n.KeyFile, "key", "", "私钥文件")
		fs.StringVar(&n.Method, "method", "", "shadowsocks 加密方式（SS2022）")
		fs.StringVar(&n.SSKey, "ss-key", "", "shadowsocks 服务端 PSK（留空自动生成）")
		fs.StringVar(&n.Flow, "flow", "", "vless flow（如 xtls-rprx-vision）")
		fs.IntVar(&n.Hy2Up, "up-mbps", 0, "hysteria2 上行提示")
		fs.IntVar(&n.Hy2Down, "down-mbps", 0, "hysteria2 下行提示")
		_ = fs.Parse(rest)
		if n.Tag == "" {
			return fmt.Errorf("必须给 --tag")
		}
		if n.Port == 0 {
			return fmt.Errorf("必须给 --port")
		}
		if n.SSKey == "" && n.Proto == "shadowsocks" {
			size := panel.SS2022KeySize[n.Method]
			if size == 0 {
				return fmt.Errorf("shadowsocks 只支持 SS2022 加密方式")
			}
			k, err := panel.GenPSK(size)
			if err != nil {
				return err
			}
			n.SSKey = k
		}
		if err := store.CreateNode(n); err != nil {
			return err
		}
		if got, err := store.NodeByTag(n.Tag); err == nil && got.SSKey != "" {
			fmt.Printf("节点 %s 已创建（服务端 PSK: %s）\n", n.Tag, got.SSKey)
		} else {
			fmt.Printf("节点 %s 已创建\n", n.Tag)
		}
		return nil
	case "set":
		if len(rest) < 2 {
			return fmt.Errorf("用法: polarisx node set <tag> key=value ...")
		}
		if err := store.UpdateNode(rest[0], rest[1:]); err != nil {
			return err
		}
		fmt.Printf("节点 %s 已更新\n", rest[0])
		return nil
	case "del", "delete", "rm":
		if len(rest) < 1 {
			return fmt.Errorf("用法: polarisx node del <tag>")
		}
		if err := store.DeleteNode(rest[0]); err != nil {
			return err
		}
		fmt.Printf("节点 %s 已删除\n", rest[0])
		return nil
	}
	return fmt.Errorf("未知子命令: node %s", sub)
}

func cmdUser(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: polarisx user <add|list|set|del|link>")
	}
	sub, rest := args[0], args[1:]
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	switch sub {
	case "list":
		users, err := store.Users()
		if err != nil {
			return err
		}
		fmt.Printf("%-4s %-14s %-12s %-12s %-12s %-6s %s\n",
			"id", "用户", "已用", "额度", "到期", "状态", "节点")
		for _, u := range users {
			q := "不限"
			if qb := panel.QuotaBytes(u, st); qb > 0 {
				q = panel.HumanBytes(qb)
			}
			state := "启用"
			if u.Enable == 0 {
				state = "停用"
			}
			expire := u.ExpireAt
			if expire == "" {
				expire = "不过期"
			}
			nodes, _ := store.NodesForUser(u, true)
			fmt.Printf("%-4d %-14s %-12s %-12s %-12s %-6s %d\n", u.ID, u.Name,
				panel.HumanBytes(panel.UsedBytes(u)), q, expire, state, len(nodes))
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("user add", flag.ExitOnError)
		quota := fs.Float64("quota", -1, "额度 GB，-1=用默认")
		expire := fs.String("expire", "", "到期日 YYYY-MM-DD")
		note := fs.String("note", "", "备注")
		nodes := fs.String("nodes", "", "绑定节点 tag（逗号分隔，空=全部）")
		// Go 的 flag 遇到第一个位置参数就停止解析（Python 的 argparse 会继续），
		// 所以这里自己把「名字」摘出来，剩下的交给 flag
		name, flagArgs := splitNameFirst(rest)
		if err := fs.Parse(flagArgs); err != nil {
			return err
		}
		if name == "" {
			return fmt.Errorf("用法: polarisx user add <名字> [--quota GB] [--expire 日期] [--nodes a,b]")
		}
		q := *quota
		if q < 0 {
			q = float64(st.DefaultQuotaGB)
		}
		var tags []string
		if *nodes != "" && strings.ToLower(*nodes) != "all" {
			tags = panel.CSV(*nodes)
		}
		u, err := store.CreateUser(name, q, *expire, *note, tags)
		if err != nil {
			return err
		}
		fmt.Printf("用户 %s 已创建\n", u.Name)
		fmt.Printf("  订阅地址: %s\n", web.SubURLFor(st, u.Token, "/clash"))
		fmt.Printf("  分享链接: %s\n", web.SubURLFor(st, u.Token, "?plain=1"))
		return nil
	case "set":
		if len(rest) < 2 {
			return fmt.Errorf("用法: polarisx user set <名字> key=value ...")
		}
		if err := store.UpdateUser(rest[0], rest[1:]); err != nil {
			return err
		}
		fmt.Printf("用户 %s 已更新\n", rest[0])
		return nil
	case "del", "delete", "rm":
		if len(rest) < 1 {
			return fmt.Errorf("用法: polarisx user del <名字>")
		}
		if err := store.DeleteUser(rest[0]); err != nil {
			return err
		}
		fmt.Printf("用户 %s 已删除\n", rest[0])
		return nil
	case "link":
		if len(rest) < 1 {
			return fmt.Errorf("用法: polarisx user link <名字>")
		}
		u, err := store.UserByName(rest[0])
		if err != nil {
			return fmt.Errorf("用户不存在: %s", rest[0])
		}
		links, err := store.LinksForUser(u, st, true)
		if err != nil {
			return err
		}
		fmt.Println(strings.Join(links, "\n"))
		fmt.Printf("\nClash 订阅: %s\n", web.SubURLFor(st, u.Token, "/clash"))
		fmt.Printf("sing-box:  %s\n", web.SubURLFor(st, u.Token, "/singbox"))
		return nil
	}
	return fmt.Errorf("未知子命令: user %s", sub)
}

func cmdApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	noRestart := fs.Bool("no-restart", false, "只写配置，不碰运行中的 xray")
	_ = fs.Parse(args)
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Apply(st, !*noRestart); err != nil {
		return err
	}
	cfg, _ := store.BuildConfig(st)
	inb, _ := cfg["inbounds"].([]any)
	fmt.Printf("已生成 %s（%d 个 inbound）\n", st.XrayConfig, len(inb))
	if !*noRestart {
		fmt.Println("已让运行中的 xray 生效（adu，失败时回退重启）")
	}
	return nil
}

func cmdSet(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: polarisx set key=value [key=value ...]")
	}
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	cur, err := panel.MarshalIndent(st)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(cur), &m); err != nil {
		return err
	}
	for _, kv := range args {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("参数格式: key=value")
		}
		if _, exists := m[k]; !exists {
			return fmt.Errorf("未知设置项 %s", k)
		}
		m[k] = v // 类型由 Settings 的容错解析统一处理
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var next panel.Settings
	if err := json.Unmarshal(b, &next); err != nil {
		return err
	}
	if err := panel.SaveSettings(next); err != nil {
		return err
	}
	out, _ := panel.MarshalIndent(next)
	var keys []string
	for _, kv := range args {
		k, _, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
	}
	var show map[string]any
	_ = json.Unmarshal([]byte(out), &show)
	for _, k := range keys {
		fmt.Printf("%-26s = %v\n", k, show[k])
	}
	return nil
}

func openStore() (*panel.Store, error) { return panel.Open(panel.DBPath()) }

// splitNameFirst 把 ["alice","--quota","5"] 拆成 ("alice", ["--quota","5"])：
// 支持位置参数写在 flag 前面或后面（与 Python 版 CLI 的手感一致）。
func splitNameFirst(args []string) (string, []string) {
	var name string
	var flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		if name == "" {
			name = a
			continue
		}
		flags = append(flags, a)
	}
	return name, flags
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func cmdCollect(args []string) error {
	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	noKick := fs.Bool("no-kick", false, "不踢掉已超额用户的在线连接")
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
	rep, err := store.Collect(st, time.Now(), !*noKick)
	if err != nil {
		return err
	}
	fmt.Printf("采集: +%s ↑ / %s ↓，停用 %d，预警 %d\n",
		panel.HumanBytes(rep.AddedUp), panel.HumanBytes(rep.AddedDown),
		len(rep.Disabled), len(rep.Warned))
	for _, d := range rep.Disabled {
		fmt.Printf("  停用 %s: %s\n", d.Name, d.Reason)
	}
	for _, w := range rep.Warned {
		fmt.Printf("  预警 %s: %.1f%% (%s / %s)\n", w.Name, w.Pct,
			panel.HumanBytes(w.Used), panel.HumanBytes(w.Quota))
	}
	return nil
}

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	_ = fs.Parse(args)
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	fmt.Printf("检查 %s 能否被 %s 直接接管（只读，不改任何东西）\n\n", panel.Home(), panel.ProductName)
	bad := 0
	for _, it := range panel.CheckCompat(st) {
		mark := "[ok]  "
		switch it.Level {
		case "warn":
			mark = "[..]  "
		case "bad":
			mark = "[!!]  "
			bad++
		}
		fmt.Println(mark + it.Message)
	}
	fmt.Println()
	if bad > 0 {
		fmt.Println("有阻塞项，先解决再接管。")
		os.Exit(1)
	}
	fmt.Println("可以直接接管：数据库与设置都是同一份，换二进制即可。")
	fmt.Println("回滚：把 ixpanel 的 systemd 单元起回来（数据没被改过）。")
	return nil
}

func cmdWeb(args []string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8090", "监听地址（默认只绑本机）")
	_ = fs.Parse(args)
	srv, err := web.New(panel.DBPath())
	if err != nil {
		return err
	}
	defer srv.Close()
	fmt.Printf("Web 面板: http://%s/\n", *listen)
	return srv.ListenAndServe(*listen)
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
