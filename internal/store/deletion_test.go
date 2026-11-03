package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 注销冷静期（决议 #51 §5 / 票 #73）的真库证据。
//
// 这一组用例守两件事：
//
//  1. **状态机本身**：发起是幂等的且**不延期**（否则每刷一次页面就多 30 天，
//     账号永远到不了期）；撤销清标志；到期清扫只删过期的。
//  2. **对外全隐藏**：注销之后，搜索、三份名单、粉丝红点、作者帖列表里
//     都必须看不到这个人；帖子仍然可读，但作者名变成「已注销用户」。
//     这是最容易漏的一半 —— 漏一处的症状是"档案 404 了，但搜索还能搜到"，
//     界面上没有任何报错，只有拿到 id 的人才看得出。
//
// 撤销后一切复原，所以第二条的每个断言都要在撤销后再确认一次。

// deletionEnv 造 alice（要注销的人）+ bob（看客）+ 一篇 alice 的帖。
func deletionEnv(t *testing.T) (s *Store, alice, bob *User, postID int64) {
	t.Helper()
	s = requireTestDB(t)
	ctx := context.Background()

	alice = seedUser(t, s, "del-alice")
	bob = seedUser(t, s, "del-bob")

	p := &Post{AuthorID: alice.ID, Title: "注销前的帖", Content: "正文", Images: []string{}}
	if err := s.CreatePost(ctx, p); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	return s, alice, bob, p.ID
}

