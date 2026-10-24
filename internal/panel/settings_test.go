package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsRejectsInvalidTypes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	raw := `{
		  "quota_reset_day": "5",
	  "sub_title": "定制·{cc}·{asn}",
	  "clash_rules": "myclash"
	}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(); err == nil {
		t.Fatal("字符串数值应被拒绝，请使用 JSON 数字")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLARISX_HOME", dir)
	st := Defaults()
	st.QuotaResetDay = 5
	st.ChainPort = 8388
	if err := SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back["quota_reset_day"].(float64); !ok {
		t.Errorf("quota_reset_day 应为数字，实际 %T", back["quota_reset_day"])
	}
	if _, err := LoadSettings(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultHome(t *testing.T) {
	t.Setenv("POLARISX_HOME", "")
	if got := Home(); got != "/var/lib/polarisx" {
		t.Fatalf("默认数据目录 = %q，期望 /var/lib/polarisx", got)
	}
	if got := Defaults().LogFile; got != "/var/lib/polarisx/polarisx.log" {
		t.Fatalf("默认日志路径 = %q", got)
	}
}
