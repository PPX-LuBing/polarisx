package panel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// SniCandidates 是 REALITY 伪装目标的候选池。
// 挑选原则：真实企业站（有独立源站、不会被 CDN 前置）、TLS1.3 + h2 + X25519、
// 且**避开** apple/microsoft/google/amazon/cloudflare 这类被用烂的域名
// （用烂的 SNI 反而是特征）。
var SniCandidates = strings.Fields(`
www.amd.com www.intel.com www.ti.com www.analog.com www.microchip.com www.nxp.com
www.infineon.com www.micron.com www.westerndigital.com www.seagate.com www.kingston.com
www.crucial.com www.dell.com www.hpe.com www.supermicro.com www.lenovo.com www.asus.com
www.msi.com www.gigabyte.com www.logitech.com www.corsair.com www.razer.com www.sony.com
www.lg.com www.panasonic.com www.siemens.com www.bosch.com www.honeywell.com www.ge.com
www.caterpillar.com www.deere.com www.boeing.com www.lockheedmartin.com www.gm.com
www.ford.com www.toyota.com www.honda.com www.tesla.com www.ups.com www.fedex.com
www.dhl.com www.maersk.com www.visa.com www.mastercard.com www.americanexpress.com
www.paypal.com www.salesforce.com www.oracle.com www.sap.com www.ibm.com www.cisco.com
www.juniper.net www.paloaltonetworks.com www.fortinet.com www.servicenow.com www.workday.com
www.adobe.com www.autodesk.com www.mathworks.com www.keysight.com www.digikey.com
www.mouser.com www.mcmaster.com www.walmart.com www.target.com www.costco.com
www.homedepot.com www.united.com www.delta.com www.marriott.com www.disney.com
`)

// SniResult 是一个候选的探测结果。
type SniResult struct {
	Host       string  `json:"host"`
	IP         string  `json:"ip"`
	TCPms      float64 `json:"tcp_ms"`
	TLSms      float64 `json:"tls_ms"`
	Totalms    float64 `json:"total_ms"`
	TLS13      bool    `json:"tls13"`
	H2         bool    `json:"h2"`
	X25519     bool    `json:"x25519"`
	Group      string  `json:"group"`
	Cloudflare bool    `json:"cloudflare"`
	Err        string  `json:"err,omitempty"`
	Probes     int     `json:"probes"`
}

// Usable 是否满足 REALITY 的要求（TLS1.3 + h2 + X25519，且不是 CF 前置）。
func (r SniResult) Usable() bool {
	return r.Err == "" && r.TLS13 && r.H2 && r.X25519 && !r.Cloudflare
}

// SniProbeFunc 探测一个域名（可注入，便于测试）。
type SniProbeFunc func(ctx context.Context, host string) SniResult

// BenchSNI 并发探测所有候选，返回**全部**结果（未过滤，便于报告为什么落选）。
func BenchSNI(ctx context.Context, hosts []string, probe SniProbeFunc, concurrency int) []SniResult {
	if concurrency < 1 {
		concurrency = 8
	}
	out := make([]SniResult, len(hosts))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = probe(ctx, h)
		}(i, h)
	}
	wg.Wait()
	return out
}

// RankSNI 过滤出可用候选并按总延迟升序排列。
func RankSNI(results []SniResult) []SniResult {
	var good []SniResult
	for _, r := range results {
		if r.Usable() {
			good = append(good, r)
		}
	}
	sort.SliceStable(good, func(i, j int) bool {
		if good[i].Totalms != good[j].Totalms {
			return good[i].Totalms < good[j].Totalms
		}
		return good[i].Host < good[j].Host
	})
	return good
}

