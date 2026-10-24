// Package panel 是 PolarisX 的核心域：settings / SQLite 存储 / Xray 配置生成。
//
// 与 Python 版 ixpanel 的关系：共用同一份 settings.json 与 SQLite schema，
// 因此换二进制即可接管，无需迁移数据。所有语义以 Python 版为参考实现。
package panel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// Version 与 Python 版对齐；Go 版接管后按自己的节奏走。
// Version 是面板版本。声明成 var 而不是 const，是为了让发布流程能用
// `-ldflags "-X .../internal/panel.Version=v1.5.0"` 把 git tag 注入进来
// （const 无法被 ldflags 覆盖）。
var Version = "1.5.0"

// SubTitleDefault 订阅标题默认模板；{cc}/{asn} 会在生成时替换。
const SubTitleDefault = "定制·{cc}·{asn}"

// legacySubTitles 老版本默认值，读到就升级成 SubTitleDefault。
var legacySubTitles = []string{"ix专线", "IX专线"}

// Settings 对应 Python 版的 DEFAULT_SETTINGS（45 个键，键名/默认值逐一对齐）。
type Settings struct {
	XrayBin                string `json:"xray_bin"`
	XrayConfig             string `json:"xray_config"`
	XrayAPI                string `json:"xray_api"`
	ServerHost             string `json:"server_host"`
	RestartCmd             string `json:"restart_cmd"`
	SubListen              string `json:"sub_listen"`
	SubBase                string `json:"sub_base"`
	SubPathStyle           string `json:"sub_path_style"`
	OutboundDomainStrategy string `json:"outbound_domain_strategy"`
	DirectPorts            string `json:"direct_ports"`
	ChainEnable            bool   `json:"chain_enable"`
	ChainType              string `json:"chain_type"`
	ChainServer            string `json:"chain_server"`
	ChainPort              int    `json:"chain_port"`
	ChainMethod            string `json:"chain_method"`
	ChainPassword          string `json:"chain_password"`
	ChainDomains           string `json:"chain_domains"`
	WarpEnable             bool   `json:"warp_enable"`
	WarpSocks              string `json:"warp_socks"`
	WarpPrivateKey         string `json:"warp_private_key"`
	WarpAddressV4          string `json:"warp_address_v4"`
	WarpAddressV6          string `json:"warp_address_v6"`
	WarpPeerKey            string `json:"warp_peer_key"`
	WarpEndpoint           string `json:"warp_endpoint"`
	WarpReserved           string `json:"warp_reserved"`
	WarpDomains            string `json:"warp_domains"`
	SingboxDNS             string `json:"singbox_dns"`
	ClashRules             string `json:"clash_rules"`
	SubTitle               string `json:"sub_title"`
	ServerCC               string `json:"server_cc"`
	ServerASN              string `json:"server_asn"`
	ServerOrg              string `json:"server_org"`
	NodeNameWithUser       bool   `json:"node_name_with_user"`
	DefaultQuotaGB         int    `json:"default_quota_gb"`
	QuotaResetDay          int    `json:"quota_reset_day"`
	LogFile                string `json:"log_file"`
	TrafficUnitGiB         bool   `json:"traffic_unit_gib"`
	Fail2banKick           bool   `json:"fail2ban_kick"`
	VerifyConfig           bool   `json:"verify_config"`
	SubTunnel              bool   `json:"sub_tunnel"`
	SubTunnelTarget        string `json:"sub_tunnel_target"`
	SubTunnelMetrics       string `json:"sub_tunnel_metrics"`
	SubTunnelHost          string `json:"sub_tunnel_host"`
	SubTunnelPrevBase      string `json:"sub_tunnel_prev_base"`
	CloudflaredBin         string `json:"cloudflared_bin"`
	// AlertWebhook 是 Go 版新增（Python 版没有）：用量预警/停用时 POST 到这里。
	// 留空 = 不告警。设置里没有这个键时零值就是空，不影响读 Python 版写的设置。
	AlertWebhook string `json:"alert_webhook"`
}

