package service

import (
	"context"
	"crypto/md5" //nolint:gosec // 只为造历史 MD5 哈希做兼容性用例
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"ProjectionRoom/internal/store"
)

// 注销冷静期（决议 #51 §5 / 票 #73）在服务层的证据。
//
// 服务层这一层要守的是**门**，不是状态机（状态机在 store 层已被钉住）：
//
//   - 发起要凭当前密码，与改密同一条理由（会话被劫持不足以让人注销别人的账号）；
//   - 封禁账号不能走注销（否则封禁成了"数据删除"的快捷方式）；
//   - 撤销**不要**密码（撤销是降低风险的方向，多一道门只会让人不敢撤销）；
//   - 两条路都不清会话、不动 token_version —— 冷静期内必须能登录看横幅。
//
// 最后一条最容易写错，也最容易被"顺手清理"掉，所以单独一条用例钉住。

// deletionFixture 造一个已注册账号（口令 abcd1234）与它的 service。
func deletionFixture(t *testing.T) (*AccountService, *fakeStore, int64) {
	t.Helper()
	f := newFakeStore()
	svc := newTestService(t, f, AccountOptions{})
	id := seedAccount(t, f, "del-user", "abcd1234")
	return svc, f, id
}

// 发起成功：落时间戳，返回的起点就是落库的起点。
func TestRequestAccountDeletionSucceeds(t *testing.T) {
	svc, f, id := deletionFixture(t)
	ctx := context.Background()

	at, err := svc.RequestAccountDeletion(ctx, id, "abcd1234")
	if err != nil {
		t.Fatalf("RequestAccountDeletion: %v", err)
	}
	if at.IsZero() {
		t.Fatal("返回的起点不该是零值")
	}

	f.mu.Lock()
	stored := f.users[id].DeletionRequestedAt
	f.mu.Unlock()
	if stored == nil {
		t.Fatal("deletion_requested_at 应已落值")
	}
	if !stored.Equal(at) {
		t.Fatalf("落库值与返回值不一致：stored=%s ret=%s", stored, at)
	}

	// 状态查询：到期日 = 起点 + 冷静期（newTestService 默认 30 天）。
	st, err := svc.DeletionState(ctx, id)
	if err != nil {
		t.Fatalf("DeletionState: %v", err)
	}
	if !st.Pending() {
		t.Fatal("Pending() 应为真")
	}
	if st.Deadline == nil {
		t.Fatal("到期日不该为空")
	}
	if got, want := st.Deadline.UTC().Truncate(time.Second), at.Add(30*24*time.Hour).UTC().Truncate(time.Second); !got.Equal(want) {
		t.Fatalf("到期日算错：got=%s want=%s", got, want)
	}
}

// 错当前密码：ErrPasswordMismatch，且**什么都没发生**。
//
// 这是"会话被劫持不足以注销别人账号"的实际执行点。断言必须包含"状态没变"——
// 只看错误值的话，一个先落库再报错的实现也能通过。
func TestRequestAccountDeletionRejectsWrongPassword(t *testing.T) {
	svc, f, id := deletionFixture(t)
	ctx := context.Background()

	if _, err := svc.RequestAccountDeletion(ctx, id, "wrong-password"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("错密码应回 ErrPasswordMismatch，实得 %v", err)
	}

	f.mu.Lock()
	stored := f.users[id].DeletionRequestedAt
	f.mu.Unlock()
	if stored != nil {
		t.Fatal("错密码不该把账号置入冷静期")
	}
}

// 封禁账号不能注销自己：否则封禁 = 数据删除的快捷方式。
func TestRequestAccountDeletionRejectsBanned(t *testing.T) {
	svc, f, id := deletionFixture(t)
	ctx := context.Background()

	f.mu.Lock()
	f.users[id].Status = store.StatusBanned
	f.mu.Unlock()

	if _, err := svc.RequestAccountDeletion(ctx, id, "abcd1234"); !errors.Is(err, ErrUserForbidden) {
		t.Fatalf("封禁账号应回 ErrUserForbidden，实得 %v", err)
	}
}

// userID 不合法 / 账号不存在：一律 ErrSessionInvalid（不泄露存在性）。
func TestRequestAccountDeletionRejectsUnknownSession(t *testing.T) {
	svc, _, _ := deletionFixture(t)
	ctx := context.Background()

	for _, id := range []int64{0, -1, 999999} {
		if _, err := svc.RequestAccountDeletion(ctx, id, "abcd1234"); !errors.Is(err, ErrSessionInvalid) {
			t.Fatalf("userID=%d 应回 ErrSessionInvalid，实得 %v", id, err)
		}
	}
}