// ProbeTLS 实测一个域名：解析 → 排除 CF → 3 次 TLS1.3 握手取中位数。
// 全程标准库：TLS1.3 / ALPN h2 / 密钥交换 X25519 都能从 ConnectionState 读到，
// 不需要 openssl 子进程。
func ProbeTLS(ctx context.Context, host string, timeout time.Duration, cf *CFRanges) SniResult {
	res := SniResult{Host: host}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		res.Err = "解析失败"
		return res
	}
	res.IP = ips[0].String()
	if cf != nil && cf.Contains(ips[0]) {
		res.Cloudflare = true
		res.Err = "被 Cloudflare 前置（REALITY 不适合）"
		return res
	}
	var tcpS, tlsS []float64
	for i := 0; i < 3; i++ {
		d := &net.Dialer{Timeout: timeout}
		t0 := time.Now()
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(res.IP, "443"))
		if err != nil {
			res.Err = "TCP 连接失败"
			return res
		}
		t1 := time.Now()
		tc := tls.Client(conn, &tls.Config{
			ServerName: host, MinVersion: tls.VersionTLS13,
			NextProtos: []string{"h2", "http/1.1"}, InsecureSkipVerify: true,
		})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			res.Err = "TLS 握手失败"
			return res
		}
		t2 := time.Now()
		st := tc.ConnectionState()
		res.TLS13 = st.Version == tls.VersionTLS13
		res.H2 = st.NegotiatedProtocol == "h2"
		// REALITY 需要目标能协商 X25519。注意现在很多服务器默认选的是
		// X25519MLKEM768（后量子混合组，同样基于 X25519），只判 == X25519 会把
		// 绝大多数合格目标误杀（实测 71 个只剩 1 个）。
		res.X25519 = st.CurveID == tls.X25519 || st.CurveID == tls.X25519MLKEM768
		res.Group = curveName(st.CurveID)
		tc.Close()
		tcpS = append(tcpS, float64(t1.Sub(t0).Microseconds())/1000)
		tlsS = append(tlsS, float64(t2.Sub(t1).Microseconds())/1000)
	}
	res.Probes = len(tcpS)
	res.TCPms = median(tcpS)
	res.TLSms = median(tlsS)
	res.Totalms = res.TCPms + res.TLSms
	return res
}

// curveName 把协商到的密钥交换组转成可读名字。
func curveName(id tls.CurveID) string {
	switch id {
	case tls.X25519:
		return "X25519"
	case tls.X25519MLKEM768:
		return "X25519MLKEM768"
	case tls.CurveP256:
		return "P256"
	case tls.CurveP384:
		return "P384"
	case tls.CurveP521:
		return "P521"
	}
	return "unknown"
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// CFRanges 是 Cloudflare 的网段（从官方 API 取，取不到就退回内置的常用段）。
type CFRanges struct{ nets []*net.IPNet }

// Contains 判断 IP 是否在 CF 网段内。
func (c *CFRanges) Contains(ip net.IP) bool {
	if c == nil {
		return false
	}
	for _, n := range c.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Len 网段条数。
func (c *CFRanges) Len() int {
	if c == nil {
		return 0
	}
	return len(c.nets)
}

// fallbackCFRanges 取不到官方列表时用的常用段。
var fallbackCFRanges = []string{
	"104.16.0.0/13", "172.64.0.0/13", "173.245.48.0/20", "103.21.244.0/22",
	"103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
	"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
	"162.158.0.0/15", "131.0.72.0/22",
}

// LoadCFRanges 取 Cloudflare 官方网段列表（无需鉴权），失败则用内置常用段。
func LoadCFRanges(ctx context.Context, timeout time.Duration) *CFRanges {
	out := &CFRanges{}
	add := func(cidrs []string) {
		for _, c := range cidrs {
			if _, n, err := net.ParseCIDR(c); err == nil {
				out.nets = append(out.nets, n)
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.cloudflare.com/client/v4/ips", nil)
	if err == nil {
		cl := &http.Client{Timeout: timeout}
		if resp, err := cl.Do(req); err == nil {
			defer resp.Body.Close()
			var d struct {
				Result struct {
					IPv4 []string `json:"ipv4_cidrs"`
					IPv6 []string `json:"ipv6_cidrs"`
				} `json:"result"`
			}
			if json.NewDecoder(resp.Body).Decode(&d) == nil {
				add(d.Result.IPv4)
				add(d.Result.IPv6)
			}
		}
	}
	if len(out.nets) == 0 {
		add(fallbackCFRanges)
	}
	return out
}

// SniNodeUpdates 把「最佳 SNI」转成节点字段更新（reality 要连 dest 一起换，
// 否则 REALITY 偷的握手目标与 serverName 不一致，客户端会连不上）。
func SniNodeUpdates(n Node, best SniResult, destPort int) []string {
	updates := []string{"sni=" + best.Host}
	if n.Security == "reality" {
		updates = append(updates, fmt.Sprintf("dest=%s", net.JoinHostPort(best.Host,
			fmt.Sprint(destPort))))
	}
	return updates
}

// BestSNI 探测候选并返回最佳的一个（供 node add --sni auto 与 sni --apply 用）。
func BestSNI(ctx context.Context, hosts []string, probe SniProbeFunc, concurrency int) (SniResult, []SniResult, error) {
	all := BenchSNI(ctx, hosts, probe, concurrency)
	good := RankSNI(all)
	if len(good) == 0 {
		return SniResult{}, all, fmt.Errorf("没有可用候选（TLS1.3 + h2 + X25519 全过且未被 Cloudflare 前置）")
	}
	return good[0], all, nil
}
