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
make test           # 单元 + parity
make parity         # 重新生成夹具并跑 parity（需要 Python 版源码）
POLARISX_HOME=/usr/local/ixpanel ./bin/polarisx doctor
POLARISX_HOME=/usr/local/ixpanel ./bin/polarisx show      # 打印将要写入的 xray 配置
```

数据目录用 `POLARISX_HOME`（兼容旧的 `IXPANEL_HOME`），默认 `/usr/local/ixpanel`。

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

拿 一台生产落地机 的**真实数据副本**跑了一遍（`sqlite3 .backup` 复制，不碰运行中的服务）：

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
