package panel

import (
	"testing"
	"time"
)

// TestTaskAccountLockSameAccountExclusive 同一账号的任务锁互斥：第二次 tryLock 必须失败，
// 解锁后可再次获取。这是「重复点一键完成不并发重跑」的核心保障。
func TestTaskAccountLockSameAccountExclusive(t *testing.T) {
	p := &Panel{}
	uid := "u1"

	if !p.tryLockAccount(uid) {
		t.Fatal("首次加锁应成功")
	}
	if p.tryLockAccount(uid) {
		t.Fatal("同账号第二次加锁应失败（互斥）")
	}
	p.unlockAccount(uid)

	if !p.tryLockAccount(uid) {
		t.Fatal("解锁后应可再次加锁")
	}
	p.unlockAccount(uid)
}

// TestTaskAccountLockDifferentAccountsIndependent 不同账号的锁互不影响（并行照旧）。
func TestTaskAccountLockDifferentAccountsIndependent(t *testing.T) {
	p := &Panel{}
	if !p.tryLockAccount("u1") {
		t.Fatal("u1 加锁应成功")
	}
	if !p.tryLockAccount("u2") {
		t.Fatal("u2 加锁应成功（不同账号不互斥）")
	}
	p.unlockAccount("u2")
	p.unlockAccount("u1")
}

// TestTaskAccountLockCrossEntryShared 单任务 auto 与全量 auto_all 共用同一把账号锁
// （在 handler 层都走 tryLockAccount，这里验证锁命名空间一致）。
func TestTaskAccountLockCrossEntryShared(t *testing.T) {
	p := &Panel{}
	if !p.tryLockAccount("u1") {
		t.Fatal("u1 加锁应成功")
	}
	// 模拟全量入口对同一 uid 加锁——必须被挡（否则两入口可并发）。
	if p.tryLockAccount("u1") {
		t.Fatal("同 uid 跨入口加锁应失败（共用锁）")
	}
	p.unlockAccount("u1")
}

// TestTaskExecutionGlobalLock 所有账号共用一个执行闸门；即使账号不同也不能并发。
func TestTaskExecutionGlobalLock(t *testing.T) {
	old := taskActionCooldown
	taskActionCooldown = 0
	t.Cleanup(func() { taskActionCooldown = old })

	p := &Panel{}
	unlock, _, err := p.lockTaskExecution(false)
	if err != nil {
		t.Fatalf("首次获取全局执行闸门: %v", err)
	}
	if _, _, err := p.lockTaskExecution(false); err == nil {
		t.Fatal("全局闸门被占用时第二次执行必须失败")
	}
	unlock()
	unlock2, _, err := p.lockTaskExecution(false)
	if err != nil {
		t.Fatalf("释放后应可再次获取: %v", err)
	}
	unlock2()
}

// TestTaskExecutionCooldown 两次任务执行开始之间必须满足冷却。
func TestTaskExecutionCooldown(t *testing.T) {
	old := taskActionCooldown
	taskActionCooldown = time.Hour
	t.Cleanup(func() { taskActionCooldown = old })

	p := &Panel{}
	unlock, _, err := p.lockTaskExecution(false)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, _, err := p.lockTaskExecution(false); err == nil {
		t.Fatal("冷却期内第二次执行必须失败")
	}
}
