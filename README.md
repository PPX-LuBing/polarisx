# PolarisX（北极星）

Xray 多协议落地机面板。**单静态二进制**，无运行时依赖。

本项目是 [ixrelay/ixpanel](../ixrelay)（Python 单文件版）的 Go 重写：目标是把一个
"能用的脚本"变成"能长期维护的工程"——更成熟、更安全、性能更好、更好用。

## 与 Python 版的关系（重要）

- **共用同一份 `settings.json` 和 `panel.db`**：schema 与键名逐一对齐，换二进制即可接管，无需迁移数据。
- **行为一致由 parity 测试保证**：`tools/mkfixture.py` 用 Python 版生成期望配置，Go 版必须生成语义相同的
  `xray config.json`（覆盖 vless/vmess/trojan/shadowsocks/hysteria2 × reality/tls/none × tcp/ws/httpupgrade/xhttp/grpc，
  以及 WARP 两种出站、链式转发、direct_ports、出口 IP 族）。
- Python 版在被完全替代前仍是**参考实现**，两边行为不一致时以 parity 测试为准。

## 当前进度

| 阶段 | 内容 | 状态 |
|---|---|---|
| ① | 项目骨架 / settings / SQLite 存储 | ✅ |
| ② | Xray 配置生成（全协议）+ parity | ✅ 6 个变体全过 |
| ③ | 订阅生成：分享链接 / Clash / sing-box | ✅ 链接 22 条 + Clash 18 份 + sing-box 6 份 parity 全过 |
| ③b | 订阅 HTTP 服务（路径 / header / 停用提示 / HEAD） | ✅ 20 个请求矩阵 parity 全过 |
| ④ | 流量统计 + 额度 + 每月重置 | ✅ 8 项 parity 全过（含状态型：重置/采集后的用户表逐字段一致） |
| ⑤ | bubbletea TUI（主菜单/节点/用户/自检/应用） | ✅ 模型级测试 4 项 |
| ⑥ | 内嵌 Web UI + 只读 API（含 /sub/ 复用） | ✅ 测试 6 项；已截图核对深/浅色与移动端 |
| ⑦ | 部署脚本 / systemd / 迁移检查 | ✅ 已在生产数据上只读演练（见下） |

## 构建与测试

```bash
make build          # 产出 bin/polarisx
make test           # 单元 + parity（固定 TZ=UTC，与夹具生成时一致）
make parity         # 重新生成夹具并跑 parity（需要 Python 版源码）
POLARISX_HOME=/usr/local/ixpanel ./bin/polarisx doctor
POLARISX_HOME=/usr/local/ixpanel ./bin/polarisx show      # 打印将要写入的 xray 配置
```

数据目录用 `POLARISX_HOME`（兼容旧的 `IXPANEL_HOME`），默认 `/usr/local/ixpanel`。

## 用量告警（webhook）

```bash
polarisx set alert_webhook=https://hooks.example.com/xxx
polarisx alert --test        # 发一条测试消息
polarisx alert               # 看当前配置与触发时机
```

触发时机：采集时用户用量过 **80%** 预警线、或被**自动停用**。一次采集里的所有事件**合并成
一条消息**（否则一分钟一条会刷屏）。

消息体带 `text` 字段，Slack / Discord / 飞书这类"贴个 JSON 就发消息"的 webhook 直接能用；
同时带结构化的 `events`（kind/user/message/used_bytes/quota_bytes/at），自建端点可以按字段处理。
发送失败**不影响采集**（配置已经改好了，只是消息没送出去），会在 `polarisx events` 里留一条记录。

## 流量历史与曲线

采集（每分钟一次）会顺手把增量写进 `traffic_history`，保留 30 天，过期的每次采集时裁掉。
`raw_stats` 只存"最近一次"，没有历史——曲线靠这张表。

```bash
polarisx traffic --hours 24        # 终端迷你图 + 逐小时明细
polarisx traffic --json
```

Web 面板顶部有「流量曲线」：24 根小时柱，**琥珀色只给峰值与当前这一小时**（这个面板里琥珀
一直是"信号"的意思，铺满就失去意义了）。手写 SVG，不引图表库。接口是 `GET /api/traffic?hours=N`。

增量为 0 的时段不写库（省空间），图表里没数据的桶补 0，所以曲线是连续的。

## 节点自检（selftest）

```bash
polarisx selftest                  # 逐节点实测
polarisx selftest --node ix-reality
polarisx selftest --show-config    # 只打印将要用的客户端配置（排障用）
polarisx selftest --json
```