// Defaults 返回与 Python 版逐字一致的默认设置。
func Defaults() Settings {
	home := os.Getenv("POLARISX_HOME")
	if home == "" {
		home = os.Getenv("IXPANEL_HOME") // 兼容旧环境变量，便于平滑接管
	}
	if home == "" {
		home = "/usr/local/ixpanel"
	}
	return Settings{
		XrayConfig:             "/usr/local/etc/xray/config.json",
		XrayAPI:                "127.0.0.1:10085",
		RestartCmd:             "systemctl restart xray",
		SubListen:              "0.0.0.0:8080",
		SubPathStyle:           "path",
		OutboundDomainStrategy: "UseIPv4",
		DirectPorts:            "22",
		ChainType:              "shadowsocks",
		ChainMethod:            "2022-blake3-aes-128-gcm",
		ChainDomains:           "openai.com,chatgpt.com,oaistatic.com,oaiusercontent.com,sora.com,livekit.cloud,anthropic.com,claude.ai,google-gemini.com,perplexity.ai,huggingface.co",
		WarpSocks:              "127.0.0.1:40000",
		WarpAddressV4:          "172.16.0.2",
		WarpPeerKey:            "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
		WarpEndpoint:           "engage.cloudflareclient.com:2408",
		WarpDomains:            "openai.com,chatgpt.com,oaistatic.com,oaiusercontent.com,sora.com,livekit.cloud",
		SingboxDNS:             "doh",
		ClashRules:             "myclash",
		SubTitle:               SubTitleDefault,
		DefaultQuotaGB:         100,
		LogFile:                filepath.Join(home, "ixpanel.log"),
		TrafficUnitGiB:         true,
		Fail2banKick:           true,
		VerifyConfig:           true,
		SubTunnelTarget:        "http://127.0.0.1:8080",
		SubTunnelMetrics:       "127.0.0.1:19090",
	}
}

// Home 返回数据目录（settings.json / panel.db 所在）。
func Home() string {
	if h := os.Getenv("POLARISX_HOME"); h != "" {
		return h
	}
	if h := os.Getenv("IXPANEL_HOME"); h != "" {
		return h
	}
	return "/usr/local/ixpanel"
}

// SettingsPath 返回 settings.json 路径。
func SettingsPath() string { return filepath.Join(Home(), "settings.json") }

// DBPath 返回 panel.db 路径。
func DBPath() string { return filepath.Join(Home(), "panel.db") }

// settingsKinds 由结构体标签反射出来（key -> 字段类型），用来做类型容错，
// 新增字段不用改这里，不会漏。
var settingsKinds = func() map[string]reflect.Kind {
	m := map[string]reflect.Kind{}
	t := reflect.TypeOf(Settings{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name != "" {
			m[name] = f.Type.Kind()
		}
	}
	return m
}()

// UnmarshalJSON 容错解析：Python 版 ixpanel set 会把 int/bool 存成字符串
// （只有 default_quota_gb 和几个布尔键会转换），所以 "1" 与 1 都要能读。
// 这是信任边界上的必要兼容，不做的话换二进制就起不来。
func (s *Settings) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		str, ok := v.(string)
		if !ok {
			continue
		}
		switch settingsKinds[k] {
		case reflect.Int:
			n, err := strconv.Atoi(strings.TrimSpace(str))
			if err != nil {
				n = 0
			}
			raw[k] = n
		case reflect.Bool:
			raw[k] = truthy(str)
		}
	}
	fixed, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	type alias Settings
	return json.Unmarshal(fixed, (*alias)(s))
}

// truthy 与 Python 版 cmd_set 的判定保持一致。
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// LoadSettings 读 settings.json；文件不存在则返回默认值。缺的键保留默认值（与 Python 版一致）。
func LoadSettings() (Settings, error) {
	s := Defaults()
	b, err := os.ReadFile(SettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Defaults(), fmt.Errorf("settings.json 解析失败: %w", err)
	}
	upgradeLegacy(&s)
	return s, nil
}

// upgradeLegacy 把老默认值升级成新的（Python 版同样逻辑）。
func upgradeLegacy(s *Settings) {
	for _, old := range legacySubTitles {
		if s.SubTitle == old {
			s.SubTitle = SubTitleDefault
		}
	}
}

// SaveSettings 原子写回 settings.json（0600，里面有密钥）。
func SaveSettings(s Settings) error {
	if err := os.MkdirAll(Home(), 0o755); err != nil {
		return err
	}
	b, err := marshalNoEscape(s, " ")
	if err != nil {
		return err
	}
	tmp := SettingsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, SettingsPath())
}

// marshalNoEscape 与 Python 的 json.dump(..., indent=N, ensure_ascii=False) 对齐：
// 不转义 HTML、保留非 ASCII 原样。
func marshalNoEscape(v any, indent string) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

// MarshalIndent 按 Python 的 json.dumps(indent=2, ensure_ascii=False) 风格序列化
// （不转义 HTML、保留非 ASCII、末尾带换行）。
func MarshalIndent(v any) (string, error) {
	b, err := marshalNoEscape(v, "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CSV 把逗号分隔的设置项切成列表（空项丢弃，兼容中文逗号）。
func CSV(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(strings.ReplaceAll(s, "，", ","), func(r rune) bool {
		return r == ','
	}) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
