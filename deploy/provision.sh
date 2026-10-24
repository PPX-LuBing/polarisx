#!/usr/bin/env bash
# PolarisX 装机脚本：把一台干净的 VPS 变成可用的落地机。
#
# 装二进制与 systemd 单元是 deploy/install.sh 的事；这个脚本负责 install.sh 不管的部分：
#   xray（含版本策略与 geodata）→ 自签证书 → 订阅前置（nginx）→ 8080 加固 → 可选建节点/用户
#
# 用法:
#   ./provision.sh                       # 交互式（xray 版本会问一句）
#   ./provision.sh --xray-version v26.6.27 --yes
#   ./provision.sh --sub-mode tls --sub-path hash
#   ./provision.sh --node reality --user alice --quota 100
#
# 关键默认值（都是有理由的，别随手改）:
#   xray 稳定版 v26.6.27 —— Xray ≥26.7 的 REALITY 与 Clash/mihomo 内核不兼容
#     （客户端会报 REALITY authentication failed），所以默认钉这个版本。
#   订阅前置默认 HTTP + token 路径 + 限速；要 TLS 用 --sub-mode tls。
#   8080 默认只允许本机访问（否则可绕过前置直接用 token 取订阅）。

set -euo pipefail

# ---------------------------------------------------------------------------
# 参数
# ---------------------------------------------------------------------------
XRAY_VERSION="${XRAY_VERSION:-}"
XRAY_STABLE="${XRAY_STABLE:-v26.6.27}"
XRAY_CHOICE="${XRAY_CHOICE:-ask}"          # ask | stable | latest
SUB_MODE="${SUB_MODE:-http}"               # http | tls | off
SUB_PATH="${SUB_PATH:-token}"              # token | hash
SUB_PORT="${SUB_PORT:-}"
CERT_DIR="${CERT_DIR:-/etc/polarisx-sub-tls}"
HARDENING="${HARDENING:-1}"
YES=0
NODE_KIND="" NODE_TAG="" NODE_PORT="" NODE_SNI="" ENTRY_HOST=""
UNAME="" QUOTA="" EXPIRE=""
XRAY_DIR=/usr/local/bin
XRAY_CONF_DIR=/usr/local/etc/xray
POLARISX="${POLARISX:-polarisx}"

c_ok()   { printf '\033[32m%s\033[0m\n' "$*"; }
c_warn() { printf '\033[33m%s\033[0m\n' "$*"; }
c_err()  { printf '\033[31m%s\033[0m\n' "$*" >&2; }
step()   { echo; echo "==> $*"; }
die()    { c_err "错误: $*"; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --xray-version) XRAY_VERSION="$2"; XRAY_CHOICE=stable; shift 2 ;;
    --xray-latest)  XRAY_CHOICE=latest; shift ;;
    --stable)       XRAY_STABLE="$2"; shift 2 ;;
    --sub-mode)     SUB_MODE="$2"; shift 2 ;;
    --sub-port)     SUB_PORT="$2"; shift 2 ;;
    --sub-path)     SUB_PATH="$2"; shift 2 ;;
    --cert-dir)     CERT_DIR="$2"; shift 2 ;;
    --no-harden)    HARDENING=0; shift ;;
    --node)         NODE_KIND="$2"; shift 2 ;;
    --node-tag)     NODE_TAG="$2"; shift 2 ;;
    --node-port)    NODE_PORT="$2"; shift 2 ;;
    --sni)          NODE_SNI="$2"; shift 2 ;;
    --entry-host)   ENTRY_HOST="$2"; shift 2 ;;
    --user)         UNAME="$2"; shift 2 ;;
    --quota)        QUOTA="$2"; shift 2 ;;
    --expire)       EXPIRE="$2"; shift 2 ;;
    -y|--yes)       YES=1; shift ;;
    -h|--help)      sed -n '2,22p' "$0" | sed 's/^# \?//'; exit 0 ;;
    *) die "未知参数: $1（--help 看用法）" ;;
  esac
