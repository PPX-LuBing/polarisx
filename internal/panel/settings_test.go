package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 生产机上 ixpanel set 会把 int/bool 存成字符串（实测 quota_reset_day="1"），
// 容错解析必须能吃下去，否则换二进制就起不来。
func TestSettingsTolerantTypes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	raw := `{
	  "quota_reset_day": "5",
	  "chain_port": "8388",
	  "chain_enable": "true",
	  "warp_enable": "0",
	  "traffic_unit_gib": "no",
	  "verify_config": "1",
	  "sub_title": "定制·{cc}·{asn}",
	  "clash_rules": "myclash"
	}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadSettings()
	if err != nil {
		t.Fatalf("容错解析失败: %v", err)
	}
	if st.QuotaResetDay != 5 {
		t.Errorf("quota_reset_day = %d，期望 5", st.QuotaResetDay)
	}
	if st.ChainPort != 8388 {
		t.Errorf("chain_port = %d，期望 8388", st.ChainPort)
	}
	if !st.ChainEnable {
		t.Error("chain_enable 应为 true")
	}
	if st.WarpEnable {
		t.Error(`warp_enable="0" 应为 false`)
	}
	if st.TrafficUnitGiB {
		t.Error(`traffic_unit_gib="no" 应为 false`)
	}
	if !st.VerifyConfig {
		t.Error(`verify_config="1" 应为 true`)
	}
	// 没写的键保留默认值
	if st.SubListen != Defaults().SubListen {
		t.Errorf("缺的键应保留默认值，实际 %q", st.SubListen)
	}
	// 写回后必须是规范类型（Python 版还能正常读）
	if err := SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back["quota_reset_day"].(float64); !ok {
		t.Errorf("写回后 quota_reset_day 应为数字，实际 %T", back["quota_reset_day"])
	}
	if _, ok := back["chain_enable"].(bool); !ok {
		t.Errorf("写回后 chain_enable 应为布尔，实际 %T", back["chain_enable"])
	}
	// 重新加载一遍确认往返一致
	st2, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st2.QuotaResetDay != 5 || !st2.ChainEnable || st2.TrafficUnitGiB {
		t.Errorf("往返后不一致: %+v", st2)
	}
}
