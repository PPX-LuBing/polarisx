// PolarisX —— Xray 多协议落地机面板（北极星）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
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
	case "sni":
		err = cmdSni(args)
	case "tunnel":
		err = cmdTunnel(args)
	case "qr":
		err = cmdQR(args)
	case "selftest":
		err = cmdSelfTest(args)
	case "alert":
		err = cmdAlert(args)
	case "traffic":
		err = cmdTraffic(args)
	case "status":
		err = cmdStatus(args)
	case "events":
		err = cmdEvents(args)
	case "export":
		err = cmdExport(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "collect":
		err = cmdCollect(args)
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
  sni [--top N] [--apply --node TAG]
                       在本机实测 SNI 候选（TLS1.3+h2+X25519、排除 Cloudflare 前置），
                       按延迟排名；--apply 把最佳结果写到节点的 sni/dest
  qr <用户> [--sub|--clash|--singbox]
                       打印二维码（默认每个节点一张；--clash/--singbox 打订阅地址，
                       后缀写进码里，手机扫码直接能用）
  selftest [--node TAG] [--user NAME] [--json]
                       在本机起 xray 客户端，逐节点实测出口 IP 与延迟
                       （用来分清「服务端坏了」还是「你的网络/客户端问题」）
  alert [--test]        用量告警：--test 发一条测试消息到 alert_webhook
  traffic [--hours N] [--json]
                       最近 N 小时的流量曲线（终端迷你图 + 合计）
  status               一眼看全：xray/节点/用户/订阅/落地机地址
  events [--limit N]   看事件日志（停用/预警/改配置都记在这里）
  export [--config PATH]
                       导出生成的 xray 配置；不带参数则列出用户与用量
  uninstall [--yes] [--purge]
                       卸载面板（--purge 连数据一起删；默认保留 panel.db/settings.json）
  tunnel [status|on|off|refresh]
                       免域名订阅隧道（cloudflared 临时隧道，域名每次重启会变，
                       由 refresh 定时器写回 sub_base）
  collect [--no-kick]  采集一次流量并做额度检查（给 systemd 定时器用）
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
		fs.StringVar(&n.SNI, "sni", "", "reality/tls 的 SNI（填 auto 则本机实测自动挑选）")
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
		// reality 缺密钥时自动生成：原来必须手填 --priv-key/--pub-key/--short-id，
		// 等于用 CLI 建不出一个可用的 reality 节点。
		if n.Security == "reality" && (n.PrivKey == "" || n.PubKey == "" || n.ShortID == "") {
			priv, pub, sid, err := panel.GenerateRealityKeys()
			if err != nil {
				return err
			}
			if n.PrivKey == "" {
				n.PrivKey = priv
			}
			if n.PubKey == "" {
				n.PubKey = pub
			}
			if n.ShortID == "" {
				n.ShortID = sid
			}
			fmt.Printf("已生成 REALITY 密钥对（shortId=%s）\n", n.ShortID)
		}
		// --sni auto：在这台机器上实测候选，自动挑最好的（不用手填、也不硬编码）
		if strings.EqualFold(n.SNI, "auto") {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			cf := panel.LoadCFRanges(ctx, 20*time.Second)
			fmt.Printf("--sni auto：实测 %d 个候选（本机视角）…\n", len(panel.SniCandidates))
			best, _, err := panel.BestSNI(ctx, panel.SniCandidates,
				func(ctx context.Context, host string) panel.SniResult {
					return panel.ProbeTLS(ctx, host, 6*time.Second, cf)
				}, 8)
			cancel()
			if err != nil {
				return err
			}
			n.SNI = best.Host
			if n.Security == "reality" && n.Dest == "" {
				n.Dest = net.JoinHostPort(best.Host, "443")
			}
			fmt.Printf("  选中 %s（%s，合计 %.1fms）\n", best.Host, best.IP, best.Totalms)
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
		// 手动停用同样要摘掉在线连接（Python 版此处 kick=True）
		for _, p := range rest[1:] {
			if p == "enable=0" || p == "enable=false" {
				if u, err := store.UserByName(rest[0]); err == nil {
					if err := store.DisableUser(st, u, "管理员手动停用", true); err != nil {
						return err
					}
				}
				break
			}
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

func cmdSni(args []string) error {
	fs := flag.NewFlagSet("sni", flag.ExitOnError)
	top := fs.Int("top", 10, "显示前 N 名")
	timeout := fs.Duration("timeout", 6*time.Second, "单个候选的探测超时")
	conc := fs.Int("concurrency", 8, "并发数")
	asJSON := fs.Bool("json", false, "输出 JSON")
	apply := fs.Bool("apply", false, "把最佳结果写到节点")
	nodeTag := fs.String("node", "", "--apply 时指定节点 tag")
	destPort := fs.Int("dest-port", 443, "--apply 时 dest 用的端口")
	cands := fs.String("candidates", "", "自定义候选（逗号或空格分隔）")
	_ = fs.Parse(args)

	hosts := panel.SniCandidates
	if *cands != "" {
		hosts = panel.CSV(strings.ReplaceAll(*cands, " ", ","))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fmt.Printf("在本机实测 %d 个 SNI 候选（并发 %d，单个超时 %s）…\n", len(hosts), *conc, *timeout)
	cf := panel.LoadCFRanges(ctx, 20*time.Second)
	fmt.Printf("Cloudflare 网段 %d 条（用于排除被 CF 前置的域名）\n\n", cf.Len())

	probe := func(ctx context.Context, host string) panel.SniResult {
		return panel.ProbeTLS(ctx, host, *timeout, cf)
	}
	best, all, err := panel.BestSNI(ctx, hosts, probe, *conc)

	if *asJSON {
		b, _ := panel.MarshalIndent(map[string]any{"best": best, "all": all})
		fmt.Print(b)
		if err != nil {
			return err
		}
		return nil
	}

	good := panel.RankSNI(all)
	fmt.Printf("可用候选 %d / %d（TLS1.3 + h2 + X25519 全过且未被 CF 前置）\n", len(good), len(all))
	fmt.Printf("%-4s %-26s %-16s %8s %8s %8s  %s\n", "#", "域名", "IP", "TCP", "TLS", "合计", "密钥交换")
	for i, r := range good {
		if i >= *top {
			break
		}
		fmt.Printf("%-4d %-26s %-16s %7.1fms %7.1fms %7.1fms  %s\n",
			i+1, r.Host, r.IP, r.TCPms, r.TLSms, r.Totalms, r.Group)
	}
	if len(good) == 0 {
		fmt.Println("（没有可用候选；下面列几个落选原因）")
		for i, r := range all {
			if i >= 5 {
				break
			}
			fmt.Printf("  %-26s %s\n", r.Host, r.Err)
		}
	}
	if err != nil {
		return err
	}
	if !*apply {
		fmt.Printf("\n最佳: %s（%s，合计 %.1fms）\n", best.Host, best.IP, best.Totalms)
		fmt.Println("要写入节点：polarisx sni --apply --node <tag>")
		return nil
	}
	if *nodeTag == "" {
		return fmt.Errorf("--apply 需要 --node <tag>")
	}
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	n, err := store.NodeByTag(*nodeTag)
	if err != nil {
		return fmt.Errorf("节点不存在: %s", *nodeTag)
	}
	updates := panel.SniNodeUpdates(n, best, *destPort)
	if err := store.UpdateNode(*nodeTag, updates); err != nil {
		return err
	}
	fmt.Printf("\n已把节点 %s 的 SNI 设为 %s\n", *nodeTag, best.Host)
	if n.Security == "reality" {
		fmt.Printf("  dest = %s:%d\n", best.Host, *destPort)
	}
	fmt.Println("  生效：polarisx apply")
	_ = st
	return nil
}

func cmdQR(args []string) error {
	fs := flag.NewFlagSet("qr", flag.ExitOnError)
	sub := fs.Bool("sub", false, "打订阅地址（含全部节点）")
	clash := fs.Bool("clash", false, "订阅地址用 Clash/Mihomo 后缀（隐含 --sub）")
	singbox := fs.Bool("singbox", false, "订阅地址用 sing-box 后缀（隐含 --sub）")
	// 同样要防「位置参数在前、选项在后」被 flag 吞掉（qr alice --sub）
	name, rest := splitNameFirst(args)
	if name == "" {
		return fmt.Errorf("用法: polarisx qr <用户名> [--sub|--clash|--singbox]")
	}
	_ = fs.Parse(rest)
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	store, err := panel.Open(panel.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	u, err := store.UserByName(name)
	if err != nil {
		return fmt.Errorf("用户不存在: %s", name)
	}
	items, err := store.QRForUser(u, st, *sub, *clash, *singbox)
	if err != nil {
		return err
	}
	for _, it := range items {
		label, text, _ := strings.Cut(it, "\n")
		fmt.Printf("%s\n%s\n", label, text)
		art, err := panel.RenderQR(text)
		if err != nil {
			return err
		}
		fmt.Print(art)
		fmt.Println()
	}
	return nil
}

func cmdSelfTest(args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	only := fs.String("node", "", "只测这个 tag")
	uname := fs.String("user", "", "用这个用户测（默认挑第一个能用的）")
	base := fs.Int("port", 18080, "客户端 socks 起始端口（每个节点 +1）")
	timeout := fs.Duration("timeout", 25*time.Second, "单个节点的超时")
	asJSON := fs.Bool("json", false, "输出 JSON")
	showCfg := fs.Bool("show-config", false, "只打印将要用的客户端配置（排障用）")
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

	bin := panel.FindXray(st)
	if bin == "" {
		return fmt.Errorf("找不到 xray 二进制（设置 xray_bin 或装到 /usr/local/bin/xray）")
	}
	nodes, err := store.Nodes(true)
	if err != nil {
		return err
	}
	if *only != "" {
		n, err := store.NodeByTag(*only)
		if err != nil {
			return fmt.Errorf("节点不存在: %s", *only)
		}
		nodes = []panel.Node{n}
	}
	if len(nodes) == 0 {
		return fmt.Errorf("没有启用的节点")
	}

	var results []panel.SelfTestResult
	port := *base
	for _, n := range nodes {
		u, err := pickUserFor(store, n, *uname)
		if err != nil {
			results = append(results, panel.SelfTestResult{Tag: n.Tag, Proto: n.Proto,
				Err: err.Error()})
			port++
			continue
		}
		if why := panel.SelfTestSkipped(n); why != "" {
			results = append(results, panel.SelfTestResult{Tag: n.Tag, Proto: n.Proto,
				Skipped: true, Err: why})
			port++
			continue
		}
		if *showCfg {
			cfg, err := store.SelfTestConfig(n, u, st, port)
			if err != nil {
				return err
			}
			body, _ := panel.MarshalIndent(cfg)
			fmt.Printf("== %s（用用户 %s）==\n%s\n", n.Tag, u.Name, body)
			port++
			continue
		}
		res := runSelfTest(store, st, bin, n, u, port, *timeout)
		results = append(results, res)
		port++
	}

	if *showCfg {
		return nil
	}
	if *asJSON {
		b, _ := panel.MarshalIndent(results)
		fmt.Print(b)
	} else {
		fmt.Printf("本机实测 %d 个节点（用 %s 起客户端，经 socks 代理访问外网）\n\n", len(results), bin)
		fmt.Printf("  %-16s %-9s %-6s %-10s %-18s %s\n", "节点", "协议", "结果", "延迟", "出口 IP", "说明")
		for _, r := range results {
			mark := "✓ 通"
			switch {
			case r.Skipped:
				mark = "— 跳过"
			case !r.OK:
				mark = "✗ 不通"
			}
			fmt.Printf("  %-16s %-9s %-6s %-10s %-18s %s\n", r.Tag, r.Proto, mark,
				firstNonEmptyStr(r.Latency, "-"), firstNonEmptyStr(r.ExitIP, "-"), r.Err)
		}
	}
	bad, skipped, tested := 0, 0, 0
	for _, r := range results {
		if r.Skipped {
			skipped++
			continue
		}
		tested++
		if !r.OK {
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d/%d 个节点不通", bad, tested)
	}
	if skipped > 0 && !*asJSON {
		fmt.Printf("\n%d 个节点已跳过（见上表说明）。", skipped)
	}
	if !*asJSON {
		fmt.Println("\n全部节点可用。若你的客户端仍超时，问题在客户端或本地网络，不在服务端。")
	}
	return nil
}

// pickUserFor 挑一个能用这个节点的用户（默认第一个启用的）。
func pickUserFor(store *panel.Store, n panel.Node, name string) (panel.User, error) {
	if name != "" {
		u, err := store.UserByName(name)
		if err != nil {
			return panel.User{}, fmt.Errorf("用户不存在: %s", name)
		}
		return u, nil
	}
	users, err := store.Users()
	if err != nil {
		return panel.User{}, err
	}
	for _, u := range users {
		if u.Enable != 1 {
			continue
		}
		ns, err := store.NodesForUser(u, true)
		if err != nil {
			continue
		}
		for _, x := range ns {
			if x.Tag == n.Tag {
				return u, nil
			}
		}
	}
	return panel.User{}, fmt.Errorf("没有启用用户能访问这个节点")
}

// runSelfTest 起一个临时 xray 客户端，经 socks 代理实测这个节点。
func runSelfTest(store *panel.Store, st panel.Settings, bin string,
	n panel.Node, u panel.User, socksPort int, timeout time.Duration) panel.SelfTestResult {
	res := panel.SelfTestResult{Tag: n.Tag, Proto: n.Proto}
	cfg, err := store.SelfTestConfig(n, u, st, socksPort)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	body, err := panel.MarshalIndent(cfg)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	dir, err := os.MkdirTemp("", "polarisx-selftest-")
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "client.json")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		res.Err = err.Error()
		return res
	}
	if out, err := exec.Command(bin, "-test", "-config", cfgPath).CombinedOutput(); err != nil {
		res.Err = "客户端配置没通过 xray 校验: " + strings.TrimSpace(string(out))
		return res
	}
	cmd := exec.Command(bin, "run", "-config", cfgPath)
	if err := cmd.Start(); err != nil {
		res.Err = err.Error()
		return res
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	proxy := fmt.Sprintf("socks5h://127.0.0.1:%d", socksPort)
	// 等 socks 端口起来
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), 300*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	// 1) 204 探活（拿延迟）
	start := time.Now()
	code, err := curlThrough(proxy, "http://www.gstatic.com/generate_204", timeout)
	if err != nil || code != "204" {
		res.Err = fmt.Sprintf("204 探测失败（http=%s）: %v", code, err)
		return res
	}
	res.Latency = fmt.Sprintf("%dms", time.Since(start).Milliseconds())
	// 2) 出口 IP（确认真的从落地机出去）
	if ip, err := curlBody(proxy, "https://api.ipify.org", timeout); err == nil {
		res.ExitIP = strings.TrimSpace(ip)
	}
	res.OK = true
	return res
}

func curlThrough(proxy, url string, timeout time.Duration) (string, error) {
	out, err := runCurl(proxy, url, timeout, true)
	return out, err
}

func curlBody(proxy, url string, timeout time.Duration) (string, error) {
	return runCurl(proxy, url, timeout, false)
}

func runCurl(proxy, url string, timeout time.Duration, codeOnly bool) (string, error) {
	args := []string{"-s", "--max-time", fmt.Sprintf("%d", int(timeout.Seconds())),
		"-x", proxy}
	if codeOnly {
		args = append(args, "-o", os.DevNull, "-w", "%{http_code}")
	}
	args = append(args, url)
	out, err := exec.Command("curl", args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func cmdAlert(args []string) error {
	fs := flag.NewFlagSet("alert", flag.ExitOnError)
	test := fs.Bool("test", false, "发一条测试告警")
	_ = fs.Parse(args)
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	if strings.TrimSpace(st.AlertWebhook) == "" {
		fmt.Println("还没配告警地址。设一个：")
		fmt.Println("  polarisx set alert_webhook=https://hooks.example.com/xxx")
		fmt.Println("  然后：polarisx alert --test")
		fmt.Println("\n触发时机：采集时用户用量过 80% 预警线、或被自动停用（每次采集最多一条消息）。")
		return nil
	}
	if !*test {
		fmt.Printf("告警地址: %s\n触发时机: 用量过 80%% 或被停用（每次采集合并成一条）\n", st.AlertWebhook)
		fmt.Println("发测试消息：polarisx alert --test")
		return nil
	}
	evs := []panel.AlertEvent{{
		Kind: "warn", User: "（测试）", Message: "这是一条测试告警",
		At: time.Now().Format("2006-01-02 15:04:05"),
	}}
	if err := panel.SendAlert(st, evs, 10*time.Second); err != nil {
		return err
	}
	fmt.Printf("已发送到 %s\n", st.AlertWebhook)
	return nil
}

func cmdTraffic(args []string) error {
	fs := flag.NewFlagSet("traffic", flag.ExitOnError)
	hours := fs.Int("hours", 24, "看最近多少小时")
	asJSON := fs.Bool("json", false, "输出 JSON")
	_ = fs.Parse(args)
	store, err := panel.Open(panel.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	series, err := store.TrafficSeries(time.Now(), *hours)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := panel.MarshalIndent(series)
		fmt.Print(b)
		return nil
	}
	if len(series) == 0 {
		fmt.Println("还没有历史数据（采集每分钟写一笔）")
		return nil
	}
	vals := make([]int64, len(series))
	peak, peakAt := int64(0), 0
	for i, p := range series {
		vals[i] = p.Up + p.Down
		if vals[i] > peak {
			peak, peakAt = vals[i], i
		}
	}
	fmt.Printf("最近 %d 小时：%s\n\n", *hours, panel.TrafficSummary(series))
	fmt.Printf("  %s\n", panel.Sparkline(vals))
	fmt.Printf("  %s%s%s\n", strings.Repeat(" ", peakAt), "↑峰值", "")
	fmt.Printf("  %s … %s\n\n", series[0].Label, series[len(series)-1].Label)
	// 有流量的小时列出来（没流量的时段不必刷屏）
	fmt.Printf("  %-16s %10s %10s %10s\n", "时段", "上行", "下行", "合计")
	shown := 0
	for _, p := range series {
		if p.Up == 0 && p.Down == 0 {
			continue
		}
		fmt.Printf("  %-16s %10s %10s %10s\n", p.Label,
			panel.HumanBytes(p.Up), panel.HumanBytes(p.Down), panel.HumanBytes(p.Up+p.Down))
		shown++
	}
	if shown == 0 {
		fmt.Println("  （这段时间没有流量）")
	}
	return nil
}

func cmdStatus(args []string) error {
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	store, err := panel.Open(panel.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	nodes, _ := store.Nodes(false)
	enabled := 0
	for _, n := range nodes {
		if n.Enabled == 1 {
			enabled++
		}
	}
	users, _ := store.Users()
	disabled := 0
	for _, u := range users {
		if u.Enable != 1 {
			disabled++
		}
	}
	host := st.ServerHost
	if host == "" {
		host = localIP()
	}
	fmt.Printf("polarisx     : %s (%s)\n", panel.Version, panel.Home())
	fmt.Printf("xray 二进制  : %s\n", firstNonEmptyStr(panel.FindXray(st), "未找到"))
	cfgNote := ""
	if _, err := os.Stat(st.XrayConfig); err != nil {
		cfgNote = "（不存在）"
	}
	fmt.Printf("xray 配置    : %s %s\n", st.XrayConfig, cfgNote)
	api := "不通"
	if panel.APIAvailable(st, 5*time.Second) {
		api = "在线"
	}
	fmt.Printf("xray API     : %s %s\n", st.XrayAPI, api)
	fmt.Printf("节点         : %d 个启用 / %d 个总计\n", enabled, len(nodes))
	fmt.Printf("用户         : %d 个 (%d 停用)\n", len(users), disabled)
	fmt.Printf("订阅服务     : %s (对外: %s)\n", st.SubListen,
		firstNonEmptyStr(st.SubBase, "未设置 sub_base"))
	fmt.Printf("落地机地址   : %s\n", firstNonEmptyStr(host, "未设置"))
	fmt.Printf("事件         : %d 条（polarisx events 看最近几条）\n", store.EventCount())
	return nil
}

func cmdEvents(args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	limit := fs.Int("limit", 30, "显示最近 N 条")
	asJSON := fs.Bool("json", false, "输出 JSON")
	_ = fs.Parse(args)
	store, err := panel.Open(panel.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	evs, err := store.Events(*limit)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := panel.MarshalIndent(evs)
		fmt.Print(b)
		return nil
	}
	if len(evs) == 0 {
		fmt.Println("还没有事件")
		return nil
	}
	for _, e := range evs {
		who := ""
		if e.UserName != "" {
			who = " " + e.UserName
		}
		fmt.Printf("%s [%-5s]%s %s\n", e.TS, e.Level, who, e.Message)
	}
	return nil
}

func cmdExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	cfgPath := fs.String("config", "", "把生成的 xray 配置写到这个路径")
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
	if *cfgPath != "" {
		cfg, err := store.BuildConfig(st)
		if err != nil {
			return err
		}
		if err := panel.WriteConfig(st, cfg, *cfgPath, false); err != nil {
			return err
		}
		fmt.Printf("xray 配置已导出: %s\n", *cfgPath)
	}
	users, err := store.Users()
	if err != nil {
		return err
	}
	for _, u := range users {
		state := "启用"
		if u.Enable != 1 {
			state = "停用"
		}
		quota := "不限"
		if q := panel.QuotaBytes(u, st); q > 0 {
			quota = panel.HumanBytes(q)
		}
		fmt.Printf("[%s] %-12s 已用 %-10s / 额度 %-10s 到期 %-12s token=%s\n",
			state, u.Name, panel.HumanBytes(panel.UsedBytes(u)), quota,
			firstNonEmptyStr(u.ExpireAt, "不限"), u.Token)
	}
	return nil
}

// cmdUninstall 委托给 deploy/install.sh 的 uninstall，保证安装/卸载对称。
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	yes := fs.Bool("yes", false, "不交互确认")
	purge := fs.Bool("purge", false, "连数据一起删（panel.db/settings.json）")
	_ = fs.Parse(args)
	script := ""
	for _, p := range []string{
		filepath.Join(panel.Home(), "install.sh"),
		"/root/polarisx-deploy/install.sh",
		filepath.Join(panel.Home(), "..", "polarisx-deploy", "install.sh"),
	} {
		if _, err := os.Stat(p); err == nil {
			script = p
			break
		}
	}
	if script == "" {
		fmt.Println("找不到 install.sh，无法自动卸载。手动执行：")
		fmt.Println("  systemctl disable --now polarisx-collect.timer polarisx-sub.service polarisx-web.service")
		fmt.Println("  rm -f /etc/systemd/system/polarisx-*.service /etc/systemd/system/polarisx-*.timer")
		fmt.Println("  rm -f /usr/local/bin/polarisx")
		fmt.Printf("  rm -rf %s   # 只在确定不要数据时\n", panel.Home())
		return nil
	}
	cmdArgs := []string{script, "uninstall"}
	if *yes {
		cmdArgs = append(cmdArgs, "--yes")
	}
	if *purge {
		cmdArgs = append(cmdArgs, "--purge")
	}
	c := exec.Command("bash", cmdArgs...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// localIP 猜本机出口 IP（没配 server_host 时给个提示值）。
func localIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

func cmdTunnel(args []string) error {
	fs := flag.NewFlagSet("tunnel", flag.ExitOnError)
	download := fs.Bool("download", false, "找不到 cloudflared 时从 GitHub 下载")
	wait := fs.Int("wait", 60, "等隧道域名的最长秒数")
	quiet := fs.Bool("quiet", false, "只输出错误（给定时器用）")
	// Go 的 flag 遇到第一个位置参数就停止解析，所以 `tunnel on --download` 会丢掉
	// --download。先把动作词摘出来，再用剩下的参数解析（同 user/node 的处理）。
	action, rest := splitNameFirst(args)
	if action == "" {
		action = "status"
	}
	_ = fs.Parse(rest)
	st, err := panel.LoadSettings()
	if err != nil {
		return err
	}
	store, err := panel.Open(panel.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	say := func(f string, a ...any) {
		if !*quiet {
			fmt.Printf(f, a...)
		}
	}
	units := []string{"polarisx-tunnel.service", "polarisx-tunnel-refresh.timer"}
	sd := func(args ...string) string {
		out, _ := exec.Command("systemctl", args...).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	hasSystemd := exec.Command("systemctl", "is-system-running").Run() == nil ||
		func() bool { _, err := os.Stat("/run/systemd/system"); return err == nil }()

	switch action {
	case "status":
		cf := panel.FindCloudflared(st)
		live := panel.QuickTunnelHost(st, 4*time.Second)
		say("免域名订阅隧道: %s\n", map[bool]string{true: "开启", false: "关闭"}[st.SubTunnel])
		say("  cloudflared   : %s\n", firstNonEmptyStr(cf, "找不到（用 `polarisx tunnel on --download`）"))
		say("  隧道指向      : %s\n", firstNonEmptyStr(st.SubTunnelTarget, panel.DefaultTunnelTarget))
		say("  metrics       : %s\n", firstNonEmptyStr(st.SubTunnelMetrics, panel.DefaultTunnelMetrics))
		say("  当前隧道域名  : %s\n", firstNonEmptyStr(live, "取不到（服务没跑？）"))
		say("  订阅里写的域名: %s\n", firstNonEmptyStr(st.SubTunnelHost, "(空)"))
		say("  订阅地址前缀  : %s\n", panel.SubBaseFor(st))
		if st.SubTunnel {
			if live != "" && st.SubTunnelHost != "" && live != st.SubTunnelHost {
				say("  !! 域名对不上：跑 `polarisx tunnel refresh` 更新订阅地址\n")
			} else if live != "" && st.SubTunnelHost == "" {
				say("  !! 订阅地址还没切到隧道：跑 `polarisx tunnel refresh`\n")
			}
		}
		if hasSystemd {
			for _, u := range units {
				say("  %-32s %s\n", u, firstNonEmptyStr(sd("is-active", u), "unknown"))
			}
		}
		return nil

	case "on":
		cf := panel.FindCloudflared(st)
		if cf == "" && *download {
			say("  下载 cloudflared…\n")
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			cf, err = panel.DownloadCloudflared(ctx)
			cancel()
			if err != nil {
				return err
			}
			say("  已装到 %s\n", cf)
		}
		if cf == "" {
			return fmt.Errorf("没找到 cloudflared。三选一：\n" +
				"  1) polarisx tunnel on --download   # 从 GitHub 下载（带镜像回退）\n" +
				"  2) apt-get install -y cloudflared  # Debian/Ubuntu\n" +
				"  3) polarisx set cloudflared_bin=/path/to/cloudflared")
		}
		st.CloudflaredBin = cf
		if st.SubTunnelPrevBase == "" {
			st.SubTunnelPrevBase = st.SubBase
		}
		st.SubTunnel = true
		if err := panel.SaveSettings(st); err != nil {
			return err
		}
		if hasSystemd {
			for name, body := range panel.TunnelUnits(st) {
				if err := os.WriteFile("/etc/systemd/system/"+name, []byte(body), 0o644); err != nil {
					return err
				}
			}
			sd("daemon-reload")
			say("  systemd: %s\n", firstNonEmptyStr(sd("enable", "--now",
				"polarisx-tunnel.service", "polarisx-tunnel-refresh.timer"), "已启用"))
		} else {
			say("  本机没有 systemd —— 自己常驻这条命令：\n")
			say("    %s tunnel --no-autoupdate --edge-ip-version auto --metrics %s --url %s\n",
				cf, firstNonEmptyStr(st.SubTunnelMetrics, panel.DefaultTunnelMetrics),
				firstNonEmptyStr(st.SubTunnelTarget, panel.DefaultTunnelTarget))
		}
		host := panel.WaitQuickTunnel(st, *wait, func(m string) { say("%s\n", m) })
		if host == "" {
			return fmt.Errorf("没等到隧道域名（cloudflared 没起来 / metrics 端口不对）。" +
				"起来后再跑 `polarisx tunnel refresh`")
		}
		if _, err := panel.ApplyTunnelBase(&st, host); err != nil {
			return err
		}
		_ = store.Event("开启免域名订阅隧道: "+panel.TunnelBase(host), "info", "")
		say("  订阅地址已切到: %s\n", panel.SubBaseFor(st))
		say("  临时隧道域名每次重启 cloudflared 都会变，refresh 定时器每 2 分钟自动跟上\n")
		return nil

	case "off":
		if hasSystemd {
			say("  systemd: %s\n", firstNonEmptyStr(sd("disable", "--now",
				"polarisx-tunnel.service", "polarisx-tunnel-refresh.timer"), "已停用"))
		}
		prev := strings.TrimSpace(st.SubTunnelPrevBase)
		st.SubTunnel = false
		st.SubTunnelHost = ""
		st.SubTunnelPrevBase = ""
		st.SubBase = prev
		if err := panel.SaveSettings(st); err != nil {
			return err
		}
		_ = store.Event("关闭免域名订阅隧道，订阅地址还原为 "+firstNonEmptyStr(prev, "(本机监听地址)"), "info", "")
		say("  隧道已关，订阅地址还原为: %s\n", panel.SubBaseFor(st))
		return nil

	case "refresh":
		host := panel.QuickTunnelHost(st, 4*time.Second)
		if host == "" {
			return fmt.Errorf("取不到隧道域名（cloudflared 没跑？）")
		}
		changed, err := panel.ApplyTunnelBase(&st, host)
		if err != nil {
			return err
		}
		if changed {
			_ = store.Event("隧道域名刷新: "+panel.TunnelBase(host), "info", "")
			say("  订阅地址已更新: %s\n", panel.SubBaseFor(st))
		} else {
			say("  隧道域名没变: %s\n", host)
		}
		return nil
	}
	return fmt.Errorf("未知动作: %s（status/on/off/refresh）", action)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
	// 默认跟 fail2ban_kick 设置走（Python 版语义），--no-kick 可强制关掉
	rep, err := store.Collect(st, time.Now(), !*noKick && st.Fail2banKick)
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
		fmt.Printf("[!!] 数据库不存在: %s（先运行 polarisx node add 或通过 TUI 创建首个节点）\n", panel.DBPath())
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