done
[ "$(id -u)" = "0" ] || die "请用 root 运行"
case "$SUB_MODE" in http|tls|off) ;; *) die "--sub-mode 只能是 http/tls/off" ;; esac
case "$SUB_PATH" in token|hash) ;; *) die "--sub-path 只能是 token/hash" ;; esac
[ -n "$SUB_PORT" ] || case "$SUB_MODE" in
  http) SUB_PORT=61887 ;;
  tls)  SUB_PORT=61886 ;;
  off)  SUB_PORT="" ;;
esac

# ---------------------------------------------------------------------------
# 0) 依赖
# ---------------------------------------------------------------------------
step "0/6 检查依赖"
need=""
for c in curl unzip openssl python3; do command -v "$c" >/dev/null 2>&1 || need="$need $c"; done
if [ "$SUB_MODE" != "off" ] && ! command -v nginx >/dev/null 2>&1; then need="$need nginx"; fi
if [ -n "$need" ]; then
  c_warn "缺少:$need，尝试安装"
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq >/dev/null 2>&1 || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $need >/dev/null 2>&1 || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q $need >/dev/null 2>&1 || true
  elif command -v yum >/dev/null 2>&1; then
    yum install -y -q $need >/dev/null 2>&1 || true
  fi
fi
for c in curl unzip openssl; do command -v "$c" >/dev/null 2>&1 || die "装不上 $c"; done
c_ok "  依赖就绪"

# ---------------------------------------------------------------------------
# 1) 安装 xray（版本策略：显式 > --xray-latest > 交互选择 > 稳定版）
# ---------------------------------------------------------------------------
step "1/6 安装 xray"
pick_latest() {
  curl -fsSL --max-time 20 https://api.github.com/repos/XTLS/Xray-core/tags 2>/dev/null \
    | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d[0]["name"] if d else "")' 2>/dev/null || true
}
if [ -z "$XRAY_VERSION" ]; then
  case "$XRAY_CHOICE" in
    latest) XRAY_VERSION="$(pick_latest)"; [ -n "$XRAY_VERSION" ] || XRAY_VERSION="$XRAY_STABLE" ;;
    stable) XRAY_VERSION="$XRAY_STABLE" ;;
    ask)
      if [ "$YES" = "1" ] || [ ! -t 0 ]; then
        XRAY_VERSION="$XRAY_STABLE"
      else
        echo "    xray 版本："
        echo "      1) 稳定版 $XRAY_STABLE —— REALITY 与 Clash/mihomo 兼容（推荐）"
        echo "      2) 最新版（新特性，但 REALITY 对 Clash/mihomo 内核不兼容）"
        printf "    选 [1/2]（默认 1）: "
        read -r ans </dev/tty || ans=1
        case "$ans" in
          2) XRAY_VERSION="$(pick_latest)"; [ -n "$XRAY_VERSION" ] || XRAY_VERSION="$XRAY_STABLE" ;;
          *) XRAY_VERSION="$XRAY_STABLE" ;;
        esac
      fi ;;
  esac
fi
case "$(uname -m)" in
  x86_64|amd64) XRAY_ARCH=64 ;;
  aarch64|arm64) XRAY_ARCH=arm64-v8a ;;
  armv7l) XRAY_ARCH=arm32-v7a ;;
  *) die "不支持的架构: $(uname -m)" ;;
esac
GH="https://github.com/XTLS/Xray-core/releases/download/$XRAY_VERSION/Xray-linux-$XRAY_ARCH.zip"
mkdir -p "$XRAY_DIR" "$XRAY_CONF_DIR"
# 注意：不要写成 `xray version | head -1 | grep -q`——set -o pipefail 下 head 提前关闭管道
# 会让 xray 收到 SIGPIPE 返回 141，整个管道判为失败，于是每次都白下一遍。
want_ver="${XRAY_VERSION#v}"
have_ver="$("$XRAY_DIR/xray" version 2>/dev/null | sed -n 1p || true)"
if [ -x "$XRAY_DIR/xray" ] && [ -n "$have_ver" ] && [ "${have_ver#*Xray }" != "$have_ver" ] \
   && case "$have_ver" in *"$want_ver"*) true ;; *) false ;; esac; then
  c_ok "  已是 $XRAY_VERSION，跳过下载"
