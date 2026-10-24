#!/usr/bin/env bash
# PolarisX 部署脚本（北极星）
#
#   bash install.sh            安装二进制 + systemd 单元（不启动服务）
#   bash install.sh --start    安装并启动 PolarisX 服务
#   bash install.sh uninstall [--yes] [--purge]
#                              卸载（默认保留 panel.db/settings.json，--purge 连数据一起删）
#   bash install.sh --from-release v2.0.0 [--start]
#                              从 GitHub Release 下载对应架构的静态二进制
#
# 这个脚本只装二进制与 systemd 单元。装机（xray/证书/订阅前置/加固/建节点）是
# deploy/provision.sh 的事：
#   bash provision.sh --yes                          # xray + 前置 + 加固
#   bash provision.sh --node reality --user alice --yes
#   bash provision.sh --sub-mode tls --sub-path hash --yes
#
set -euo pipefail

BIN_SRC="${BIN_SRC:-./polarisx-linux-amd64}"
BIN_DST="/usr/local/bin/polarisx"
HOME_DIR="${POLARISX_HOME:-/var/lib/polarisx}"
UNIT_DIR="/etc/systemd/system"
UNITS=(polarisx-sub.service polarisx-web.service polarisx-collect.service polarisx-collect.timer)
RELEASE_REPO="${POLARISX_RELEASE_REPO:-PPX-LuBing/polarisx}"
RELEASE_BASE_URL="${POLARISX_RELEASE_BASE_URL:-https://github.com/$RELEASE_REPO/releases/download}"

[ "$(id -u)" = "0" ] || { echo "请用 root 跑"; exit 1; }

ACTION="install"
YES=0; PURGE=0
START=0
FROM_RELEASE=""
while [ $# -gt 0 ]; do
  case "$1" in
    uninstall) ACTION=uninstall; shift ;;
    --start) START=1; shift ;;
    --from-release)
      [ $# -ge 2 ] || { echo "--from-release 需要 tag（例如 v2.0.0）" >&2; exit 1; }
      FROM_RELEASE="$2"; shift 2 ;;
    --yes) YES=1; shift ;;
    --purge) PURGE=1; shift ;;
    install) shift ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
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
  echo "  注意：xray 本体、/etc/nginx-sub、/etc/polarisx-sub-tls、iptables 规则都**没动**"
  echo "        （它们不属于面板；要一起清就手工删，或看 deploy/provision.sh 的反向步骤）"
  echo "==> 卸载完成"
  exit 0
fi

if [ -n "$FROM_RELEASE" ]; then
  case "$FROM_RELEASE" in
    v*) ;;
    *) FROM_RELEASE="v$FROM_RELEASE" ;;
  esac
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64) RELEASE_ASSET="polarisx-linux-amd64" ;;
    Linux:aarch64|Linux:arm64) RELEASE_ASSET="polarisx-linux-arm64" ;;
    Darwin:x86_64|Darwin:amd64) RELEASE_ASSET="polarisx-darwin-amd64" ;;
    Darwin:arm64) RELEASE_ASSET="polarisx-darwin-arm64" ;;
    MINGW*:*|MSYS*:*|CYGWIN*:x86_64) RELEASE_ASSET="polarisx-windows-amd64.exe" ;;
    *) echo "不支持从 Release 安装的系统/架构: $(uname -s)/$(uname -m)" >&2; exit 1 ;;
  esac
  tmp_dir="$(mktemp -d)"
  trap 'rm -rf "$tmp_dir"' EXIT
  release_url="$RELEASE_BASE_URL/$FROM_RELEASE/$RELEASE_ASSET"
  sums_url="$RELEASE_BASE_URL/$FROM_RELEASE/SHA256SUMS"
  echo "==> 下载 $RELEASE_ASSET ($FROM_RELEASE)"
  curl -fsSL --retry 3 -o "$tmp_dir/$RELEASE_ASSET" "$release_url"
  curl -fsSL --retry 3 -o "$tmp_dir/SHA256SUMS" "$sums_url"
  expected="$(awk -v f="$RELEASE_ASSET" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp_dir/SHA256SUMS")"
  [ "${#expected}" = 64 ] || { echo "SHA256SUMS 中找不到 $RELEASE_ASSET" >&2; exit 1; }
  actual="$(sha256sum "$tmp_dir/$RELEASE_ASSET" | awk '{print $1}')"
  [ "$actual" = "$expected" ] || { echo "校验失败: $RELEASE_ASSET" >&2; exit 1; }
  BIN_SRC="$tmp_dir/$RELEASE_ASSET"
  echo "  校验通过: $actual"
  version_output="$("$BIN_SRC" version 2>/dev/null || true)"
  [ -n "$version_output" ] && echo "  $version_output"
fi

echo "==> 安装二进制到 $BIN_DST"
install -m 0755 "$BIN_SRC" "$BIN_DST"
install -d -m 0700 "$HOME_DIR"
chmod 0700 "$HOME_DIR"

echo "==> 安装 systemd 单元"
for u in "${UNITS[@]}"; do
  install -m 0644 "$(dirname "$0")/$u" "$UNIT_DIR/$u"
done
systemctl daemon-reload

if [ "$START" = "1" ]; then
  echo "==> 起 PolarisX"
  systemctl enable --now polarisx-sub.service polarisx-collect.timer
  # Web 面板默认只绑本机，按需自行放开
  systemctl enable --now polarisx-web.service || true
  echo "==> 完成"
else
  echo "==> 已安装二进制与单元（未启动服务；需要时执行 bash install.sh --start）"
fi
