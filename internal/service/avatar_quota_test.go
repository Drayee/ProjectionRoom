package service

// 头像频控与管理端重置（票 #75 / 决议 #54 §3）在服务层的证据。
//
// 本文件要钉的是**四条判断**，每一条都能单独被写错：
//
//  1. 额度只在"真的要变"时扣 —— 幂等重设与清除都不吃额度；
//  2. 用满之后回 ErrAvatarQuotaExceeded（→ 429），不是"随便哪个错误"；
//  3. 跨日自动恢复（时间源可注入，所以这条能测）；
//  4. 管理员重置**不扣用户额度**、**不需要对象存储**、且先置空再删对象。

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ProjectionRoom/internal/store"
)

// quotaService 造一个带"可控时钟"的账号服务。
//
// 时钟是**指针共享**的：调用方拿到 clock 之后改它的值，服务里读到的时间
// 立刻跟着变 —— 这样"第二天"只是一个赋值，不需要 sleep 也不需要真实等待。
type testClock struct{ at time.Time }

func (c *testClock) now() time.Time { return c.at }

// quotaFixture 造 (服务, 假 store, 假后端, 用户 id, 时钟)。
func quotaFixture(t *testing.T) (*AccountService, *fakeStore, *fakeAvatarBackend, int64, *testClock) {
	t.Helper()
	f := newFakeStore()
	be := newFakeAvatarBackend()
	clock := &testClock{at: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
	svc := newTestService(t, f, AccountOptions{
		Avatar: be,
		Now:    clock.now,
	})
	id := seedAccount(t, f, "quota-avatar", "abcd1234")
	return svc, f, be, id, clock
}

// TestUpdateMyAvatarURLEnforcesDailyQuota 判"第 6 次被拒"。
//
// 5 次是决议 #54 §3 的数字。这里逐次设**不同的** URL，因为同一个 URL
// 走的是幂等短路（不扣额度）—— 想测限额就必须真的每次都变。
func TestUpdateMyAvatarURLEnforcesDailyQuota(t *testing.T) {
	svc, f, _, id, _ := quotaFixture(t)
	ctx := context.Background()

	for i := 0; i < DefaultAvatarChangesPerDay; i++ {
		url := avatarURLFor(string(rune('a' + i)))
		if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
			t.Fatalf("第 %d 次（共 %d 次）应当通过：%v", i+1, DefaultAvatarChangesPerDay, err)
		}
	}

	overflow := avatarURLFor("z")
	_, err := svc.UpdateMyAvatarURL(ctx, id, &overflow)
	if !errors.Is(err, ErrAvatarQuotaExceeded) {
		t.Fatalf("第 %d 次应当被拒（ErrAvatarQuotaExceeded），实际：%v",
			DefaultAvatarChangesPerDay+1, err)
	}

	// 被拒的那次不能改库：用户看到的头像还是第 5 次设的那张。
	f.mu.Lock()
	stored := f.users[id].AvatarURL
	f.mu.Unlock()
	if stored == nil || *stored != avatarURLFor("e") {
		t.Fatalf("被拒之后库里的头像应停在第 5 张，实际 %v", stored)
	}

	// 额度也确实停在 5（被拒的那次没有占额度）。
	if got := f.avatarQuotaCount(id); got != DefaultAvatarChangesPerDay {
		t.Fatalf("额度应停在 %d，实际 %d", DefaultAvatarChangesPerDay, got)
	}
}

// TestUpdateMyAvatarURLQuotaDoesNotCountIdempotentWrites 判"同一张图重设不扣额度"。
//
// 这条不是便利性：前端每次进入设置页都会重挂组件、网络重试也会重复发同一个
// PATCH。如果它们吃额度，用户"什么都没改"就会莫名其妙地用掉今天的机会。
func TestUpdateMyAvatarURLQuotaDoesNotCountIdempotentWrites(t *testing.T) {
	svc, f, _, id, _ := quotaFixture(t)
	ctx := context.Background()
	url := avatarURLFor("a")

	for i := 0; i < 10; i++ {
		if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
			t.Fatalf("第 %d 次重设同一张图不该失败：%v", i+1, err)
		}
	}
	if got := f.avatarQuotaCount(id); got != 1 {
		t.Fatalf("10 次同值写入只该扣 1 次额度，实际 %d", got)
	}
}

