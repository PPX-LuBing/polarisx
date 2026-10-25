# PolarisX（北极星）

当前版本：`v2.0.12`

Xray 多协议落地机面板。**单静态二进制**，无运行时依赖。

本项目是 PolarisX 的独立 Go 实现：单静态二进制、无运行时依赖，面向长期维护和明确的服务边界。

- 数据只由 PolarisX 管理，不依赖旧面板或旧环境变量。
- 协议输出由固定夹具和语义测试保护，覆盖多协议、多传输和 WARP/链式转发。
- 生产数据目录默认是 `/var/lib/polarisx`，程序安装到 `/usr/local/bin/polarisx`。

## 当前进度

| 阶段 | 内容 | 状态 |
|---|---|---|
| ① | 项目骨架 / settings / SQLite 存储 | ✅ |
| ② | Xray 配置生成（全协议）+ golden 回归基线 | ✅ 6 个变体全过 |
| ③ | 订阅生成：分享链接 / Clash / sing-box | ✅ 链接 22 条 + Clash 18 份 + sing-box 6 份 golden 基线通过 |
| ③b | 订阅 HTTP 服务（路径 / header / 停用提示 / HEAD） | ✅ 20 个请求矩阵 golden 基线通过 |
| ④ | 流量统计 + 额度 + 每月重置 | ✅ 8 项 golden 基线通过（含状态型：重置/采集后的用户表逐字段一致） |
| ⑤ | bubbletea TUI（主菜单/节点/用户/自检/应用） | ✅ 模型级测试 4 项 |
| ⑥ | 内嵌 Web UI + 只读 API（含 /sub/ 复用） | ✅ 测试 6 项；已截图核对深/浅色与移动端 |
| ⑦ | 部署脚本 / systemd / 独立数据目录 | ✅ |

## 构建与测试

```bash
make build          # 产出 bin/polarisx
make test           # 单元 + golden 回归基线（与时区无关）
POLARISX_HOME=/var/lib/polarisx ./bin/polarisx doctor
POLARISX_HOME=/var/lib/polarisx ./bin/polarisx show      # 打印将要写入的 xray 配置
```

数据目录用 `POLARISX_HOME`，默认 `/var/lib/polarisx`。

### 校准倍率：`polarisx traffic --verify`

倍率不用拍脑袋，直接测：

```bash
polarisx traffic --verify 120     # 采样 120 秒，对比网卡与 xray 用户级流量
```

它只读（不触发采集、不重置计数器），会打印「网卡流量 / 用户级流量」的实测倍数，
并给出建议的 `traffic_rate`。注意两点：**远程客户端**的流量才会两段都过网卡
（本机回环测不准）；网卡计数重启归零，所以比的是**速率**不是累计值。

### 计费倍率（让额度口径对齐提供商账单）

面板记的是 **xray 的用户级流量**（每个字节算一次）✓；而 VPS 提供商按**网卡层**计费——
代理服务器上同一份载荷会**进出各算一次**，所以账单约为面板数字的 **2 倍**
（走中转的流量在两台机器上各算一次，合计约 4 倍）。

```bash
polarisx set traffic_rate=2      # 默认 1；节点上报的 up/down 乘以它再入账
polarisx doctor                  # 会显示当前倍率
```

倍率在采集的**源头**乘一次，用户用量、历史图表、采集报告随之全部一致。
做法与 Xboard/V2board 相同（它们的 `Server.rate` 在 `TrafficFetchJob` 里生效，
同样只是"节点上报的用户级流量 × 倍率"，并不读网卡）。

> 倍率**只对之后采集到的流量生效**（和 Xboard 一样），历史用量保持原样。
> 想让历史用量也按新口径重算，需手工改一次数据库。

### 订阅的 HTTPS（内置，不需要 nginx/certbot）

`polarisx sub` 自己就能跑 TLS，证书自动签发、自动续期：

```bash
polarisx set sub_tls=auto                    # off（默认，明文）/ auto（ACME）/ files（自带证书）
polarisx set sub_base=https://sub.example.com
polarisx set sub_tls_listen=:443             # 443 被 xray 占用时改 :8443
polarisx set sub_http_listen=:80             # ACME 的 HTTP-01 挑战固定走 80
systemctl restart polarisx-sub
```

- **auto**：用 ACME（Let's Encrypt）自动申请与续期，证书缓存在 `$POLARISX_HOME/acme`；
  同时接管明文端口，`/.well-known/acme-challenge/` 交给 ACME，其余请求 301 跳 HTTPS
- **files**：`sub_tls_cert` / `sub_tls_key` 指向自己的证书
- **off**（默认）：只跑明文，留给"前面已有反代"的部署
- `polarisx doctor` 会显示当前模式；auto 模式下还会显示证书到期日

> 面板只监听 `sub_listen`（建议 `127.0.0.1:8080`），公网入口只有 TLS 那一个端口——
> 订阅正文里是全套节点凭据，明文暴露等于把节点送给链路中间人。

### 全新机器上装 xray

面板不代装 xray，用官方脚本装：

```bash
bash -c "$(curl -L https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
```

**注意权限**：官方脚本建的单元是 `User=xray`，而面板把配置写成 `0600 root`，
xray 会起不来（日志里只有 `permission denied`）。两种修法任选：

```bash
# 甲：让 xray 以 root 运行（与既有机器一致）
sed -i 's/^User=xray/User=root/; s/^Group=xray/Group=root/' /etc/systemd/system/xray.service
systemctl daemon-reload && systemctl restart xray

# 乙：保持最小权限，把配置交给 xray 用户（注意每次 apply 后属主会被重写，需配合 sudo 规则或改用甲）
```

`polarisx doctor` 会直接点出这个问题（包括当前权限与属主）。

> **安装脚本要和 `deploy/` 里的 systemd 单元文件同目录**：单元缺失时它会先尝试从同一个
> Release 取，取不到就**直接报错退出**（不会静默跳过——那样 `--start` 会因为单元不存在而
> 失败，报错却离真正的原因很远）。所以全新机器上推荐整目录使用：
> `bash deploy/install.sh --from-release v2.0.2 --start`。

golden 基线位于 `testdata/golden/`，输入夹具位于 `testdata/fixture*/` 并冻结。实现有意改变时，先运行 `UPDATE_GOLDEN=1 go test ./internal/panel ./internal/sub` 更新基线，再检查并提交生成的 diff；不需要改变行为时不要更新。该命令可重复运行且幂等。

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
polarisx selftest --node polarisx-reality
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
polarisx sni --apply --node polarisx-reality # 把最佳结果写到节点（reality 会连 dest 一起换）
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
- 其余全部标准库：`flag` 做 CLI、`net/http` 做订阅/API、`log/slog` 做日志、手写 Clash YAML（由 golden 回归基线锁定输出）。
- TUI 用 `bubbletea` + `lipgloss`（第 ⑤ 阶段引入，仅此两个依赖）。
- 订阅服务的 HTTP 契约稳定：路径、内容格式和状态码由测试覆盖。

## 部署

```bash
# 交叉编译静态二进制
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o polarisx-linux-amd64 .

# 安装二进制和 systemd 单元（默认不启动）
cd deploy && BIN_SRC=../polarisx-linux-amd64 bash install.sh

# 确认数据目录和配置后启动服务
bash install.sh --start
```

服务使用 `/var/lib/polarisx` 保存 `panel.db`、`settings.json` 和日志；卸载默认保留数据，只有显式使用 `--purge` 才会删除。

MIT License.
