package panel

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// 候选池的卫生检查：不能有空项/重复，也不能混进被用烂的 SNI。
func TestSniCandidatesSanity(t *testing.T) {
	if len(SniCandidates) < 30 {
		t.Fatalf("候选太少: %d", len(SniCandidates))
	}
	seen := map[string]bool{}
	overused := []string{"apple", "microsoft", "google", "amazon", "cloudflare",
		"icloud", "bing", "yahoo", "github"}
	for _, h := range SniCandidates {
		if seen[h] {
			t.Errorf("候选重复: %s", h)
		}
		seen[h] = true
		if !strings.Contains(h, ".") || strings.ContainsAny(h, " /") {
			t.Errorf("候选格式不对: %q", h)
		}
		for _, bad := range overused {
			if strings.Contains(h, bad) {
				t.Errorf("候选 %s 含被用烂的域名特征 %q（用烂的 SNI 反而是特征）", h, bad)
			}
		}
	}
}

func mkResult(host string, total float64, tls13, h2, x, cf bool, err string) SniResult {
	return SniResult{Host: host, Totalms: total, TLS13: tls13, H2: h2, X25519: x,
		Cloudflare: cf, Err: err}
}

func TestRankSNIFiltersAndSorts(t *testing.T) {
	all := []SniResult{
		mkResult("slow.example.com", 50, true, true, true, false, ""),
		mkResult("fast.example.com", 5, true, true, true, false, ""),
		mkResult("no-tls13.example.com", 1, false, true, true, false, ""),
		mkResult("no-h2.example.com", 1, true, false, true, false, ""),
		mkResult("no-x25519.example.com", 1, true, true, false, false, ""),
		mkResult("cf.example.com", 1, true, true, true, true, ""),
		mkResult("dead.example.com", 1, true, true, true, false, "TCP 连接失败"),
	}
	good := RankSNI(all)
	if len(good) != 2 {
		t.Fatalf("可用候选应为 2 个，实际 %d: %v", len(good), good)
	}
	if good[0].Host != "fast.example.com" || good[1].Host != "slow.example.com" {
		t.Errorf("排序不对: %s, %s", good[0].Host, good[1].Host)
	}
	// 每种落选原因都不能被当成可用（真实 ProbeTLS 会给 CF/失败带上 Err，这里只验不变量）
	for _, r := range all {
		if r.Host != "fast.example.com" && r.Host != "slow.example.com" && r.Usable() {
			t.Errorf("%s 不该判为可用", r.Host)
		}
	}
}

func TestRankSNITieBreaksByHost(t *testing.T) {
	good := RankSNI([]SniResult{
		mkResult("b.example.com", 10, true, true, true, false, ""),
		mkResult("a.example.com", 10, true, true, true, false, ""),
	})
	if good[0].Host != "a.example.com" {
		t.Errorf("同延迟时按域名排序，实际 %s", good[0].Host)
	}
}

// BenchSNI 要并发跑完全部候选，且结果与输入顺序一一对应。
func TestBenchSNIConcurrent(t *testing.T) {
	hosts := []string{"a", "b", "c", "d", "e"}
	var maxConcurrent, cur int
	probe := func(ctx context.Context, host string) SniResult {
		cur++
		if cur > maxConcurrent {
			maxConcurrent = cur
		}
		time.Sleep(20 * time.Millisecond)
		cur--
		return mkResult(host, 1, true, true, true, false, "")
	}
	got := BenchSNI(context.Background(), hosts, probe, 3)
	if len(got) != len(hosts) {
		t.Fatalf("结果数 %d，期望 %d", len(got), len(hosts))
	}
	for i, h := range hosts {
		if got[i].Host != h {
			t.Errorf("第 %d 个应是 %s，实际 %s", i, h, got[i].Host)
		}
	}
	if maxConcurrent < 2 {
		t.Errorf("并发数没生效（峰值 %d）", maxConcurrent)
	}
	if maxConcurrent > 3 {
		t.Errorf("并发超过上限 3（峰值 %d）", maxConcurrent)
	}
}

func TestBestSNI(t *testing.T) {
	// 有可用候选：取最快
	best, _, err := BestSNI(context.Background(), []string{"a", "b"}, func(_ context.Context, h string) SniResult {
		if h == "a" {
			return mkResult(h, 30, true, true, true, false, "")
		}
		return mkResult(h, 10, true, true, true, false, "")
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if best.Host != "b" {
		t.Errorf("应选 b（更快），实际 %s", best.Host)
	}
	// 全不可用：报错并说明要求
	_, all, err := BestSNI(context.Background(), []string{"x"}, func(_ context.Context, h string) SniResult {
		return mkResult(h, 1, false, true, true, false, "")
	}, 1)
	if err == nil {
		t.Error("没有可用候选时应报错")
	}
	if !strings.Contains(err.Error(), "TLS1.3") {
		t.Errorf("错误信息应说明要求，实际 %v", err)
	}
	if len(all) != 1 {
		t.Errorf("应返回全部结果用于排查，实际 %d", len(all))
	}
}

func TestCFRangesContains(t *testing.T) {
	cf := &CFRanges{}
	for _, c := range fallbackCFRanges {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		cf.nets = append(cf.nets, n)
	}
	cases := map[string]bool{
		"104.16.0.1":    true,
		"172.64.1.1":    true,
		"1.1.1.1":       false,
		"38.102.235.98": false,
	}
	for ip, want := range cases {
		if got := cf.Contains(net.ParseIP(ip)); got != want {
			t.Errorf("Contains(%s) = %v，期望 %v", ip, got, want)
		}
	}
	if cf.Len() == 0 {
		t.Error("网段应非空")
	}
	var nilCF *CFRanges
	if nilCF.Contains(net.ParseIP("1.1.1.1")) {
		t.Error("nil 应安全返回 false")
	}
	if nilCF.Len() != 0 {
		t.Error("nil 的 Len 应为 0")
	}
}

func TestSniNodeUpdates(t *testing.T) {
	best := SniResult{Host: "www.sony.com", IP: "23.210.216.170", Totalms: 3.5}
	// reality：sni 与 dest 必须一起换
	got := SniNodeUpdates(Node{Security: "reality"}, best, 443)
	if len(got) != 2 || got[0] != "sni=www.sony.com" || got[1] != "dest=www.sony.com:443" {
		t.Errorf("reality 的更新不对: %v", got)
	}
	// 非 reality：只换 sni（dest 与 REALITY 无关）
	got = SniNodeUpdates(Node{Security: "tls"}, best, 443)
	if len(got) != 1 || got[0] != "sni=www.sony.com" {
		t.Errorf("tls 的更新不对: %v", got)
	}
	// 自定义端口
	got = SniNodeUpdates(Node{Security: "reality"}, best, 8443)
	if got[1] != "dest=www.sony.com:8443" {
		t.Errorf("端口没生效: %v", got)
	}
}

// ProbeTLS 对必然失败的输入要有可解释的错误（不联网也能跑）。
func TestProbeTLSErrors(t *testing.T) {
	res := ProbeTLS(context.Background(), "this-host-should-not-resolve.invalid",
		2*time.Second, nil)
	if res.Err == "" {
		t.Error("无法解析的域名应带错误原因")
	}
	if res.Usable() {
		t.Error("失败的探测不应判为可用")
	}
}
