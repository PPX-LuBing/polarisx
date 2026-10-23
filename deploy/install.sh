#!/usr/bin/env bash
# PolarisX 部署/接管脚本（北极星）
#
#   bash install.sh            安装二进制 + systemd 单元，并做一次兼容性检查
#   bash install.sh --takeover 从 Python 版 ixpanel 接管（停旧单元、起新单元）
#   bash install.sh uninstall [--yes] [--purge]
#                              卸载（默认保留 panel.db/settings.json，--purge 连数据一起删）
#
# 这个脚本只装二进制与 systemd 单元。装机（xray/证书/订阅前置/加固/建节点）是
# deploy/provision.sh 的事：
#   bash provision.sh --yes                          # xray + 前置 + 加固
#   bash provision.sh --node reality --user alice --yes
#   bash provision.sh --sub-mode tls --sub-path hash --yes
#
# 与 Python 版共用同一份 /usr/local/ixpanel/{settings.json,panel.db}，不迁移数据。
set -euo pipefail

BIN_SRC="${BIN_SRC:-./polarisx-linux-amd64}"
BIN_DST="/usr/local/bin/polarisx"
HOME_DIR="${POLARISX_HOME:-/usr/local/ixpanel}"
UNIT_DIR="/etc/systemd/system"
UNITS=(polarisx-sub.service polarisx-web.service polarisx-collect.service polarisx-collect.timer)
OLD_UNITS=(ixpanel-sub.service ixpanel-collect.timer)

[ "$(id -u)" = "0" ] || { echo "请用 root 跑"; exit 1; }

ACTION="${1:-install}"
YES=0; PURGE=0
for a in "$@"; do
  case "$a" in --yes) YES=1 ;; --purge) PURGE=1 ;; esac
done

if [ "$ACTION" = "uninstall" ]; then
  echo "==> 卸载 PolarisX"
  if [ "$YES" != "1" ]; then
    printf "确认卸载？(y/N) "; read -r ans </dev/tty || ans=n
    case "$ans" in y|Y) ;; *) echo "已取消"; exit 0 ;; esac
  fi
  echo "  -- 停用并删除单元"
  for u in "${UNITS[@]}" polarisx-tunnel.service polarisx-tunnel-refresh.service \
           polarisx-tunnel-refresh.timer sub-http.service sub-proxy.service; do
    systemctl disable --now "$u" >/dev/null 2>&1 || true
    rm -f "$UNIT_DIR/$u"
  done
  systemctl daemon-reload
  echo "  -- 删除二进制"
  rm -f "$BIN_DST"
  if [ "$PURGE" = "1" ]; then
    echo "  -- 删除数据目录 $HOME_DIR（--purge）"
    rm -rf "$HOME_DIR"
  else
    echo "  -- 保留数据目录 $HOME_DIR（panel.db / settings.json）"
  fi
  echo "  注意：xray 本体、/etc/nginx-sub、/etc/ix-sub-tls、iptables 规则都**没动**"
  echo "        （它们不属于面板；要一起清就手工删，或看 deploy/provision.sh 的反向步骤）"
  echo "==> 卸载完成"
  exit 0
fi

echo "==> 安装二进制到 $BIN_DST"
install -m 0755 "$BIN_SRC" "$BIN_DST"
mkdir -p "$HOME_DIR"

echo "==> 安装 systemd 单元"
for u in "${UNITS[@]}"; do
  install -m 0644 "$(dirname "$0")/$u" "$UNIT_DIR/$u"
done
systemctl daemon-reload

echo "==> 兼容性检查（只读）"
POLARISX_HOME="$HOME_DIR" "$BIN_DST" migrate || {
  echo "有阻塞项，未做接管。修好后重跑。"; exit 1; }

if [ "${1:-}" = "--takeover" ]; then
  echo "==> 停掉 Python 版的单元（保留二进制与数据，便于回滚）"
  for u in "${OLD_UNITS[@]}"; do
    systemctl disable --now "$u" 2>/dev/null || true
  done
  echo "==> 起 PolarisX"
  systemctl enable --now polarisx-sub.service polarisx-collect.timer
  # Web 面板默认只绑本机，按需自行放开
  systemctl enable --now polarisx-web.service || true
  echo "==> 完成。回滚：systemctl disable --now ${UNITS[*]}; systemctl enable --now ${OLD_UNITS[*]}"
else
  echo "==> 只装了二进制与单元（没动运行中的服务）。要接管加 --takeover"
fi