else
  got=0
  # 镜像回退：GitHub 直连不通时换加速前缀
  for prefix in "" "https://ghfast.top/" "https://gh-proxy.com/"; do
    if curl -fsSL --max-time 300 -o /tmp/xray.zip "${prefix}${GH}" 2>/dev/null \
       && unzip -o -q /tmp/xray.zip -d /tmp/xray_pkg 2>/dev/null; then got=1; break; fi
  done
  [ "$got" = "1" ] || die "xray 下载失败（$XRAY_VERSION）：网络或镜像问题"
  install -m 755 /tmp/xray_pkg/xray "$XRAY_DIR/xray"
  rm -rf /tmp/xray.zip /tmp/xray_pkg
  c_ok "  已安装 $("$XRAY_DIR/xray" version 2>/dev/null | head -1)"
fi

# geodata：xray 26+ 不再内置，且按「配置文件目录/二进制目录/share」相对查找，都放一份
step "  补 geodata（xray 26+ 需要）"
GD="$XRAY_DIR"
for n in geoip.dat geosite.dat; do
  if [ ! -f "$GD/$n" ]; then
    for u in "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/$n" \
             "https://ghfast.top/https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/$n"; do
      curl -fsSL --max-time 180 -o "$GD/$n" "$u" 2>/dev/null && [ -s "$GD/$n" ] && break
    done
  fi
  [ -s "$GD/$n" ] || { c_warn "  $n 没拿到（用到 geoip/geosite 规则时会失败）"; continue; }
  for d in "$GD" "$XRAY_CONF_DIR" /usr/local/share/xray /usr/local/xray; do
    mkdir -p "$d" 2>/dev/null || continue
    [ "$d" = "$GD" ] || ln -sf "$GD/$n" "$d/$n" 2>/dev/null || true
  done
done
c_ok "  geodata 就位"

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  cat > /etc/systemd/system/xray.service <<'EOF'
[Unit]
Description=Xray Service
Documentation=https://github.com/xtls
After=network.target nss-lookup.target

[Service]
User=root
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ExecStart=/usr/local/bin/xray run -config /usr/local/etc/xray/config.json
Restart=on-failure
RestartPreventExitStatus=23
LimitNPROC=10000
LimitNOFILE=1000000

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable xray.service >/dev/null 2>&1 || true
  c_ok "  xray.service 已安装并设为开机自启"
fi

# ---------------------------------------------------------------------------
# 2) 自签证书（CA + leaf 两级；只用自签 leaf 会被 Go 内核判为不合规）
# ---------------------------------------------------------------------------
CERT_SNI="${NODE_SNI:-polarisx-sub.local}"
SERVER_HOST="${ENTRY_HOST:-$(curl -fsSL --max-time 10 https://api.ipify.org 2>/dev/null || echo 127.0.0.1)}"
ensure_cert() {
  mkdir -p "$CERT_DIR"
  if [ -s "$CERT_DIR/cert.pem" ] && [ -s "$CERT_DIR/private.key" ]; then
    c_ok "  复用已有证书 $CERT_DIR/cert.pem"; return 0
  fi
  local cnf="$CERT_DIR/openssl-ix.cnf"
  cat > "$cnf" <<IXCNF
[req]
distinguished_name = dn
prompt = no
[dn]
CN = polarisx
[v3_ca]
basicConstraints = critical,CA:TRUE
keyUsage = critical,digitalSignature,keyCertSign,cRLSign
subjectKeyIdentifier = hash
[v3_srv]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:$CERT_SNI,IP:$SERVER_HOST,DNS:polarisx-sub.local
IXCNF
  openssl req -x509 -newkey rsa:2048 -nodes -days 36500 -keyout "$CERT_DIR/ca.key" \
    -out "$CERT_DIR/ca.crt" -subj "/CN=polarisx-ca" -config "$cnf" -extensions v3_ca >/dev/null 2>&1
  openssl req -new -newkey rsa:2048 -nodes -keyout "$CERT_DIR/private.key" \
    -out "$CERT_DIR/srv.csr" -subj "/CN=$CERT_SNI" -config "$cnf" >/dev/null 2>&1
  openssl x509 -req -in "$CERT_DIR/srv.csr" -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" \
    -CAcreateserial -days 36500 -out "$CERT_DIR/cert.pem" -extfile "$cnf" -extensions v3_srv >/dev/null 2>&1
  chmod 600 "$CERT_DIR/private.key" "$CERT_DIR/ca.key"
  rm -f "$CERT_DIR/srv.csr"
  openssl verify -CAfile "$CERT_DIR/ca.crt" "$CERT_DIR/cert.pem" >/dev/null 2>&1 \
    || die "证书链自检失败"
  openssl x509 -in "$CERT_DIR/cert.pem" -noout -fingerprint -sha256 2>/dev/null \
    | sed 's/.*=//; s/://g' > "$CERT_DIR/leaf.sha256"
  c_ok "  已签发证书链（CA → leaf，CN=$CERT_SNI）"
  echo "     客户端锁定指纹(pinSHA256): $(cat "$CERT_DIR/leaf.sha256")"
}
if [ "$SUB_MODE" = "tls" ] || [ -n "$NODE_KIND" ]; then
  step "2/6 自签证书"
  ensure_cert