// 历史 MD5 哈希的账号也能注销（与改密那条路一致：老账号不该被锁死）。
func TestRequestAccountDeletionAcceptsLegacyMD5(t *testing.T) {
	svc, f, id := deletionFixture(t)
	ctx := context.Background()

	const legacyPlain = "legacy1234"
	sum := md5.Sum([]byte(legacyPlain)) //nolint:gosec // 刻意造历史哈希
	f.mu.Lock()
	f.users[id].PasswordHash = hex.EncodeToString(sum[:])
	f.mu.Unlock()

	if _, err := svc.RequestAccountDeletion(ctx, id, legacyPlain); err != nil {
		t.Fatalf("历史 MD5 账号应能注销，实得 %v", err)
	}
}

// 撤销不需要密码，且撤销之后状态查询回到"没有待执行的注销"。
func TestCancelAccountDeletionNeedsNoPassword(t *testing.T) {
	svc, _, id := deletionFixture(t)
	ctx := context.Background()

	if _, err := svc.RequestAccountDeletion(ctx, id, "abcd1234"); err != nil {
		t.Fatalf("发起失败：%v", err)
	}
	if err := svc.CancelAccountDeletion(ctx, id); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}

	st, err := svc.DeletionState(ctx, id)
	if err != nil {
		t.Fatalf("DeletionState: %v", err)
	}
	if st.Pending() {
		t.Fatal("撤销后不该还在冷静期")
	}

	// 再撤一次：明确的"没有待执行的注销"，不是静默成功。
	if err := svc.CancelAccountDeletion(ctx, id); !errors.Is(err, ErrDeletionNotPending) {
		t.Fatalf("重复撤销应回 ErrDeletionNotPending，实得 %v", err)
	}
}

// 发起与撤销都**不**清会话、不动 token_version —— 冷静期内必须能登录看横幅。
//
// 这是本票最容易被"顺手清理"掉的一条：看到"注销"就想顺手把会话全蹬掉。
// 蹬掉的直接后果是用户再也登不进来，横幅上的「撤销」按钮永远点不到，
// 30 天后数据就真的没了 —— 一个误点变成不可挽回的数据丢失。
func TestDeletionKeepsSessionsAndTokenVersion(t *testing.T) {
	svc, f, id := deletionFixture(t)
	ctx := context.Background()

	// 造一枚会话，用来验证它还活着。
	before, err := svc.Login(ctx, "del-user", "abcd1234", "203.0.113.9:1", "test-agent")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	f.mu.Lock()
	tvBefore := f.users[id].TokenVersion
	f.mu.Unlock()

	if _, err := svc.RequestAccountDeletion(ctx, id, "abcd1234"); err != nil {
		t.Fatalf("发起失败：%v", err)
	}
	if err := svc.CancelAccountDeletion(ctx, id); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}

	f.mu.Lock()
	tvAfter := f.users[id].TokenVersion
	f.mu.Unlock()
	if tvAfter != tvBefore {
		t.Fatalf("注销/撤销不该改动 token_version：before=%d after=%d", tvBefore, tvAfter)
	}

	// 那枚 access token 仍然可用（冷静期里要能打开 /me 看横幅）。
	if _, err := svc.VerifyAccessToken(ctx, before.AccessToken); err != nil {
		t.Fatalf("冷静期内 access token 应仍然有效，实得 %v", err)
	}
}

// 注销之后公开档案 404（与封禁合流成同一个 ErrUserNotFound）。
func TestPublicProfileHidesDeletionPending(t *testing.T) {
	svc, _, id := deletionFixture(t)
	ctx := context.Background()

	if _, err := svc.PublicProfile(ctx, id); err != nil {
		t.Fatalf("注销前公开档案应可读：%v", err)
	}
	if _, err := svc.RequestAccountDeletion(ctx, id, "abcd1234"); err != nil {
		t.Fatalf("发起失败：%v", err)
	}
	if _, err := svc.PublicProfile(ctx, id); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("注销后公开档案应回 ErrUserNotFound，实得 %v", err)
	}

	// 撤销后回来。
	if err := svc.CancelAccountDeletion(ctx, id); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	if _, err := svc.PublicProfile(ctx, id); err != nil {
		t.Fatalf("撤销后公开档案应重新可读：%v", err)
	}
}