// TestUpdateMyAvatarURLQuotaDoesNotCountClears 判"清除不扣额度"。
//
// 回落到字母头是**降低风险**的方向（违规图的紧急处置正是这条路），
// 给它设计数等于让用户在"想撤掉那张图"和"留到明天"之间选后者 —— 正好反了。
// 与注销冷静期"撤销不要密码"是同一条判断。
func TestUpdateMyAvatarURLQuotaDoesNotCountClears(t *testing.T) {
	svc, f, _, id, _ := quotaFixture(t)
	ctx := context.Background()

	url := avatarURLFor("a")
	if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
		t.Fatalf("设头像：%v", err)
	}
	// 设了一次 + 清一次，再设一次 …… 清除那几步都不该继续吃额度。
	for i := 0; i < DefaultAvatarChangesPerDay+3; i++ {
		if _, err := svc.UpdateMyAvatarURL(ctx, id, nil); err != nil {
			t.Fatalf("第 %d 次清除不该失败：%v", i+1, err)
		}
	}
	if got := f.avatarQuotaCount(id); got != 1 {
		t.Fatalf("清除不该扣额度，期望仍为 1，实际 %d", got)
	}

	// 而且清除之后仍然可以继续用剩下的额度（清除没有把计数弄坏）。
	next := avatarURLFor("b")
	if _, err := svc.UpdateMyAvatarURL(ctx, id, &next); err != nil {
		t.Fatalf("清除之后应当还能改头像：%v", err)
	}
}

// TestUpdateMyAvatarURLQuotaResetsNextDay 判跨日恢复。
//
// 时间源可注入，所以"第二天"只是一个赋值 —— 这也是它必须走 s.now 而不是
// time.Now 的理由：用 time.Now 的实现在本用例里会永远红。
func TestUpdateMyAvatarURLQuotaResetsNextDay(t *testing.T) {
	svc, _, _, id, clock := quotaFixture(t)
	ctx := context.Background()

	for i := 0; i < DefaultAvatarChangesPerDay; i++ {
		url := avatarURLFor(string(rune('a' + i)))
		if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
			t.Fatalf("第 %d 次应当通过：%v", i+1, err)
		}
	}
	full := avatarURLFor("z")
	if _, err := svc.UpdateMyAvatarURL(ctx, id, &full); !errors.Is(err, ErrAvatarQuotaExceeded) {
		t.Fatalf("当天已满，应当被拒：%v", err)
	}

	clock.at = clock.at.AddDate(0, 0, 1)

	if _, err := svc.UpdateMyAvatarURL(ctx, id, &full); err != nil {
		t.Fatalf("第二天应当重新可用：%v", err)
	}
}

// TestUpdateMyAvatarURLQuotaUnlimited 判 AvatarDailyLimit < 0 = 不限额。
//
// 这条路径的存在理由是部署侧的取舍要能被表达（"这个实例先不限"），
// 而不是给测试留后门 —— 所以它也必须仍然记数（见 store 层的同名判据）。
func TestUpdateMyAvatarURLQuotaUnlimited(t *testing.T) {
	f := newFakeStore()
	be := newFakeAvatarBackend()
	svc := newTestService(t, f, AccountOptions{Avatar: be, AvatarDailyLimit: -1})
	id := seedAccount(t, f, "no-limit", "abcd1234")
	ctx := context.Background()

	for i := 0; i < DefaultAvatarChangesPerDay+4; i++ {
		url := avatarURLFor(string(rune('a' + i%26)))
		if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
			t.Fatalf("不限额时第 %d 次应当通过：%v", i+1, err)
		}
	}
}

