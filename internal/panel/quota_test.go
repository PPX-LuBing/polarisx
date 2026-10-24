package panel

import (
	"testing"
	"time"
)

// 从旧版迁移过来的库没有 last_reset：第一次采集**不能**把已有用量清零。
// 这是真踩过的坑（迁移 TW 后跑 collect，3.25GB 用量被抹掉）。
func TestAutoResetDoesNotWipeMigratedUsage(t *testing.T) {
	store, st := selftestStore(t)
	st.QuotaResetDay = 1
	if _, err := store.CreateUser("migrated", 1024, "", "", nil); err != nil {
		t.Fatal(err)
	}
	// 造出"迁移过来"的样子：有真实用量、last_reset 为空
	if _, err := store.DB().Exec(
		"UPDATE users SET used_up=?, used_down=?, last_reset='' WHERE name=?",
		int64(800*1024*1024), int64(2400*1024*1024), "migrated"); err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)

	n, err := store.AutoResetMonthly(st, today)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("迁移过来的启用用户不该被重置，实际重置了 %d 人", n)
	}
	u, err := store.UserByName("migrated")
	if err != nil {
		t.Fatal(err)
	}
	if u.UsedUp == 0 && u.UsedDown == 0 {
		t.Fatal("用量被清零了——这正是要修的 bug")
	}
	if u.UsedUp != 800*1024*1024 || u.UsedDown != 2400*1024*1024 {
		t.Errorf("用量应原样保留: ↑%d ↓%d", u.UsedUp, u.UsedDown)
	}
	// 但要记下本周期，保证下个月才会真正重置（幂等）
	if u.LastReset != "2026-03-01" {
		t.Errorf("应记下本周期重置日，实际 %q", u.LastReset)
	}
	again, err := store.AutoResetMonthly(st, today)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("第二次调用应无操作，实际 %d", again)
	}
	// 下个月的重置日照常生效
	next := time.Date(2026, 4, 2, 12, 0, 0, 0, time.Local)
	n, err = store.AutoResetMonthly(st, next)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("下个周期应重置 1 人，实际 %d", n)
	}
	u, _ = store.UserByName("migrated")
	if u.UsedUp != 0 || u.UsedDown != 0 {
		t.Errorf("新周期应清零: ↑%d ↓%d", u.UsedUp, u.UsedDown)
	}
}

// 但"被超额停用 + 没有重置记录"的用户仍要在新周期恢复（旧版语义，不能一起改掉）。
func TestAutoResetStillRecoversDisabledUser(t *testing.T) {
	store, st := selftestStore(t)
	st.QuotaResetDay = 1
	if _, err := store.CreateUser("over", 1, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(
		"UPDATE users SET used_up=?, used_down=0, last_reset='', enable=0, disabled_reason='流量超额' WHERE name=?",
		int64(5*1024*1024*1024), "over"); err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)
	n, err := store.AutoResetMonthly(st, today)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("被停用的用户应在新周期恢复并重置，实际重置 %d 人", n)
	}
	u, _ := store.UserByName("over")
	if u.Enable != 1 {
		t.Error("被停用的用户应被重新启用")
	}
	if u.UsedUp != 0 {
		t.Errorf("重置后用量应为 0，实际 %d", u.UsedUp)
	}
}