else
  step "2/6 自签证书（跳过：订阅前置非 TLS 且未建节点）"
fi

# ---------------------------------------------------------------------------
# 3) 订阅前置（nginx）：只放行 /sub/...，其余 403，并限速防 token 枚举
# ---------------------------------------------------------------------------
if [ "$SUB_MODE" != "off" ]; then
  step "3/6 订阅前置（$SUB_MODE :$SUB_PORT）"
  command -v nginx >/dev/null 2>&1 || die "nginx 没装上"
  mkdir -p /etc/nginx-sub /var/lib/nginx-sub/{body,proxy,fastcgi,uwsgi,scgi} /var/log/nginx
  NGINX_BIN="$(command -v nginx)"
  TMPDIRS='    client_body_temp_path /var/lib/nginx-sub/body;
    proxy_temp_path       /var/lib/nginx-sub/proxy;
    fastcgi_temp_path     /var/lib/nginx-sub/fastcgi;
    uwsgi_temp_path       /var/lib/nginx-sub/uwsgi;
    scgi_temp_path        /var/lib/nginx-sub/scgi;'

  if [ "$SUB_MODE" = "tls" ]; then
    HASH="$(python3 -c 'import os;print(os.urandom(16).hex())')"
    if [ "$SUB_PATH" = "hash" ]; then
      printf 'HASH=%s\n' "$HASH" > /root/.polarisx_sub_hash
      LOC="location ~ ^/$HASH/sub/(?<subrest>.*)\$ {"
    else
      LOC="location ~ ^/sub/(?<subrest>.*)\$ {"
    fi
    cat > /etc/nginx-sub/nginx.conf <<NGINXEOF
user  root;
worker_processes  auto;
error_log  /var/log/nginx/sub-error.log warn;
pid        /run/nginx-sub.pid;
events { worker_connections 1024; }
http {
  include       /etc/nginx/mime.types;
  default_type  application/octet-stream;
  access_log    off;
  server_tokens off;
  keepalive_timeout 65;
$TMPDIRS
  limit_req_zone \$binary_remote_addr zone=subzone:10m rate=30r/m;
  server {
    listen $SUB_PORT ssl;
    listen [::]:$SUB_PORT ssl;
    server_name _;
    ssl_certificate     $CERT_DIR/cert.pem;
    ssl_certificate_key $CERT_DIR/private.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:SSL:2m;
    $LOC
      limit_req zone=subzone burst=20 nodelay;
      proxy_http_version 1.1;
      proxy_set_header Host \$host;
      proxy_set_header Connection "";
      proxy_pass http://127.0.0.1:8080/sub/\$subrest;
    }
    location / { return 403; }
  }
}
NGINXEOF
    UNIT=sub-proxy.service; CONF=/etc/nginx-sub/nginx.conf
    DESC="PolarisX subscription TLS front (nginx)"
  else
    cat > /etc/nginx-sub/http.conf <<NGINXEOF