// TestResetUserAvatarClearsAndDeletes 判管理端重置的完整效果。
//
// 三件事一起看：库里的值没了（所有人视角立刻回字母头）、旧对象进了异步删除、
// 被处置者的额度**没有**被消耗（他明天还要能传正经图）。
func TestResetUserAvatarClearsAndDeletes(t *testing.T) {
	svc, f, be, id, _ := quotaFixture(t)
	ctx := context.Background()
	url := avatarURLFor("a")

	if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
		t.Fatalf("先设一张头像：%v", err)
	}
	before := f.avatarQuotaCount(id)

	adminID := seedAccount(t, f, "reset-admin", "abcd5678")
	if err := svc.ResetUserAvatar(ctx, adminID, id, "10.0.0.1"); err != nil {
		t.Fatalf("重置头像：%v", err)
	}

	f.mu.Lock()
	stored := f.users[id].AvatarURL
	f.mu.Unlock()
	if stored != nil {
		t.Fatalf("重置后库里必须置空（所有人视角立刻回字母头），实际 %v", *stored)
	}

	be.waitRemoved(t, url)

	if after := f.avatarQuotaCount(id); after != before {
		t.Fatalf("管理端重置不该消耗被处置者的额度：before %d after %d", before, after)
	}

	// 审计：动作名要能被事后复盘检索到。
	//
	// 审计走 s.submit（异步单写，I4），所以必须轮询等它落地 ——
	// 直接断言会与后台协程抢时间，偶发假红。
	wantTarget := strconv.FormatInt(id, 10)
	waitUntil(t, "写重置头像审计", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, a := range f.audits {
			if a.Action == "user.avatar.reset" && a.ActorID == adminID && a.TargetID == wantTarget {
				return true
			}
		}
		return false
	})
}

// TestResetUserAvatarWithoutStorage 判"存储未配置时仍然能重置"。
//
// 这是刻意与 UpdateMyAvatarURL 不同的地方：那条路在 s.avatar == nil 时
// 立刻 503（因为没有存储就上传不了，设头像无从谈起）；而重置只写库
// （`avatar_url = NULL`），删对象那一步才需要存储。
//
// 如果这里也 503，症状是"部署没开存储 → 管理员也没法把违规头像下架"，
// 而后者恰恰是更紧急的那件事。
func TestResetUserAvatarWithoutStorage(t *testing.T) {
	f := newFakeStore()
	// 刻意不给 Avatar：模拟 PR_STORAGE_ENDPOINT 为空的部署。
	svc := newTestService(t, f, AccountOptions{})
	id := seedAccount(t, f, "no-store-user", "abcd1234")
	adminID := seedAccount(t, f, "no-store-admin", "abcd5678")
	ctx := context.Background()

	// 直接往库里塞一个 URL（这种部署下用户设不了，但历史数据/手工修的要有）。
	url := avatarURLFor("a")
	f.mu.Lock()
	f.users[id].AvatarURL = &url
	f.mu.Unlock()

	if err := svc.ResetUserAvatar(ctx, adminID, id, ""); err != nil {
		t.Fatalf("没有对象存储时重置仍必须成功（它只写库）：%v", err)
	}

	f.mu.Lock()
	stored := f.users[id].AvatarURL
	f.mu.Unlock()
	if stored != nil {
		t.Fatalf("重置后库里必须置空，实际 %v", *stored)
	}
}

// TestResetUserAvatarIsIdempotent 判"本来就是字母头"时是空操作（但仍写审计）。
//
// 幂等不是洁癖：管理端面板会连点，而"再点一次"应当只多一条审计，
// 不该产生第二次删对象（那个对象已经被删了，第二次必然失败并打一行 WARN）。
func TestResetUserAvatarIsIdempotent(t *testing.T) {
	svc, f, be, id, _ := quotaFixture(t)
	ctx := context.Background()
	adminID := seedAccount(t, f, "reset-admin2", "abcd5678")

	// 第一次：本来就没有头像。
	if err := svc.ResetUserAvatar(ctx, adminID, id, ""); err != nil {
		t.Fatalf("对没有头像的用户重置应当成功：%v", err)
	}
	if be.removedCount() != 0 {
		t.Fatalf("没有头像时不该删任何对象，实际删了 %d 个", be.removedCount())
	}

	// 第二次：设一张再重置两次，第二次重置不该再删。
	url := avatarURLFor("a")
	if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
		t.Fatalf("设头像：%v", err)
	}
	if err := svc.ResetUserAvatar(ctx, adminID, id, ""); err != nil {
		t.Fatalf("重置：%v", err)
	}
	be.waitRemoved(t, url)
	if err := svc.ResetUserAvatar(ctx, adminID, id, ""); err != nil {
		t.Fatalf("重复重置应当成功：%v", err)
	}
	if n := be.removedCount(); n != 1 {
		t.Fatalf("第二次重置不该再删对象，累计删除应为 1，实际 %d", n)
	}
}