做法是**在本机起一个 xray 客户端**（socks 入站 + 被测节点的出站），经它访问
`generate_204` 拿延迟、访问 `api.ipify.org` 确认出口 IP 确实是这台落地机。

存在的意义：客户端报超时时，用它一条命令分清**服务端坏了**还是**你的网络/客户端问题**。
字段细节也在这里被钉住（比如自签证书必须 `pinnedPeerCertSha256` —— Xray 26 已移除
`allowInsecure`；SS2022 的 method 不能去掉 `2022-` 前缀）。

**hysteria2 的 auth 必须放在传输层**：Xray 出站的 `settings` 里**没有 auth 字段**（只有
server/address/port），hy2 的 auth 属于 `streamSettings.hysteriaSettings.auth`
（见 Xray 源码 `transport/internet/hysteria/config.proto` 的 `Config.auth`）。
放错地方的话客户端会发**空 auth**，服务端回 `auth failed code 404`——而且报错完全不提
"字段放错了"，很容易误判成"Xray 客户端不支持"。

## TUI 写操作

`polarisx ui` 里现在不只是看，还能改：

| 页面 | 按键 |
|---|---|
| 节点管理 | `a` 新增（协议/传输/安全是枚举，左右键选）· `e` 改端口/入口/SNI/启停 · `d` 删除（要确认）|
| 用户管理 | `Enter` 详情与二维码 · `a` 新增（额度/到期/绑哪些节点）· `e` 改额度/到期/启停/备注 · `d` 删除（要确认）|
| 用户详情 | 订阅地址 + **终端二维码**（`c` 在 clash / sing-box / base64 之间切）——手机直接扫 |
| 事件与告警 | 最近 20 条事件 · `w` 设置告警 webhook · `t` 发测试消息 |

表单字段刻意只收**必须手填**的部分：reality 的密钥对与 shortId 自动生成；用户的节点留空
= 绑全部节点（与 CLI 语义一致）；SNI 留空 = 本机实测挑选。删除一律要 `y` 确认。
改完记得回主菜单「生成配置并重启 xray」。

## 终端二维码

```bash
polarisx qr alice              # 每个节点一张链接码（手机扫码导入单节点）
polarisx qr alice --clash      # 订阅地址的码，带 /clash 后缀
polarisx qr alice --singbox    # 带 /singbox 后缀
polarisx qr alice --sub        # 裸订阅地址（base64，老客户端）
```

**格式后缀必须写进二维码本身**：裸订阅地址返回的是 base64，mihomo/Clash 系客户端
（如 BetterBox）会把它当 YAML 解析并直接报 `cannot unmarshal !!str ... into config.RawConfig`，
而扫码之后没法再手动补后缀。

实现用 `github.com/skip2/go-qrcode`（纯 Go，会按标准惩罚分挑掩码）。曾经试过 `rsc.io/qr`，
它源码里写着 `// TODO: Pick appropriate mask.`、掩码恒为 0 —— 掩码的作用正是避开会让扫码器
困惑的图案，而这个功能的核心就是拿手机扫，所以换掉了。渲染成半块字符（▀▄█），静默区用库自带的
4 模块（不再叠一层），输出宽度比一张图小得多。

## 免域名订阅（cloudflared 临时隧道）

没有域名、也不想给落地机配证书时，用 Cloudflare 的临时隧道把订阅暴露成一个 HTTPS 地址：

```bash
polarisx tunnel on --download   # 下载 cloudflared、起隧道、把 sub_base 切成隧道域名
polarisx tunnel status          # 看当前域名 / 服务状态 / 是否对得上
polarisx tunnel refresh         # 手动把最新域名写回 sub_base（定时器每 2 分钟自动做）
polarisx tunnel off             # 关隧道，订阅地址还原成开隧道之前的值
```

行为要点：

1. 域名从 cloudflared 的 metrics 端口 `GET /quicktunnel` 取（**不解析日志**），只接受
   `*.trycloudflare.com`；metrics 地址是设置项 `sub_tunnel_metrics`（默认 `127.0.0.1:19090`）。
2. **临时隧道域名每次重启 cloudflared 都会变**，所以 `polarisx-tunnel-refresh.timer` 每 2 分钟
   跑一次 `polarisx tunnel refresh` 把新域名写回 `sub_base`（域名没变则不写盘）。
