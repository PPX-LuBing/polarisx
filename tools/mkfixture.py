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


# 生成期望值时用真实路径（要真读证书算指纹）；落盘前再把 DB 里的路径改写成 {REPO} 占位符
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
FROZEN_TODAY = "2026-03-10"          # 额度/重置相关用这个"今天"
QUOTA_DIR = os.path.join(TESTDATA, "fixture-quota")


def frozen_dt(today_iso):
    """把模块里的 dt 换掉，让 dt.date.today() 固定 —— 否则期望值不可复现。"""
    import datetime as _dt
    y, m, d = (int(x) for x in today_iso.split("-"))

    class FrozenDate(_dt.date):
        @classmethod
        def today(cls):
            return _dt.date(y, m, d)

    class Shim:
        date, datetime, time, timedelta = FrozenDate, _dt.datetime, _dt.time, _dt.timedelta
    return Shim


_MB = 1024 ** 2
QUOTA_USERS = [
    # name, used_up, used_down, quota_gb, expire_at, enable, last_reset, disabled_reason
    ("over-reset", 600 * _MB, 600 * _MB, 1, "", 1, "", ""),      # 跨重置日：先清零，不该再被停用
    ("over-now", 1100 * _MB, 0, 1, "", 1, FROZEN_TODAY, ""),     # 本周期超额 -> 停用
    ("warn", 850 * _MB, 0, 1, "", 1, FROZEN_TODAY, ""),          # 83% -> 预警
    ("reset-old", 100 * _MB, 100 * _MB, 2, "", 0, "2026-01-01", "流量超额"),  # 停用 + 跨周期 -> 恢复
    ("reset-current", 100 * _MB, 100 * _MB, 2, "", 1, FROZEN_TODAY, ""),      # 本周期已重置 -> 不动
    ("expired", 0, 0, 0, "2026-01-01", 1, "", ""),               # 已过期 -> 停用
    ("fresh", 0, 0, 5, "2027-01-01", 1, "", ""),                 # 正常
]

STATS_SAMPLES = {
    "json-zero": '{"stat":[{"name":"user>>>alice>>>traffic>>>uplink","value":1234},'
                 '{"name":"user>>>bob>>>traffic>>>downlink"}]}',
    "json-array": '[{"name":"user>>>alice>>>traffic>>>downlink","value":7}]',
    "proto-text": 'stat: <\n  name: "user>>>alice>>>traffic>>>uplink"\n  value: 999\n>\n'
                  'stat: <\n  name: "user>>>bob>>>traffic>>>downlink"\n  value: 5\n>\n',
    "value-first": 'stat: < value: 42 name: "user>>>x>>>traffic>>>uplink" >',
    "empty": "",
    "garbage": "not a stats output at all",
}


def build_quota_fixture(qx):
    """额度/重置/采集的状态型夹具（独立 HOME，独立 panel.db）"""
    if os.path.exists(QUOTA_DIR):
        shutil.rmtree(QUOTA_DIR)
    os.makedirs(QUOTA_DIR)
    qx.db_init()
    conn = qx.db()
    for name, up, down, quota, expire, enable, last, reason in QUOTA_USERS:
        conn.execute("INSERT INTO users(name, uuid, password, token, quota_gb, used_up,"
                     " used_down, expire_at, enable, last_reset, disabled_reason, created)"
                     " VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
                     (name, "uuid-" + name, "pw-" + name, "tok-" + name, quota, up, down,
                      expire, enable, last, reason, "2026-01-01T00:00:00"))
    conn.commit()
    # 立刻留一份原始副本：后面要跑 auto_reset/collect，跑完得还原
    conn.close()
    shutil.copy2(os.path.join(QUOTA_DIR, "panel.db"),
                 os.path.join(QUOTA_DIR, "panel.db.pristine"))
    return qx.db()


