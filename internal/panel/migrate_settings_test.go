package panel

import (
	"strings"
	"testing"
)

// "不猜"的核心：类型以代码为准、键一个不挑、对不上就报错。
func TestPlanSettingsMigrationNoGuessing(t *testing.T) {
	cur := Defaults()
	cur.SubBase = "http://new.example:61887"
	cur.LogFile = "/var/lib/polarisx/polarisx.log"

	// 旧版（Python）把 int/bool 存成字符串，还带一堆当前版本没有的键
	old := []byte(`{
	  "sub_base": "http://old.example:50365",
	  "server_cc": "TW",
	  "server_asn": "AS3462",
	  "quota_reset_day": "5",
	  "fail2ban_kick": "true",
	  "traffic_unit_gib": "false",
	  "chain_enable": "True",
	  "chain_port": "50360",
	  "chain_server": "tw.example",
	  "chain_password": "secret",
	  "log_file": "/usr/local/ixpanel/ixpanel.log",
	  "legacy_unknown_key": "x"
	}`)
	plan, out, err := PlanSettingsMigration(old, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Errors) != 0 {
		t.Fatalf("不该有类型错误: %v", plan.Errors)
	}
	// 类型按结构体来：字符串数字 -> int，字符串布尔 -> bool
	if out.QuotaResetDay != 5 {
		t.Errorf("quota_reset_day 应转成 int 5，实际 %d", out.QuotaResetDay)
	}
	if !out.Fail2banKick || out.TrafficUnitGiB {
		t.Errorf("布尔字段转换不对: fail2ban_kick=%v traffic_unit_gib=%v",
			out.Fail2banKick, out.TrafficUnitGiB)
	}
	if !out.ChainEnable || out.ChainPort != 50360 || out.ChainServer != "tw.example" {
		t.Errorf("chain 字段没搬过来: %+v", out)
	}
	// 当前版本不认的键要被**列出来**，不是静默丢掉
	if len(plan.Skipped) != 1 || plan.Skipped[0] != "legacy_unknown_key" {
		t.Errorf("不认的键应列出，实际 %v", plan.Skipped)
	}
	// 指向旧数据目录的路径要显式跳过并给警告
	if out.LogFile != cur.LogFile {
		t.Errorf("指向旧数据目录的 log_file 不该被搬: %q", out.LogFile)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0], "旧数据目录") {
		t.Errorf("应给出跳过 log_file 的警告，实际 %v", plan.Warnings)
	}
	// 值真的变了的才列为改动
	if len(plan.Changed) == 0 {
		t.Error("应有改动项")
	}
	for _, c := range plan.Changed {
		if c.Key == "log_file" || c.Key == "legacy_unknown_key" {
			t.Errorf("跳过的键不该出现在改动里: %s", c.Key)
		}
	}
}

// 类型对不上必须报错并中止，**绝不能静默变成零值**（这正是之前手写迁移的坑）。
func TestPlanSettingsMigrationRejectsBadType(t *testing.T) {
	cur := Defaults()
	old := []byte(`{"quota_reset_day": "不是数字", "server_cc": "HK"}`)
	plan, _, err := PlanSettingsMigration(old, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Errors) != 1 || !strings.Contains(plan.Errors[0], "quota_reset_day") {
		t.Fatalf("非法 int 应报错，实际 %v", plan.Errors)
	}
	// 有错误时 MigrateSettingsFile 不落盘
	if _, _, err := MigrateSettingsFile("/nonexistent", false, nil); err == nil {
		t.Error("文件不存在应报错")
	}
}

// 空文件/坏 JSON 也要报错，而不是当成"没有设置"。
func TestPlanSettingsMigrationBadInput(t *testing.T) {
	cur := Defaults()
	if _, _, err := PlanSettingsMigration([]byte(`{`), cur); err == nil {
		t.Error("坏 JSON 应报错")
	}
	if _, _, err := PlanSettingsMigration([]byte(`{}`), cur); err == nil {
		t.Error("空对象应报错")
	}
}

// --except/keep：已经有意改过的键不该被迁移覆盖回去。
func TestPlanSettingsMigrationKeep(t *testing.T) {
	cur := Defaults()
	cur.SubTitle = "定制·{cc}·{asn}" // 已按新命名改过
	old := []byte(`{"sub_title": "ix专线·{cc}·{asn}", "server_cc": "TW"}`)
	plan, out, err := PlanSettingsMigrationExcept(old, cur, []string{"sub_title"})
	if err != nil {
		t.Fatal(err)
	}
	if out.SubTitle != "定制·{cc}·{asn}" {
		t.Errorf("keep 的键不该被覆盖，实际 %q", out.SubTitle)
	}
	for _, c := range plan.Changed {
		if c.Key == "sub_title" {
			t.Error("keep 的键不该出现在改动列表里")
		}
	}
	if out.ServerCC != "TW" {
		t.Errorf("没 keep 的键仍应迁移，实际 %q", out.ServerCC)
	}
}

// `polarisx set` 的底层：按字段类型写值。之前 int/bool 字段一个都改不了
// （值被当字符串塞进去，靠已删除的容错解析兜底）。
func TestSetSettingByFieldType(t *testing.T) {
	st := Defaults()
	if err := SetSetting(&st, "quota_reset_day", "1"); err != nil {
		t.Fatalf("int 字段应能写: %v", err)
	}
	if st.QuotaResetDay != 1 {
		t.Errorf("quota_reset_day 应为 1，实际 %d", st.QuotaResetDay)
	}
	if err := SetSetting(&st, "fail2ban_kick", "false"); err != nil {
		t.Fatalf("bool 字段应能写: %v", err)
	}
	if st.Fail2banKick {
		t.Error("fail2ban_kick 应为 false")
	}
	if err := SetSetting(&st, "server_cc", "HK"); err != nil {
		t.Fatalf("string 字段应能写: %v", err)
	}
	if st.ServerCC != "HK" {
		t.Errorf("server_cc 应为 HK，实际 %q", st.ServerCC)
	}
	// 未知键与非法值都要报错，且不改动结构体
	before := st
	if err := SetSetting(&st, "nope_not_a_key", "1"); err == nil {
		t.Error("未知键应报错")
	}
	if err := SetSetting(&st, "quota_reset_day", "不是数字"); err == nil {
		t.Error("非法 int 应报错")
	}
	if st != before {
		t.Error("报错时不该改动任何字段")
	}
	if got := SettingValue(st, "quota_reset_day"); got != "1" {
		t.Errorf("SettingValue 应回显 1，实际 %q", got)
	}
}
