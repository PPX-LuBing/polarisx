package panel

import (
	"path/filepath"
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
)

// 渲染是这一块唯一由我们自己写的逻辑，必须钉住：
// 把字符画解析回矩阵，应当与库给出的位图逐位一致（含静默区留白）。
func TestRenderQRMatchesLibraryBitmap(t *testing.T) {
	texts := []string{
		"http://0.0.0.0:8080/sub/tok-alice/clash",
		"vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?security=reality#n1",
		"HELLO",
	}
	for _, text := range texts {
		art, err := RenderQR(text)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		mine := trimBlank(parseQRArt(t, art))
		lib := trimBlank(libraryBitmap(t, text))
		if len(mine) != len(lib) || len(mine[0]) != len(lib[0]) {
			t.Fatalf("尺寸不一致: 渲染 %dx%d，库 %dx%d", len(mine), len(mine[0]), len(lib), len(lib[0]))
		}
		for y := range lib {
			for x := range lib[y] {
				if mine[y][x] != lib[y][x] {
					t.Fatalf("%s 在 (%d,%d) 不一致", text, x, y)
				}
			}
		}
	}
}

// 静默区必须是留白：画成黑框会让最外圈模块与边框糊在一起，扫码器分不清边界。
func TestRenderQRQuietZoneIsBlank(t *testing.T) {
	art, err := RenderQR("https://example.com/sub/abc/clash")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(art, "\n"), "\n")
	if len(lines) < 8 {
		t.Fatalf("输出太短: %d 行", len(lines))
	}
	for i := 0; i < 2; i++ { // 上下各 2 行（半块字符 → 4 模块）
		if strings.TrimSpace(lines[i]) != "" {
			t.Errorf("顶部静默区第 %d 行不是留白: %q", i, lines[i])
		}
		if strings.TrimSpace(lines[len(lines)-1-i]) != "" {
			t.Errorf("底部静默区第 %d 行不是留白: %q", i, lines[len(lines)-1-i])
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "█") || strings.HasPrefix(l, "▀") || strings.HasPrefix(l, "▄") {
			t.Errorf("左侧静默区不是留白: %q", l)
			break
		}
	}
}

// QRForUser 的后缀规则：格式必须写进码里，否则 Clash 系客户端扫码后会把 base64 当 YAML 解析。
func TestQRForUser(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	store, err := Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st := Defaults()
	st.SubBase = "https://sub.example.com"
	if err := store.CreateNode(Node{Tag: "n1", Proto: "vless", Net: "tcp",
		Security: "reality", Port: 443, EntryHost: "1.2.3.4", SNI: "s.example.com",
		PubKey: "PUB", ShortID: "ab"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.CreateUser("alice", 10, "", "", []string{"n1"})
	if err != nil {
		t.Fatal(err)
	}

	// 订阅：带格式后缀
	items, err := store.QRForUser(u, st, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(items[0], "/sub/"+u.Token+"/clash") {
		t.Errorf("--clash 应给出带 /clash 的订阅地址，实际 %v", items)
	}
	// 裸订阅
	items, _ = store.QRForUser(u, st, true, false, false)
	if !strings.Contains(items[0], "/sub/"+u.Token) || strings.Contains(items[0], "/clash") {
		t.Errorf("裸订阅不该带后缀，实际 %v", items)
	}
	// 两个格式不能同时要
	if _, err := store.QRForUser(u, st, false, true, true); err == nil {
		t.Error("--clash 与 --singbox 同时给应报错")
	}
	// 节点链接：每个启用节点一张
	items, err = store.QRForUser(u, st, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(items[0], "vless://") {
		t.Errorf("默认应给每个节点一张链接码，实际 %v", items)
	}
	// 绑定为空 = 全部节点（与 Python 版一致），所以"没节点"只发生在没有启用节点时
	if _, err := store.CreateUser("bob", 10, "", "", nil); err != nil {
		t.Fatal(err)
	}
	ub, _ := store.UserByName("bob")
	if items, err := store.QRForUser(ub, st, false, false, false); err != nil || len(items) != 1 {
		t.Errorf("绑定为空应等于全部节点: items=%v err=%v", items, err)
	}
	if err := store.UpdateNode("n1", []string{"enabled=0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QRForUser(ub, st, false, false, false); err == nil {
		t.Error("没有启用节点时应报错提示（而不是给空二维码）")
	}
}

// parseQRArt 把半块字符画解析回布尔矩阵（含静默区）。
func parseQRArt(t *testing.T, art string) [][]bool {
	t.Helper()
	var rows [][]bool
	for _, line := range strings.Split(art, "\n") {
		if line == "" {
			continue
		}
		top := make([]bool, 0, len(line))
		bottom := make([]bool, 0, len(line))
		for _, ch := range line {
			top = append(top, ch == '█' || ch == '▀')
			bottom = append(bottom, ch == '█' || ch == '▄')
		}
		rows = append(rows, top, bottom)
	}
	return rows
}

// trimBlank 裁掉四周全白的行/列（静默区与半块字符补齐的那一行）。
func trimBlank(m [][]bool) [][]bool {
	for len(m) > 0 && !anyTrue(m[0]) {
		m = m[1:]
	}
	for len(m) > 0 && !anyTrue(m[len(m)-1]) {
		m = m[:len(m)-1]
	}
	if len(m) == 0 {
		return m
	}
	left, right := len(m[0]), 0
	for _, row := range m {
		for x, v := range row {
			if v {
				if x < left {
					left = x
				}
				if x > right {
					right = x
				}
			}
		}
	}
	out := make([][]bool, len(m))
	for i, row := range m {
		out[i] = row[left : right+1]
	}
	return out
}

func anyTrue(row []bool) bool {
	for _, v := range row {
		if v {
			return true
		}
	}
	return false
}

// libraryBitmap 取库自己的位图（含静默区）。
func libraryBitmap(t *testing.T, text string) [][]bool {
	t.Helper()
	c, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	return c.Bitmap()
}