3. `polarisx-tunnel.service` 的 `--metrics` / `--url` 用**当前设置渲染**，不会出现
   「单元里写死的参数和设置不一致」这种坑。
4. `tunnel on` 会把开隧道前的 `sub_base` 记在 `sub_tunnel_prev_base`，`off` 时还原。

## SNI 自动挑选（REALITY 伪装目标）

不同 VPS 到各站点的 RTT 差别很大，**不该硬编码一个 SNI**。`polarisx sni` 在落地机本机上实测候选：

```bash
polarisx sni --top 10                      # 排名（TLS1.3 + h2 + X25519、排除 Cloudflare 前置）
polarisx sni --apply --node ix-reality     # 把最佳结果写到节点（reality 会连 dest 一起换）
polarisx node add --tag n1 --proto vless --net tcp --security reality \
    --port 443 --entry-host 1.2.3.4 --sni auto   # 建节点时自动挑
```

判据（全部用标准库实现，不依赖 openssl）：

1. 解析出 IP，若落在 **Cloudflare 网段**（从官方 API 取，取不到用内置常用段）则跳过 —— REALITY 不适合拿 CDN 前置的域名当伪装目标；
2. 3 次 TLS 握手取中位数，分别记录 TCP 与 TLS 耗时；
3. 必须 **TLS1.3 + ALPN h2 + X25519 系密钥交换**。注意现在多数服务器默认协商 `X25519MLKEM768`（后量子混合组），只判 `== X25519` 会把绝大多数合格目标误杀（实测 71 个只剩 1 个）；
4. 候选池刻意**避开** apple / microsoft / google / amazon / cloudflare 这类被用烂的域名 —— 用烂的 SNI 本身就是特征。

> 换 SNI 会改变客户端订阅里的 `servername`，客户端**重新拉一次订阅**才生效，所以别在用户活跃时随手换。

## 设计取舍

- 依赖只有一个：`modernc.org/sqlite`（纯 Go、无 cgo，才能编出单静态二进制）。
- 其余全部标准库：`flag` 做 CLI、`net/http` 做订阅/API、`log/slog` 做日志、手写 Clash YAML（与 Python 版字节对齐）。
- TUI 用 `bubbletea` + `lipgloss`（第 ⑤ 阶段引入，仅此两个依赖）。
- 订阅服务的兼容性优先：路径、header、内容格式与 Python 版保持一致，客户端无感。

## 迁移路径

```bash
# 1. 交叉编译静态二进制（无 cgo）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o polarisx-linux-amd64 .

# 2. 装上二进制与 systemd 单元，并做只读兼容性检查（不动运行中的服务）
scp polarisx-linux-amd64 root@机器:/tmp/polarisx
POLARISX_HOME=/usr/local/ixpanel /tmp/polarisx migrate

# 3. 接管（停 Python 版单元、起 PolarisX）
cd deploy && bash install.sh --takeover

# 4. 回滚（数据没被改过，把旧单元起回来即可）
systemctl disable --now polarisx-sub polarisx-web polarisx-collect.timer
systemctl enable --now ixpanel-sub ixpanel-collect.timer
```

`migrate` 是只读的：它只报告节点/用户数、库结构、xray 与订阅地址是否就绪，不会写任何东西。

### 已经在生产机上只读演练过（2026-10-23）

拿一台生产落地机的**真实数据副本**跑了一遍（`sqlite3 .backup` 复制，不碰运行中的服务）：

| 检查项 | 结果 |
|---|---|
| `migrate` 兼容性 | 节点 2 / 用户 1 / 库结构完整 / xray 与订阅地址就绪，无阻塞项 |
| xray 配置生成 | 与磁盘上 Python 写的那份**语义完全一致**（2 个 inbound） |
| Clash 订阅 | **逐字节一致**（14254 字节） |
| sing-box 订阅 | **逐字节一致**（3371 字节） |
| 分享链接（plain） | **逐字节一致**（574 字节） |
| `/info` | 仅差实时流量漂移（副本取于 14:10:44、线上读于 14:12:30，中间采集进 2572 字节 ↓） |

演练中发现并修掉一个真实阻塞项：Python 版 `ixpanel set` 会把 `quota_reset_day` 这类整数存成**字符串**
（`"1"`），Go 侧严格解析会直接起不来 —— 现在 `Settings.UnmarshalJSON` 会按结构体字段类型做容错
（信任边界上的必要兼容，写回时仍是规范类型，Python 版照样能读）。

MIT License.