def dump_users(conn):
    return [dict(r) for r in conn.execute("SELECT * FROM users ORDER BY id")]


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
    sb = {}
    for u in [ix.row_to_dict(r) for r in conn.execute("SELECT * FROM users ORDER BY id")]:
        for dns in ("doh", "system"):
            txt, skipped = ix.singbox_config(conn, u, dict(ix.DEFAULT_SETTINGS, singbox_dns=dns))
            sb["%s.%s" % (u["name"], dns)] = {"json": json.loads(txt), "skipped": skipped}
    subs["singbox"] = sb
    print("  sing-box 期望: %d 份" % len(sb))
    subs["clash_frozen_now"] = FROZEN_NOW
    print("  Clash 期望: %d 份（%d 预设 × %d 用户 + direct_ports 变体）"
          % (len(clash), len(ix.CLASH_RULE_PRESETS) + 1,
             len(subs["links"])))
    # ---- 阶段④：额度 / 重置 / 采集 ----
    qx = load_ixpanel(QUOTA_DIR)
    qx.dt = frozen_dt(FROZEN_TODAY)
    qconn = build_quota_fixture(qx)
    pristine = os.path.join(QUOTA_DIR, "panel.db.pristine")
    qset = dict(qx.DEFAULT_SETTINGS)
    qset.update({"quota_reset_day": 5, "traffic_unit_gib": True,
                 "xray_config": os.path.join(QUOTA_DIR, "xray-config.json"),
                 "verify_config": False, "restart_cmd": "true"})
    quota = {
        "today": FROZEN_TODAY,
        "settings": {k: qset[k] for k in ("quota_reset_day", "traffic_unit_gib")},
        "human_bytes": {str(n): qx.human_bytes(n)
                        for n in (0, 1, 999, 1024, 1536, 1048576, 3 * 1024 ** 3,
                                  1024 ** 4, 1234567890123)},
        "quota_bytes": {str(g): qx.quota_bytes({"quota_gb": g}, qset)
                        for g in (0, 0.5, 1, 100)},
        "quota_bytes_si": {str(g): qx.quota_bytes({"quota_gb": g}, dict(qset, traffic_unit_gib=False))
                           for g in (1, 100)},
        "reset_day": {str(v): qx.reset_day_of_month(dict(qset, quota_reset_day=v))
                      for v in (0, 1, 5, 28, 29, 31)},
        "last_reset_due": {d: (qx.last_reset_due(qx.dt.date.fromisoformat(d), 5).isoformat()
                               if qx.last_reset_due(qx.dt.date.fromisoformat(d), 5) else None)
                           for d in ("2026-03-04", "2026-03-05", "2026-03-10",
                                     "2026-01-03", "2026-01-05")},
        "parse_stats": {k: qx.parse_stats(v) for k, v in STATS_SAMPLES.items()},
        "userinfo": {u[0]: qx.userinfo_header(qconn, qx.get_user(qconn, u[0]), qset)
                     for u in QUOTA_USERS},
    }
    def restore_pristine():
        """把 DB 还原成原始状态（两条路径都从同一个起点跑，期望值才可比）"""
        nonlocal qconn
        qconn.close()
        shutil.copy2(pristine, os.path.join(QUOTA_DIR, "panel.db"))
        for sfx in ("-wal", "-shm"):
            f = os.path.join(QUOTA_DIR, "panel.db" + sfx)
            if os.path.exists(f):
                os.unlink(f)
        qconn = qx.db()

    # 路径一：每月重置（从原始状态跑两次，验幂等）
    quota["auto_reset_count"] = qx.auto_reset_monthly(qconn, qset)
    quota["after_reset"] = dump_users(qconn)
    quota["auto_reset_again"] = qx.auto_reset_monthly(qconn, qset)
    quota["after_reset_again"] = dump_users(qconn)
    restore_pristine()
    # 采集：注入一份统计，跑 collect（不踢连接、不重启）
    stats_json = json.dumps({"stat": [
        {"name": "user>>>fresh>>>traffic>>>uplink", "value": 500},
        {"name": "user>>>fresh>>>traffic>>>downlink", "value": 700},
        {"name": "user>>>warn>>>traffic>>>uplink", "value": 100},
        {"name": "user>>>ghost>>>traffic>>>uplink", "value": 1},   # 已删除用户，应忽略
    ]})
    quota["collect_stats_json"] = stats_json
    qx.fetch_stats = lambda settings, reset=True: qx.parse_stats(stats_json)
    qx.restart_xray = lambda settings, quiet=True, flush=True: True
    # 路径二：采集（也从原始状态跑：collect 内部会先做每月重置，这是真实的月初场景）
    rep = qx.collect(qset, conn=qconn, kick=False, verbose=False)
    quota["collect_report"] = rep
    quota["after_collect"] = dump_users(qconn)
    quota["raw_stats"] = [dict(r) for r in qconn.execute("SELECT * FROM raw_stats ORDER BY email")]
    # 跑过 auto_reset 与 collect，DB 已被改脏；提交给 Go 测试的必须是**原始**状态
    restore_pristine()
    qconn.close()
    os.unlink(pristine)
    json.dump(quota, open(os.path.join(PARITY, "quota.json"), "w"),
              ensure_ascii=False, indent=2, sort_keys=True)
    print("  额度期望: 重置 %d 人 / 再次重置 %d 人 / 采集 +%d↑+%d↓ 停用 %d 预警 %d"
          % (quota["auto_reset_count"], quota["auto_reset_again"],
             rep["added_up"], rep["added_down"], len(rep["disabled"]), len(rep["warned"])))

    subs["cert_fp"] = {
        "sha256": ix.cert_sha256(CERT),
        "pubkey_sha256": ix.cert_pubkey_sha256(CERT),
    }
    subs["repo_root"] = ROOT
    # 夹具不能带生成机器的绝对路径：落盘前统一改写成 {REPO}
    for dbpath in (os.path.join(FIXTURE, "panel.db"), os.path.join(QUOTA_DIR, "panel.db")):
        if not os.path.exists(dbpath):
            continue
        c2 = sqlite3.connect(dbpath)
        for col in ("cert_file", "key_file"):
            c2.execute("UPDATE nodes SET %s=REPLACE(%s, ?, '{REPO}')" % (col, col), (ROOT,))
        c2.commit()
        c2.close()
    print("  夹具路径已占位符化（{REPO}）")
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