user  root;
worker_processes  auto;
error_log  /var/log/nginx/sub-error.log warn;
pid        /run/nginx-sub-http.pid;
events { worker_connections 1024; }
http {
  include       /etc/nginx/mime.types;
  default_type  application/octet-stream;
  access_log    off;
  server_tokens off;
  keepalive_timeout 65;
$TMPDIRS
  # 路径就是 token 时 token 是唯一凭据 → 必须限速防枚举
  limit_req_zone \$binary_remote_addr zone=subzone:10m rate=30r/m;
  server {
    listen $SUB_PORT;
    listen [::]:$SUB_PORT;
    server_name _;
    location ~ ^/sub/(?<subrest>.*)\$ {
      limit_req zone=subzone burst=20 nodelay;
      proxy_http_version 1.1;
      proxy_set_header Host \$host;
      proxy_set_header Connection "";
      proxy_pass http://127.0.0.1:8080/sub/\$subrest;
    }
    location / { return 403; }
  }
}
NGINXEOF
    UNIT=sub-http.service; CONF=/etc/nginx-sub/http.conf
    DESC="PolarisX subscription plain-HTTP front (nginx)"
  fi

  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    cat > "/etc/systemd/system/$UNIT" <<UNITEOF
[Unit]
Description=$DESC
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStartPre=$NGINX_BIN -c $CONF -t
ExecStart=$NGINX_BIN -c $CONF -g "daemon off;"
ExecReload=/bin/kill -HUP \$MAINPID
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNITEOF
    "$NGINX_BIN" -c "$CONF" -t >/dev/null 2>&1 || die "nginx 配置校验失败：$NGINX_BIN -c $CONF -t"
    systemctl daemon-reload
    systemctl enable --now "$UNIT" >/dev/null 2>&1
    systemctl restart "$UNIT"
    c_ok "  $UNIT 已启动（:$SUB_PORT → 127.0.0.1:8080）"
  else
    c_warn "  无 systemd，跳过前置启停"
  fi
else
  step "3/6 订阅前置（跳过）"
fi

# ---------------------------------------------------------------------------
# 4) 加固：8080 只允许本机（否则可绕过前置直接用 token 取订阅）+ 规则持久化
# ---------------------------------------------------------------------------
if [ "$HARDENING" = "1" ]; then
  step "4/6 加固 8080（仅本机可达）"
  if command -v iptables >/dev/null 2>&1; then
    if iptables -C INPUT -p tcp --dport 8080 ! -i lo -j DROP 2>/dev/null; then
      c_ok "  规则已存在"
    else
      iptables -I INPUT 1 -p tcp --dport 8080 ! -i lo -j DROP && c_ok "  已添加规则"
    fi
    if command -v iptables-save >/dev/null 2>&1; then
      mkdir -p /etc/iptables && iptables-save > /etc/iptables/rules.v4
      if [ -x /sbin/iptables-restore ] || [ -x /usr/sbin/iptables-restore ]; then
        cat > /etc/systemd/system/iptables-restore.service <<'EOF'
[Unit]
Description=Restore iptables rules
Before=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
ExecStart=/sbin/iptables-restore /etc/iptables/rules.v4
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF
        systemctl daemon-reload >/dev/null 2>&1 || true
        systemctl enable iptables-restore.service >/dev/null 2>&1 || true
        c_ok "  规则已持久化（iptables-restore.service）"
      fi
    fi
  else
    c_warn "  没有 iptables，跳过（建议手工限制 8080）"
  fi
else
  step "4/6 加固（--no-harden 跳过）"
fi

