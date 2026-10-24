package panel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 自检的选人与失败路径：不联网、不起真 xray，只验证"谁被选中"和"失败怎么报"。
// 真实连通性由 `polarisx selftest` 在落地机上实测（那也是这个函数的下游）。

func selftestStore(t *testing.T) (*Store, Settings) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	st := Defaults()
	st.ServerHost = "203.0.113.9"
	st.XrayConfig = filepath.Join(dir, "config.json")
	return store, st
}

func TestPickUserForNode(t *testing.T) {
	store, _ := selftestStore(t)
	alice, err := store.CreateUser("alice", 10, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	n := Node{Tag: "n1", Proto: "vless", Net: "tcp", Security: "none", Port: 443,
		EntryHost: "1.2.3.4", Enabled: 1}
	if err := store.CreateNode(n); err != nil {
		t.Fatal(err)
	}

	// 不指定名字：挑第一个启用的、且绑了这个节点的用户
	got, err := PickUserForNode(store, n, "")
	if err != nil {
		t.Fatalf("应能自动挑到用户: %v", err)
	}
	if got.Name != alice.Name {
		t.Errorf("挑错用户: %s", got.Name)
	}

	// 指定不存在的用户：要报错，不能悄悄换成别人（否则测出来的结论对不上）
	if _, err := PickUserForNode(store, n, "nobody"); err == nil {
		t.Error("指定不存在的用户应报错")
	} else if !strings.Contains(err.Error(), "nobody") {
		t.Errorf("错误里应带上名字: %v", err)
	}

	// 停用的用户不算可用
	if err := store.UpdateUser(alice.Name, []string{"enable=0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PickUserForNode(store, n, ""); err == nil {
		t.Error("没有可用用户时应报错")
	}
}

// 每个节点都要有一条结果，顺序与入参一致；xray 配置校验失败要如实报出来。
func TestSelfTestAllReportsPerNode(t *testing.T) {
	store, st := selftestStore(t)
	if _, err := store.CreateUser("alice", 10, "", "", nil); err != nil {
		t.Fatal(err)
	}
	nodes := []Node{
		{Tag: "n1", Proto: "vless", Net: "tcp", Security: "none", Port: 443,
			EntryHost: "1.2.3.4", Enabled: 1},
		{Tag: "n2", Proto: "trojan", Net: "tcp", Security: "tls", Port: 8443,
			EntryHost: "1.2.3.4", Enabled: 1},
	}
	for _, n := range nodes {
		if err := store.CreateNode(n); err != nil {
			t.Fatal(err)
		}
	}

	// 假的 xray：-test 直接失败。真实现会等 6 秒 socks 端口，这里只走快速失败路径。
	fake := filepath.Join(t.TempDir(), "fake-xray")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'fake: bad config' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	res := SelfTestAll(store, st, fake, nodes, "", 18080, 3*time.Second)
	if len(res) != len(nodes) {
		t.Fatalf("每个节点都该有一条结果: 期望 %d 实际 %d", len(nodes), len(res))
	}
	for i, r := range res {
		if r.Tag != nodes[i].Tag || r.Proto != nodes[i].Proto {
			t.Errorf("第 %d 条结果的节点对不上: %+v", i, r)
		}
		if r.OK || r.Skipped {
			t.Errorf("%s 不该被判为通过: %+v", r.Tag, r)
		}
		if !strings.Contains(r.Err, "没通过 xray 校验") {
			t.Errorf("%s 的错误应说明是配置校验失败，实际 %q", r.Tag, r.Err)
		}
		if !strings.Contains(r.Err, "fake: bad config") {
			t.Errorf("%s 应带上 xray 的原始输出，实际 %q", r.Tag, r.Err)
		}
	}

	// 没有用户可用时：报错而不是当成"不通"
	if err := store.UpdateUser("alice", []string{"enable=0"}); err != nil {
		t.Fatal(err)
	}
	res = SelfTestAll(store, st, fake, nodes[:1], "", 18080, 3*time.Second)
	if len(res) != 1 || !strings.Contains(res[0].Err, "没有可用这个节点的启用用户") {
		t.Errorf("挑不到用户时应如实报出: %+v", res)
	}
}
