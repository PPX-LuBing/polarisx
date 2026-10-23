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
| ③b | 订阅 HTTP 服务（路径 / header / 停用提示） | ⏳ 依赖的额度统计已就绪 |
| ④ | 流量统计 + 额度 + 每月重置 | ✅ 8 项 parity 全过（含状态型：重置/采集后的用户表逐字段一致） |
| ⑤ | bubbletea TUI | ⏳ |
| ⑥ | 内嵌 Web UI + API | ⏳ |
| ⑦ | 部署脚本 / systemd / 迁移切换 | ⏳ |

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

1. Go 版跑通全部 parity（②③④ 阶段完成）；
2. 在生产机上并行部署（不同端口），比对订阅输出与流量统计；
3. 切换 systemd 单元，保留 Python 版二进制与数据库备份作为回滚点。

MIT License.