// TestResetUserAvatarUnknownUser 判目标不存在时的错误。
//
// 必须是 ErrUserNotFound（→ 404），不能悄悄成功 —— 否则管理端会以为
// "处置成功"，而那条 URL 仍然挂在一个不存在的 id 上（或者根本是打错了 id）。
func TestResetUserAvatarUnknownUser(t *testing.T) {
	svc, f, _, _, _ := quotaFixture(t)
	ctx := context.Background()
	adminID := seedAccount(t, f, "reset-admin3", "abcd5678")

	if err := svc.ResetUserAvatar(ctx, adminID, 999999, ""); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("不存在的用户应当报 ErrUserNotFound，实际：%v", err)
	}
	if err := svc.ResetUserAvatar(ctx, 0, 1, ""); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("actorID=0 应当报 ErrUserNotFound，实际：%v", err)
	}
}

// TestResetUserAvatarDeletesAfterClearing 判"先置空、再删对象"的顺序。
//
// 反过来的顺序有一段真实的坏窗口：图片被删了而库里还指着它 ——
// 那时前端只能靠 onerror 兜底，用户看到的是破图而不是字母头。
// 我们用一个**记录顺序**的后端把这条钉住：Remove 被调用时，
// 库里的值必须已经是 nil。
func TestResetUserAvatarDeletesAfterClearing(t *testing.T) {
	f := newFakeStore()
	order := &orderProbe{store: f, done: make(chan struct{}, 1)}
	svc := newTestService(t, f, AccountOptions{Avatar: order})
	ctx := context.Background()
	id := seedAccount(t, f, "order-user", "abcd1234")
	adminID := seedAccount(t, f, "order-admin", "abcd5678")

	url := avatarURLFor("a")
	if _, err := svc.UpdateMyAvatarURL(ctx, id, &url); err != nil {
		t.Fatalf("设头像：%v", err)
	}
	order.watchID = id

	if err := svc.ResetUserAvatar(ctx, adminID, id, ""); err != nil {
		t.Fatalf("重置：%v", err)
	}
	order.wait(t)
	if order.clearedAtRemove != 1 {
		t.Fatal("Remove 被调用时库里必须已经是空 —— 顺序反了会留下破图")
	}
}

// orderProbe 是一个在后端回调里窥探"库现在是什么"的 AvatarBackend。
type orderProbe struct {
	store   *fakeStore
	watchID int64

	mu              sync.Mutex
	clearedAtRemove int
	done            chan struct{}
}

func (o *orderProbe) OwnsURL(u string) bool {
	return strings.HasPrefix(u, "https://cdn.example.test/pr-files/")
}

func (o *orderProbe) Remove(_ context.Context, _ string) error {
	o.store.mu.Lock()
	stored := o.store.users[o.watchID].AvatarURL
	o.store.mu.Unlock()

	o.mu.Lock()
	if stored == nil {
		o.clearedAtRemove = 1
	}
	hasDone := o.done != nil
	o.mu.Unlock()

	if hasDone {
		select {
		case o.done <- struct{}{}:
		default:
		}
	}
	return nil
}

// wait 等在 done 上的信号；done 为 nil 时不阻塞（调用方忘了准备就退化成"不等"，
// 让断言以"没观察到"的形式失败，而不是让测试挂死）。
func (o *orderProbe) wait(t *testing.T) {
	t.Helper()
	o.mu.Lock()
	if o.done == nil {
		o.mu.Unlock()
		return
	}
	done := o.done
	o.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("等不到异步删除 —— 被重置的对象没有进异步通道")
	}
}

// 保住 store 包的引用（本文件用 store.User 的字段但不需要更多符号）。
var _ = store.ErrNotFound