// 发起注销：落一列时间戳，DeletionStateOf 算得出到期日。
func TestRequestAccountDeletionStampsAndDeadline(t *testing.T) {
	s, alice, _, _ := deletionEnv(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	got, err := s.RequestAccountDeletion(ctx, alice.ID, at)
	if err != nil {
		t.Fatalf("RequestAccountDeletion: %v", err)
	}
	if !got.Equal(at) {
		t.Fatalf("返回的起点应等于传入时刻：got=%s want=%s", got, at)
	}

	u, err := s.UserByID(ctx, alice.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if u.DeletionRequestedAt == nil {
		t.Fatal("deletion_requested_at 应已落值")
	}
	if !u.DeletionRequestedAt.UTC().Equal(at) {
		t.Fatalf("落库的起点不对：got=%s want=%s", u.DeletionRequestedAt.UTC(), at)
	}

	grace := 30 * 24 * time.Hour
	st := DeletionStateOf(u, grace)
	if !st.Pending() {
		t.Fatal("Pending() 应为真")
	}
	if st.Deadline == nil || !st.Deadline.UTC().Equal(at.Add(grace)) {
		t.Fatalf("到期日算错：got=%v want=%v", st.Deadline, at.Add(grace))
	}
}

// 幂等且**不延期**：第二次发起返回原起点，不把到期日往后推。
//
// 这条是刻意的：用户可能反复点"确认注销"（或者刷新页面时前端又发一次），
// 如果每次都把时间戳覆盖成 now，冷静期就永远不会结束。
func TestRequestAccountDeletionIsIdempotentAndDoesNotExtend(t *testing.T) {
	s, alice, _, _ := deletionEnv(t)
	ctx := context.Background()

	first := time.Now().Add(-10 * 24 * time.Hour).UTC().Truncate(time.Second)
	if _, err := s.RequestAccountDeletion(ctx, alice.ID, first); err != nil {
		t.Fatalf("第一次发起失败：%v", err)
	}

	second := time.Now().UTC().Truncate(time.Second)
	got, err := s.RequestAccountDeletion(ctx, alice.ID, second)
	if err != nil {
		t.Fatalf("第二次发起失败：%v", err)
	}
	if !got.Equal(first) {
		t.Fatalf("第二次发起应返回**原**起点（不延期）：got=%s want=%s", got, first)
	}

	u, err := s.UserByID(ctx, alice.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if u.DeletionRequestedAt == nil || !u.DeletionRequestedAt.UTC().Equal(first) {
		t.Fatalf("落库时间被覆盖了：got=%v want=%s", u.DeletionRequestedAt, first)
	}
}

// 发起对不存在的账号要报 ErrNotFound（而不是静默成功）。
func TestRequestAccountDeletionUnknownUser(t *testing.T) {
	s, _, _, _ := deletionEnv(t)
	ctx := context.Background()

	if _, err := s.RequestAccountDeletion(ctx, 999999999, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的账号应回 ErrNotFound，实得 %v", err)
	}
	if _, err := s.RequestAccountDeletion(ctx, 0, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("userID=0 应回 ErrNotFound，实得 %v", err)
	}
}

// 撤销：清空那一列；再撤销一次报 ErrDeletionNotRequested（不是静默成功）。
func TestCancelAccountDeletion(t *testing.T) {
	s, alice, _, _ := deletionEnv(t)
	ctx := context.Background()

	if _, err := s.RequestAccountDeletion(ctx, alice.ID, time.Now()); err != nil {
		t.Fatalf("发起失败：%v", err)
	}
	if err := s.CancelAccountDeletion(ctx, alice.ID); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}

	u, err := s.UserByID(ctx, alice.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if u.DeletionRequestedAt != nil {
		t.Fatalf("撤销后 deletion_requested_at 应为 NULL，实得 %v", u.DeletionRequestedAt)
	}

	// 再撤一次：没有待执行的注销，必须是一个明确的错误。
	if err := s.CancelAccountDeletion(ctx, alice.ID); !errors.Is(err, ErrDeletionNotRequested) {
		t.Fatalf("重复撤销应回 ErrDeletionNotRequested，实得 %v", err)
	}
	// 不存在的账号：是 ErrNotFound，与上面区分开。
	if err := s.CancelAccountDeletion(ctx, 999999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的账号应回 ErrNotFound，实得 %v", err)
	}
}

// 到期清扫只删过期的，未到期与未申请的都不动；分批 limit 生效。
func TestPurgeExpiredDeletions(t *testing.T) {
	s, alice, bob, _ := deletionEnv(t)
	ctx := context.Background()
	carol := seedUser(t, s, "del-carol")
	dave := seedUser(t, s, "del-dave")

	now := time.Now()
	// alice 与 carol 都已过期（40 天前发起），bob 未申请，dave 刚发起。
	for _, id := range []int64{alice.ID, carol.ID} {
		if _, err := s.RequestAccountDeletion(ctx, id, now.Add(-40*24*time.Hour)); err != nil {
			t.Fatalf("发起失败：%v", err)
		}
	}
	if _, err := s.RequestAccountDeletion(ctx, dave.ID, now); err != nil {
		t.Fatalf("发起失败：%v", err)
	}

	cutoff := now.Add(-30 * 24 * time.Hour)

	n, err := s.CountExpiredDeletions(ctx, cutoff)
	if err != nil {
		t.Fatalf("CountExpiredDeletions: %v", err)
	}
	if n != 2 {
		t.Fatalf("过期数应为 2（alice+carol），实得 %d", n)
	}

	// 先只删一个，验证 limit 真的分批。
	removed, err := s.PurgeExpiredDeletions(ctx, cutoff, 1)
	if err != nil {
		t.Fatalf("PurgeExpiredDeletions: %v", err)
	}
	if removed != 1 {
		t.Fatalf("limit=1 应删 1 个，实得 %d", removed)
	}

	// 把剩下的删干净。
	removed, err = s.PurgeExpiredDeletions(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("PurgeExpiredDeletions: %v", err)
	}
	if removed != 1 {
		t.Fatalf("第二批应删 1 个，实得 %d", removed)
	}

	// alice 与 carol 真的没了。
	for _, id := range []int64{alice.ID, carol.ID} {
		if _, err := s.UserByID(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("过期的账号 %d 应已被删除，实得 err=%v", id, err)
		}
	}
	// bob（没申请）与 dave（刚发起）都还在。
	if _, err := s.UserByID(ctx, bob.ID); err != nil {
		t.Fatalf("未申请的账号不该被删：%v", err)
	}
	if _, err := s.UserByID(ctx, dave.ID); err != nil {
		t.Fatalf("未到期的账号不该被删：%v", err)
	}

	// 清完之后再数一次是 0。
	if n, err := s.CountExpiredDeletions(ctx, cutoff); err != nil || n != 0 {
		t.Fatalf("清完后过期数应为 0，实得 n=%d err=%v", n, err)
	}
}

// 硬删会 CASCADE 带走内容（帖、关注边等），这是"到期彻底删除"的实际含义。
func TestPurgeCascadesAuthoredContent(t *testing.T) {
	s, alice, bob, postID := deletionEnv(t)
	ctx := context.Background()

	// 造一条关注边，验证关系也会随账号消失。
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("CreateFollow: %v", err)
	}

	now := time.Now()
	if _, err := s.RequestAccountDeletion(ctx, alice.ID, now.Add(-40*24*time.Hour)); err != nil {
		t.Fatalf("发起失败：%v", err)
	}
	if _, err := s.PurgeExpiredDeletions(ctx, now.Add(-30*24*time.Hour), 100); err != nil {
		t.Fatalf("PurgeExpiredDeletions: %v", err)
	}

	// 帖子没了（外键 ON DELETE CASCADE）。
	var postCount int64
	if err := s.db.WithContext(ctx).Model(&Post{}).Where("id = ?", postID).Count(&postCount).Error; err != nil {
		t.Fatalf("数帖子失败：%v", err)
	}
	if postCount != 0 {
		t.Fatalf("账号被硬删后帖子应一并消失，实得 %d 条", postCount)
	}

	// 关注边没了。
	var followCount int64
	if err := s.db.WithContext(ctx).Model(&Follow{}).
		Where("follower_id = ? OR followee_id = ?", alice.ID, alice.ID).Count(&followCount).Error; err != nil {
		t.Fatalf("数关注边失败：%v", err)
	}
	if followCount != 0 {
		t.Fatalf("账号被硬删后关注边应一并消失，实得 %d 条", followCount)
	}
}

// 注销期间**对外全隐藏**：搜索、三份名单、粉丝红点、作者帖列表都看不到。
func TestDeletionHidesAccountEverywhere(t *testing.T) {
	s, alice, bob, postID := deletionEnv(t)
	ctx := context.Background()

	// bob 关注 alice，于是 alice 是 bob 的"关注"与"粉丝"两侧都能看到的对象。
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("CreateFollow: %v", err)
	}

	// 注销前：搜得到、名单里有、红点算得到、作者帖列得出来。
	hits, err := s.SearchUsers(ctx, "del-alice", bob.ID)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("注销前应搜到 1 人，实得 %d", len(hits))
	}

	// 发起注销。
	if _, err := s.RequestAccountDeletion(ctx, alice.ID, time.Now()); err != nil {
		t.Fatalf("发起失败：%v", err)
	}

	// ① 搜索：搜不到了。
	hits, err = s.SearchUsers(ctx, "del-alice", bob.ID)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("注销后不该被搜到，实得 %d 人", len(hits))
	}

	// ② 三份名单（bob 视角）：关注/粉丝两侧都没有 alice。
	for _, tab := range []FollowListTab{FollowListFollowing, FollowListFollowers, FollowListMutual} {
		rows, err := s.ListFollowList(ctx, bob.ID, tab)
		if err != nil {
			t.Fatalf("ListFollowList(%s): %v", tab, err)
		}
		for i := range rows {
			if rows[i].UserID == alice.ID {
				t.Fatalf("注销后不该出现在名单 %s 里", tab)
			}
		}
	}

	// ③ 粉丝红点：被注销的**粉丝**不该再点亮别人的红点。
	//
	// 这里的视角要摆正：红点属于"被关注的人"，而可见性谓词过滤的是**粉丝**
	// （`p` = follower）。所以要用第三个人 carol 来判 —— 让 carol 关注 alice、
	// 再注销 carol，alice 的红点必须少一个。拿 alice 自己当被注销方去数她的红点
	// 判不出这件事（她自己是不是可见与她的红点无关）。
	carol := seedUser(t, s, "del-carol")
	if _, _, err := s.CreateFollow(ctx, carol.ID, alice.ID); err != nil {
		t.Fatalf("CreateFollow(carol→alice): %v", err)
	}
	before, err := s.CountNewFollowers(ctx, alice.ID)
	if err != nil {
		t.Fatalf("CountNewFollowers: %v", err)
	}
	if before != 2 {
		t.Fatalf("注销前 alice 应有 2 个新粉丝（bob+carol），实得 %d", before)
	}
	if _, err := s.RequestAccountDeletion(ctx, carol.ID, time.Now()); err != nil {
		t.Fatalf("发起失败：%v", err)
	}
	after, err := s.CountNewFollowers(ctx, alice.ID)
	if err != nil {
		t.Fatalf("CountNewFollowers: %v", err)
	}
	if after != 1 {
		t.Fatalf("注销的粉丝不该再算进红点（应剩 1），实得 %d", after)
	}
	if err := s.CancelAccountDeletion(ctx, carol.ID); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}

	// ④ 作者帖列表（bob 视角）：一篇都不该有。
	rows, _, err := s.ListPostsByAuthor(ctx, alice.ID, 10, 0, bob.ID)
	if err != nil {
		t.Fatalf("ListPostsByAuthor: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("注销后作者帖列表应为空，实得 %d 篇", len(rows))
	}

	// ⑤ 帖子本身仍可读，但作者名是占位名（这是决议 #51 §5 的"帖署名已注销用户"）。
	p, err := s.PostByID(ctx, postID, bob.ID)
	if err != nil {
		t.Fatalf("注销后帖子应仍可读（私聊/帖子历史保留）：%v", err)
	}
	if p.AuthorName != DeletedUserDisplayName {
		t.Fatalf("注销账号的帖子作者名应为 %q，实得 %q", DeletedUserDisplayName, p.AuthorName)
	}

	// ⑥ 撤销之后一切复原。
	if err := s.CancelAccountDeletion(ctx, alice.ID); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	hits, err = s.SearchUsers(ctx, "del-alice", bob.ID)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("撤销后应重新搜到 1 人，实得 %d", len(hits))
	}
	rows, _, err = s.ListPostsByAuthor(ctx, alice.ID, 10, 0, bob.ID)
	if err != nil {
		t.Fatalf("ListPostsByAuthor: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("撤销后作者帖应重新可见，实得 %d 篇", len(rows))
	}
	p, err = s.PostByID(ctx, postID, bob.ID)
	if err != nil {
		t.Fatalf("PostByID: %v", err)
	}
	if p.AuthorName == DeletedUserDisplayName {
		t.Fatal("撤销后作者名不该还是占位名")
	}
}

