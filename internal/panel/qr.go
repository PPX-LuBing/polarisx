package panel

import (
	"fmt"
	"net/url"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// RenderQR 把文本渲染成终端二维码。
//
// 用半块字符（▀▄█）两行并一行，所以一张 N×N 的码只占 N/2 行左右；
// 四周留 4 模块静默区，否则很多手机扫不出来。
func RenderQR(text string) (string, error) {
	// 用 skip2/go-qrcode 而不是 rsc.io/qr：后者源码里写着 `// TODO: Pick
	// appropriate mask.`，掩码恒为 0。掩码的作用就是避开会让扫码器困惑的图案，
	// 而这个功能的核心就是拿手机扫，所以必须用会挑掩码的实现。
	code, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return "", err
	}
	bitmap := code.Bitmap() // 含静默区，true=黑
	const quiet = 4
	size := len(bitmap) - quiet*2
	var b strings.Builder
	black := func(x, y int) bool {
		if x < 0 || y < 0 || x >= len(bitmap) || y >= len(bitmap) {
			return false
		}
		return bitmap[y][x]
	}
	for y := 0; y < size+quiet*2; y += 2 {
		for x := 0; x < size+quiet*2; x++ {
			top, bottom := black(x, y), black(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// QRForUser 返回该用户要扫码的内容：
//   - sub=false：每个节点一条分享链接（各打一张）
//   - sub=true：订阅地址；clash/singbox 为真时带上 /clash 或 /singbox 后缀
//
// 后缀必须写进二维码本身：裸订阅地址返回的是 base64，mihomo/Clash 系客户端
// （如 BetterBox）会当成 YAML 解析并直接报 `cannot unmarshal !!str ... into
// config.RawConfig`，而扫码之后没法再手动补后缀。
func (s *Store) QRForUser(u User, st Settings, sub, clash, singbox bool) ([]string, error) {
	if clash && singbox {
		return nil, fmt.Errorf("--clash 与 --singbox 只能选一个")
	}
	if sub || clash || singbox {
		suffix, label := "", ""
		switch {
		case clash:
			suffix, label = "/clash", "订阅地址（Clash/Mihomo YAML）"
		case singbox:
			suffix, label = "/singbox", "订阅地址（sing-box JSON）"
		default:
			label = "订阅地址（base64，老客户端用）"
		}
		return []string{label + "\n" + SubBaseFor(st) + "/sub/" + u.Token + suffix}, nil
	}
	links, err := s.LinksForUser(u, st, true)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("用户 %s 没有可用节点（先 node add 再 user set %s --nodes ...）", u.Name, u.Name)
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		name := ""
		if i := strings.Index(l, "#"); i >= 0 {
			if dec, err := url.PathUnescape(l[i+1:]); err == nil {
				name = dec
			} else {
				name = l[i+1:]
			}
		}
		out = append(out, name+"\n"+l)
	}
	return out, nil
}