# ---------------------------------------------------------------------------
# 5) 可选：建节点 + 用户 + 应用（走 PolarisX 自己的 CLI）
# ---------------------------------------------------------------------------
if [ -n "$NODE_KIND" ]; then
  step "5/6 创建节点与用户"
  command -v "$POLARISX" >/dev/null 2>&1 || die "找不到 $POLARISX（先跑 deploy/install.sh）"
  TAG="${NODE_TAG:-polarisx-$NODE_KIND}"
  PORT="${NODE_PORT:-443}"
  SNI="${NODE_SNI:-auto}"
  HOST="${ENTRY_HOST:-$SERVER_HOST}"
  "$POLARISX" node rm "$TAG" --yes >/dev/null 2>&1 || true
  case "$NODE_KIND" in
    reality)
      "$POLARISX" node add --tag "$TAG" --proto vless --net tcp --security reality \
        --port "$PORT" --entry-host "$HOST" --sni "$SNI" --flow xtls-rprx-vision 2>&1 | sed 's/^/  /' ;;
    hy2|hysteria2)
      "$POLARISX" node add --tag "$TAG" --proto hysteria --net hysteria --security tls \
        --port "$PORT" --entry-host "$HOST" --sni "$CERT_SNI" \
        --cert-file "$CERT_DIR/cert.pem" --key-file "$CERT_DIR/private.key" 2>&1 | sed 's/^/  /' ;;
    *) die "--node 支持 reality / hy2（其它协议用 $POLARISX node add 手工建）" ;;
  esac
  if [ -n "$UNAME" ]; then
    "$POLARISX" user info "$UNAME" >/dev/null 2>&1 \
      || "$POLARISX" user add "$UNAME" --quota "${QUOTA:-100}" --expire "${EXPIRE:-}" 2>&1 | sed 's/^/  /'
  fi
  "$POLARISX" apply 2>&1 | sed 's/^/  /'
  if command -v systemctl >/dev/null 2>&1; then
    systemctl restart xray 2>/dev/null || systemctl start xray
    sleep 2
    systemctl is-active --quiet xray || die "xray 没起来：journalctl -u xray -n 30"
    c_ok "  xray 已监听（$TAG :$PORT）"
  fi
else
  step "5/6 创建节点与用户（跳过：未指定 --node）"
fi

# ---------------------------------------------------------------------------
# 6) 自检 + 交付信息
# ---------------------------------------------------------------------------
step "6/6 自检"
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  for u in xray polarisx-sub polarisx-web polarisx-collect.timer ${UNIT:-}; do
    [ -n "$u" ] || continue
    printf "  %-24s %s\n" "$u" "$(systemctl is-active "$u" 2>/dev/null)"
  done
fi
if curl -fsS -m 5 -o /dev/null "http://127.0.0.1:8080/" 2>/dev/null; then
  c_ok "  后端 8080 可达"
else
  c_warn "  后端 8080 没响应（polarisx-sub 起来了吗）"
fi
if [ "$SUB_MODE" != "off" ]; then
  if [ "$SUB_MODE" = "tls" ]; then PROBE="https://127.0.0.1:$SUB_PORT"; PROBE_OPT="-k"
  else PROBE="http://127.0.0.1:$SUB_PORT"; PROBE_OPT=""; fi
  code="$(curl -s $PROBE_OPT -o /dev/null -m 5 -w '%{http_code}' "$PROBE/sub/x/clash" 2>/dev/null || echo 000)"
  case "$code" in
    200|404|401|403) c_ok "  前置 :$SUB_PORT 有响应（http=$code）" ;;
    *) c_warn "  前置 :$SUB_PORT 无响应（http=$code）" ;;
  esac
fi
TOKEN="$(python3 - <<'PY' 2>/dev/null || true
import sqlite3, os, sys, json
home = os.environ.get("POLARISX_HOME") or "/var/lib/polarisx"
try:
    c = sqlite3.connect(os.path.join(home, "panel.db"))
    r = c.execute("select token from users order by id limit 1").fetchone()
    print(r[0] if r else "")
except Exception:
    print("")
PY
)"
echo
c_ok "装机完成"
if [ -n "$TOKEN" ]; then
  SCHEME=http; [ "$SUB_MODE" = "tls" ] && SCHEME=https
  GATE=""
  [ "$SUB_MODE" = "tls" ] && [ "$SUB_PATH" = "hash" ] && GATE="$(sed -n 's/^HASH=//p' /root/.polarisx_sub_hash 2>/dev/null)/"
  echo "  订阅地址: $SCHEME://$SERVER_HOST:$SUB_PORT/${GATE}sub/$TOKEN/clash"
  echo "  用量查询: $SCHEME://$SERVER_HOST:$SUB_PORT/${GATE}sub/$TOKEN/info"
else
  echo "  还没有用户：$POLARISX user add <名字> --quota 100"
fi
[ "$SUB_MODE" = "tls" ] && echo "  TLS 自签证书：客户端需信任 $CERT_DIR/ca.crt 或锁定 pinSHA256=$(cat "$CERT_DIR/leaf.sha256" 2>/dev/null)"
echo "  下一步：$POLARISX doctor"
