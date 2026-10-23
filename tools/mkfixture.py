#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成 parity 夹具：用 Python 版 ixpanel 建一份覆盖所有分支的 panel.db，
再把几种 settings 变体下的 build_config 输出存成 testdata/parity/*.json。

Go 版必须对同一份 DB + settings 生成语义相同的配置 —— 这就是迁移的验收标准。

跑法: python3 tools/mkfixture.py
"""
import base64
import importlib.util
import json
import os
import shutil
import sqlite3
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TESTDATA = os.path.join(ROOT, "testdata")
FIXTURE = os.path.join(TESTDATA, "fixture")
PARITY = os.path.join(TESTDATA, "parity")
IXPANEL_SRC = os.environ.get("IXPANEL_SRC", "/Users/ice/WorkSpace/ixrelay/src/ixpanel.py")


def load_ixpanel(home):
    os.environ["IXPANEL_HOME"] = home
    spec = importlib.util.spec_from_file_location("ixpanel", IXPANEL_SRC)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def psk(n):
    return base64.b64encode(bytes(range(n))).decode()  # 固定值，夹具才可复现


NODES = [
    # tag, proto, net, security, 其它字段
    ("reality-tcp", "vless", "tcp", "reality",
     {"port": 443, "dest": "www.microsoft.com:443", "sni": "www.microsoft.com",
      "priv_key": "REALITYPRIV", "pub_key": "REALITYPUB", "short_id": "0123456789abcdef",
      "flow": "xtls-rprx-vision"}),
    ("tls-vision", "vless", "tcp", "tls",
     {"port": 8443, "sni": "tls.example.com", "cert_file": "/etc/ssl/fullchain.pem",
      "key_file": "/etc/ssl/privkey.pem", "flow": "xtls-rprx-vision"}),
    ("vmess-ws", "vmess", "ws", "tls",
     {"port": 2053, "path": "/ws", "ws_host": "cdn.example.com", "sni": "cdn.example.com",
      "cert_file": "/etc/ssl/fullchain.pem", "key_file": "/etc/ssl/privkey.pem"}),
    ("trojan-grpc", "trojan", "grpc", "tls",
     {"port": 2087, "grpc_service": "GunService", "sni": "grpc.example.com",
      "cert_file": "/etc/ssl/fullchain.pem", "key_file": "/etc/ssl/privkey.pem"}),
    ("ss2022", "shadowsocks", "tcp", "none",
     {"port": 8388, "method": "2022-blake3-aes-128-gcm", "ss_key": psk(16)}),
    ("hy2", "hysteria", "hysteria", "tls",
     {"port": 8443, "sni": "hy2.example.com", "cert_file": "/etc/ssl/fullchain.pem",
      "key_file": "/etc/ssl/privkey.pem", "hy2_up": 60, "hy2_down": 150}),
    ("vless-xhttp", "vless", "xhttp", "none", {"port": 2096, "path": "/x"}),
    ("vless-hu", "vless", "httpupgrade", "none",
     {"port": 2095, "path": "/hu", "ws_host": "hu.example.com"}),
    ("bad-reality", "vless", "tcp", "reality", {"port": 444, "sni": "x.com"}),  # 缺 priv/dest -> 跳过
    ("off-node", "vless", "tcp", "none", {"port": 445, "enabled": 0}),          # 禁用 -> 跳过
]

USERS = [("alice", 1, []), ("bob", 1, [1, 3]), ("carol", 0, [])]


def build_fixture(ix):
    if os.path.exists(FIXTURE):
        shutil.rmtree(FIXTURE)
    os.makedirs(FIXTURE)
    ix.db_init()  # 建表 + 补列
    conn = ix.db()
    for i, (tag, proto, net, sec, extra) in enumerate(NODES):
        cols = {"tag": tag, "proto": proto, "net": net, "security": sec,
                "created": "2026-01-01T00:00:00"}
        cols.update(extra)
        keys = ", ".join(cols)
        qs = ", ".join("?" for _ in cols)
        conn.execute("INSERT INTO nodes(%s) VALUES (%s)" % (keys, qs), tuple(cols.values()))
    for name, enable, binds in USERS:
        conn.execute(
            "INSERT INTO users(name, uuid, password, token, quota_gb, enable, created)"
            " VALUES (?,?,?,?,?,?,?)",
            (name, "11111111-2222-3333-4444-55555555555" + str(len(name)), "pw-" + name,
             "tok-" + name, 100, enable, "2026-01-01T00:00:00"))
        uid = conn.execute("SELECT id FROM users WHERE name=?", (name,)).fetchone()["id"]
        for nid in binds:
            conn.execute("INSERT INTO user_nodes(user_id, node_id) VALUES (?,?)", (uid, nid))
    conn.commit()
    # 先把每个用户在每个节点上的 PSK 生成出来并落库（Python 生成时就会写）
    for n in ix.get_nodes(conn):
        for u in ix.active_users_for_node(conn, n):
            if n["proto"] in ("shadowsocks", "hysteria"):
                ix.user_node_key(conn, u["id"], n)
    conn.close()


VARIANTS = {
    "default": {},
    "useipv6": {"outbound_domain_strategy": "UseIPv6"},
    "warp-socks": {"warp_enable": True, "warp_socks": "127.0.0.1:40000"},
    "warp-wg": {"warp_enable": True, "warp_socks": "", "warp_private_key": "PRIVKEY123",
                "warp_address_v4": "172.16.0.2", "warp_address_v6": "fd01::2",
                "warp_reserved": "1, 2, 3"},
    "chain": {"chain_enable": True, "chain_server": "tw.example.com", "chain_port": 8388,
              "chain_password": "chainpw", "chain_domains": "openai.com,claude.ai"},
    "direct-ports": {"direct_ports": "22, 2222"},
}


def main():
    ix = load_ixpanel(FIXTURE)
    build_fixture(ix)
    os.makedirs(PARITY, exist_ok=True)
    conn = ix.db()
    for name, over in VARIANTS.items():
        s = dict(ix.DEFAULT_SETTINGS)
        s.update(over)
        cfg = ix.build_config(conn, s)
        json.dump(cfg, open(os.path.join(PARITY, name + ".json"), "w"),
                  ensure_ascii=False, indent=2, sort_keys=True)
        json.dump(s, open(os.path.join(PARITY, name + ".settings.json"), "w"),
                  ensure_ascii=False, indent=2, sort_keys=True)
        print("  %-14s inbound=%d outbound=%d rules=%d"
              % (name, len(cfg["inbounds"]), len(cfg["outbounds"]),
                 len(cfg["routing"]["rules"])))
    conn.close()
    print("\n夹具已生成: %s" % FIXTURE)
    print("期望配置:   %s（%d 个变体）" % (PARITY, len(VARIANTS)))


if __name__ == "__main__":
    main()