// 注销期间**不能继续往里写**：给冷静期里的账号发私聊必须失败。
//
// 决议 #51 §5 的措辞是"私聊历史保留给对方（可读不可回）"——
// 前半句已经有 `TestDeletionHidesAccountEverywhere` 之外的用例覆盖（历史仍可读），
// 后半句就落在 SendDM 的收件人可见性判断上。
//
// 为什么这条容易漏：`SendDM` 原来只判"收件人存在"，而注销的账号**确实存在**
// （它只是对外不可见）。漏掉的症状很轻：界面上查无此人，但拿 id 还能发进去，
// 于是会话列表里会冒出一条"发给已注销用户"的新消息，看起来像注销没生效。
func TestSendDMRejectsDeletionPendingRecipient(t *testing.T) {
	s, alice, bob, _ := deletionEnv(t)
	ctx := context.Background()

	// 先互关（私聊门闩是互关），再让 alice 进冷静期。
	if _, _, err := s.CreateFollow(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("alice→bob 关注失败：%v", err)
	}
	if _, _, err := s.CreateFollow(ctx, bob.ID, alice.ID); err != nil {
		t.Fatalf("bob→alice 关注失败：%v", err)
	}
	// 冷静期里发得进去（对照组：证明后面的失败确实来自注销，而不是门闩）。
	if _, err := s.SendDM(ctx, bob.ID, alice.ID, "注销前"); err != nil {
		t.Fatalf("注销前应能发送：%v", err)
	}

	if _, err := s.RequestAccountDeletion(ctx, alice.ID, time.Now()); err != nil {
		t.Fatalf("发起注销失败：%v", err)
	}

	// 与"发给不存在的人"同一个错误（不给"这个 id 存在但注销了"的探测通道）。
	if _, err := s.SendDM(ctx, bob.ID, alice.ID, "注销后"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("给冷静期里的账号发私聊应返回 ErrNotFound：got %v", err)
	}

	// 被拒的消息不得落库。
	var n int64
	if err := s.DB().Model(&Message{}).
		Where("sender_id = ? AND recipient_id = ?", bob.ID, alice.ID).Count(&n).Error; err != nil {
		t.Fatalf("统计消息失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("冷静期内只该有注销前那一条消息：got %d 条", n)
	}

	// 撤销之后又能发了（证明这道门是跟着那一列走的，不是永久封死）。
	if err := s.CancelAccountDeletion(ctx, alice.ID); err != nil {
		t.Fatalf("撤销注销失败：%v", err)
	}
	if _, err := s.SendDM(ctx, bob.ID, alice.ID, "撤销后"); err != nil {
		t.Fatalf("撤销后应能重新发送：%v", err)
	}
}
