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
import subprocess
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


CERT = os.path.join(TESTDATA, "certs", "test.crt")
KEY = os.path.join(TESTDATA, "certs", "test.key")

NODES = [
    # tag, proto, net, security, 其它字段
    ("reality-tcp", "vless", "tcp", "reality",
     {"port": 443, "entry_host": "1.2.3.4", "dest": "www.microsoft.com:443",
      "sni": "www.microsoft.com", "priv_key": "REALITYPRIV", "pub_key": "REALITYPUB",
      "short_id": "0123456789abcdef", "flow": "xtls-rprx-vision"}),
    # 多入口节点：订阅里要展开成 -1/-2 两条
    ("tls-vision", "vless", "tcp", "tls",
     {"port": 8443, "entry_host": "5.6.7.8, 9.10.11.12", "entry_port": 24443,
      "sni": "tls.example.com", "cert_file": CERT, "key_file": KEY,
      "flow": "xtls-rprx-vision"}),
    ("vmess-ws", "vmess", "ws", "tls",
     {"port": 2053, "entry_host": "cdn.example.com", "path": "/ws",
      "ws_host": "cdn.example.com", "sni": "cdn.example.com",
      "cert_file": CERT, "key_file": KEY}),
    ("trojan-grpc", "trojan", "grpc", "tls",
     {"port": 2087, "entry_host": "1.2.3.4", "grpc_service": "GunService",
      "sni": "grpc.example.com", "cert_file": CERT, "key_file": KEY}),
    ("ss2022", "shadowsocks", "tcp", "none",
     {"port": 8388, "entry_host": "1.2.3.4", "method": "2022-blake3-aes-128-gcm",
      "ss_key": psk(16)}),
    ("hy2", "hysteria", "hysteria", "tls",
     {"port": 8443, "entry_host": "1.2.3.4", "entry_sni": "front.example.com",
      "sni": "hy2.example.com", "cert_file": CERT, "key_file": KEY,
      "hy2_up": 60, "hy2_down": 150}),
    ("vless-xhttp", "vless", "xhttp", "none",
     {"port": 2096, "entry_host": "1.2.3.4", "path": "/x"}),
    ("vless-hu", "vless", "httpupgrade", "none",
     {"port": 2095, "entry_host": "1.2.3.4", "path": "/hu", "ws_host": "hu.example.com"}),
    ("bad-reality", "vless", "tcp", "reality", {"port": 444, "entry_host": "1.2.3.4",
                                                "sni": "x.com"}),  # 缺 priv/dest -> 跳过
    ("off-node", "vless", "tcp", "none", {"port": 445, "entry_host": "1.2.3.4",
                                          "enabled": 0}),         # 禁用 -> 跳过
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


def make_cert():
    """自签证书：证书指纹（整张 DER 的 SHA256）与公钥指纹都要能对上。"""
    d = os.path.join(TESTDATA, "certs")
    os.makedirs(d, exist_ok=True)
    if not os.path.exists(CERT):
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                        "-keyout", KEY, "-out", CERT, "-days", "3650",
                        "-subj", "/CN=parity.example.com",
                        "-addext", "subjectAltName=DNS:parity.example.com"],
                       check=True, capture_output=True)
        print("  已生成自签证书 testdata/certs/test.crt")


FROZEN_NOW = "2026-01-02 03:04:05"


def main():
    ix = load_ixpanel(FIXTURE)
    make_cert()
    ix.now_str = lambda: FROZEN_NOW   # 冻结时间戳，Clash 期望值才可复现
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
    # 订阅侧期望：分享链接 / 三种载荷 / 标题 / 下载头
    subs = {"links": {}, "payloads": {}, "titles": {}}
    for u in ix.get_users_all(conn) if hasattr(ix, "get_users_all") else \
            [ix.row_to_dict(r) for r in conn.execute("SELECT * FROM users ORDER BY id")]:
        subs["links"][u["name"]] = ix.links_for_user(conn, u, ix.DEFAULT_SETTINGS)
        for fmt in ("base64", "plain", "json"):
            body, ctype = ix.sub_payload(conn, u, ix.DEFAULT_SETTINGS, fmt)
            subs["payloads"]["%s.%s" % (u["name"], fmt)] = {"body": body, "content_type": ctype}
    for name, over in VARIANTS.items():
        s2 = dict(ix.DEFAULT_SETTINGS)
        s2.update(over)
        subs["titles"][name] = {
            "title": ix.expand_title(s2),
            "disposition": ix.content_disposition(ix.expand_title(s2), ".yaml"),
        }
    subs["titles"]["with-cc-asn"] = {
        "title": ix.expand_title(dict(ix.DEFAULT_SETTINGS, server_cc="US",
                                      server_asn="AS174", server_org="Cogent")),
        "disposition": ix.content_disposition(
            ix.expand_title(dict(ix.DEFAULT_SETTINGS, server_cc="US", server_asn="AS174",
                                 server_org="Cogent")), ".yaml"),
    }
    clash = {}
    for u in [ix.row_to_dict(r) for r in conn.execute("SELECT * FROM users ORDER BY id")]:
        for preset in ix.CLASH_RULE_PRESETS:
            s3 = dict(ix.DEFAULT_SETTINGS, clash_rules=preset)
            clash["%s.%s" % (u["name"], preset)] = ix.clash_yaml(conn, u, s3)
        s4 = dict(ix.DEFAULT_SETTINGS, clash_rules="myclash", direct_ports="22, 2222, x")
        clash["%s.direct-ports" % u["name"]] = ix.clash_yaml(conn, u, s4)
    subs["clash"] = clash
    subs["clash_frozen_now"] = FROZEN_NOW
    print("  Clash 期望: %d 份（%d 预设 × %d 用户 + direct_ports 变体）"
          % (len(clash), len(ix.CLASH_RULE_PRESETS) + 1,
             len(subs["links"])))
    subs["cert_fp"] = {
        "sha256": ix.cert_sha256(CERT),
        "pubkey_sha256": ix.cert_pubkey_sha256(CERT),
    }
    json.dump(subs, open(os.path.join(PARITY, "subscriptions.json"), "w"),
              ensure_ascii=False, indent=2, sort_keys=True)
    print("  订阅期望: links=%d payload=%d titles=%d"
          % (sum(len(v) for v in subs["links"].values()), len(subs["payloads"]),
             len(subs["titles"])))
    conn.close()
    print("\n夹具已生成: %s" % FIXTURE)
    print("期望配置:   %s（%d 个变体）" % (PARITY, len(VARIANTS)))


if __name__ == "__main__":
    main()
