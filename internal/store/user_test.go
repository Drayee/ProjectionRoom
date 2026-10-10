package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreateUserAndLookups(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	u := seedUser(t, s, "alice")
	if u.ID == 0 {
		t.Fatalf("Create 之后应该回填自增主键")
	}
	if u.Role != RoleUser || u.Status != StatusActive || u.TokenVersion != 1 {
		t.Fatalf("默认值不符合 §4：role=%q status=%q token_version=%d", u.Role, u.Status, u.TokenVersion)
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatalf("GORM 应回填 created_at/updated_at：created=%s updated=%s", u.CreatedAt, u.UpdatedAt)
	}
	if u.BannedReason != nil || u.Email != nil {
		t.Fatalf("可空列应该是 NULL：banned_reason=%v email=%v", u.BannedReason, u.Email)
	}

	// 查询侧与写入侧共用同一套归一化：传大写也能命中。
	got, err := s.UserByUsername(ctx, "ALICE")
	if err != nil {
		t.Fatalf("按用户名查用户失败：%v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("查到的用户不对：want id=%d got id=%d", u.ID, got.ID)
	}
	if got.Username != "alice" {
		t.Fatalf("用户名应该以小写规范形落库，实际 %q", got.Username)
	}
	// 落库后的 last_seen_at 必须是真实时间：DDL 的 DEFAULT now() 在 GORM 的
	// 显式列表插入路径上用不到，所以这里验证 CreateUser 补齐了它。
	if got.LastSeenAt.IsZero() {
		t.Fatalf("last_seen_at 不该是零值（CreateUser 应补成创建时间）")
	}
	ensureTimeClose(t, "last_seen_at（应约等于创建时间）", u.CreatedAt, got.LastSeenAt)

	byID, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("按 ID 查用户失败：%v", err)
	}
	if byID.Username != "alice" {
		t.Fatalf("按 ID 查到的用户不对：%+v", byID)
	}

	// 可选字段的往返。
	email := "alice@example.com"
	u2 := &User{Username: "bob", DisplayName: "Bob", PasswordHash: "h-bob", Email: &email, Role: RoleAdmin}
	if err := s.CreateUser(ctx, u2); err != nil {
		t.Fatalf("建第二个用户失败：%v", err)
	}
	back, err := s.UserByUsername(ctx, "bob")
	if err != nil {
		t.Fatalf("查 bob 失败：%v", err)
	}
	if back.Email == nil || *back.Email != email {
		t.Fatalf("email 往返失败：%v", back.Email)
	}
	if back.Role != RoleAdmin {
		t.Fatalf("显式指定的角色不该被默认值覆盖：%q", back.Role)
	}
}

func TestCreateUserRejectsDuplicateUsername(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	seedUser(t, s, "alice")

	// 大小写不同但归一化后同名 → 也必须冲突（唯一约束落在规范形上）。
	err := s.CreateUser(ctx, &User{Username: "Alice", DisplayName: "另一个 Alice", PasswordHash: "h"})
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("重名应该返回 ErrUsernameTaken，实际 %v", err)
	}

	var count int64
	if err := s.DB().Model(&User{}).Count(&count).Error; err != nil {
		t.Fatalf("统计用户数失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("冲突时不该插入任何行，实际 %d 行", count)
	}
}

func TestCreateUserValidation(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	cases := []struct {
		name string
		user *User
		want string
	}{
		{"nil", nil, "nil"},
		{"username 为空", &User{DisplayName: "x", PasswordHash: "h"}, "username"},
		{"username 只有空白", &User{Username: "   ", DisplayName: "x", PasswordHash: "h"}, "username"},
		{"username 超长", &User{Username: strings.Repeat("a", maxUsernameLen+1), DisplayName: "x", PasswordHash: "h"}, "超过"},
		{"display_name 为空", &User{Username: "ok1", PasswordHash: "h"}, "display_name"},
		{"display_name 超长", &User{Username: "ok2", DisplayName: strings.Repeat("字", maxDisplayNameLen+1), PasswordHash: "h"}, "超过"},
		{"password_hash 为空", &User{Username: "ok3", DisplayName: "x"}, "password_hash"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.CreateUser(ctx, c.user)
			if err == nil {
				t.Fatalf("应该报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", c.want, err)
			}
		})
	}

	var count int64
	if err := s.DB().Model(&User{}).Count(&count).Error; err != nil {
		t.Fatalf("统计用户数失败：%v", err)
	}
	if count != 0 {
		t.Fatalf("校验失败的调用不该写入任何行，实际 %d 行", count)
	}
}

func TestUserLookupNotFound(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	if _, err := s.UserByID(ctx, 123456); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺省 ID 应该返回 ErrNotFound，实际 %v", err)
	}
	if _, err := s.UserByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺省用户名应该返回 ErrNotFound，实际 %v", err)
	}
	if _, err := s.UserByUsername(ctx, "   "); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空白用户名应该返回 ErrNotFound（且不查库），实际 %v", err)
	}
}

func TestSetUserStatus(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	before, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}

	if err := s.SetUserStatus(ctx, u.ID, StatusBanned, "  刷屏  "); err != nil {
		t.Fatalf("封禁失败：%v", err)
	}
	banned, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}
	if banned.Status != StatusBanned {
		t.Fatalf("状态应该是 banned，实际 %q", banned.Status)
	}
	if banned.BannedReason == nil || *banned.BannedReason != "刷屏" {
		t.Fatalf("封禁理由应该被去掉首尾空白后记下，实际 %v", banned.BannedReason)
	}
	if !banned.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("状态变更应推进 updated_at：before=%s after=%s", before.UpdatedAt, banned.UpdatedAt)
	}
	if !banned.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("created_at 不该被动")
	}
	// 状态变更不该顺手改 token_version（§5 的组合动作由 service 负责，见方法注释）。
	if banned.TokenVersion != 1 {
		t.Fatalf("SetUserStatus 不该改 token_version，实际 %d", banned.TokenVersion)
	}

	// 封禁但不给理由 → banned_reason 置 NULL。
	if err := s.SetUserStatus(ctx, u.ID, StatusBanned, "   "); err != nil {
		t.Fatalf("第二次封禁失败：%v", err)
	}
	again, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}
	if again.BannedReason != nil {
		t.Fatalf("没给理由时 banned_reason 应该是 NULL，实际 %v", *again.BannedReason)
	}

	// 解封必须清空理由，不留陈旧信息。
	if err := s.SetUserStatus(ctx, u.ID, StatusActive, "刷屏"); err != nil {
		t.Fatalf("解封失败：%v", err)
	}
	unbanned, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}
	if unbanned.Status != StatusActive || unbanned.BannedReason != nil {
		t.Fatalf("解封后状态/理由应为 active/NULL，实际 %q/%v", unbanned.Status, unbanned.BannedReason)
	}

	// 非法状态：应用层先报错，库里的值不变。
	if err := s.SetUserStatus(ctx, u.ID, "rooted", ""); err == nil {
		t.Fatalf("非法 status 应该报错")
	}
	// 目标不存在。
	if err := s.SetUserStatus(ctx, 999999, StatusBanned, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的用户应该返回 ErrNotFound，实际 %v", err)
	}
}

func TestSetUserRole(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	if err := s.SetUserRole(ctx, u.ID, RoleAdmin); err != nil {
		t.Fatalf("改角色失败：%v", err)
	}
	got, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}
	if got.Role != RoleAdmin {
		t.Fatalf("角色应该是 admin，实际 %q", got.Role)
	}

	if err := s.SetUserRole(ctx, u.ID, "superuser"); err == nil {
		t.Fatalf("非法角色应该报错")
	}
	if err := s.SetUserRole(ctx, 999999, RoleAdmin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的用户应该返回 ErrNotFound，实际 %v", err)
	}
	// 非法角色必须被 CHECK 约束与应用层双重挡住：库里仍是 admin。
	after, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}
	if after.Role != RoleAdmin {
		t.Fatalf("非法改角色不该落库，实际 %q", after.Role)
	}
}

func TestBumpTokenVersion(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	first, err := s.BumpTokenVersion(ctx, u.ID)
	if err != nil {
		t.Fatalf("自增失败：%v", err)
	}
	if first != 2 {
		t.Fatalf("初始版本号是 1，自增后应为 2，实际 %d", first)
	}
	second, err := s.BumpTokenVersion(ctx, u.ID)
	if err != nil {
		t.Fatalf("第二次自增失败：%v", err)
	}
	if second != 3 {
		t.Fatalf("第二次自增应返回 3（返回值必须来自同一条 UPDATE 的 RETURNING），实际 %d", second)
	}
	if _, err := s.BumpTokenVersion(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的用户应该返回 ErrNotFound，实际 %v", err)
	}

	got, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}
	if got.TokenVersion != 3 {
		t.Fatalf("库里的 token_version 应该是 3，实际 %d", got.TokenVersion)
	}
}

func TestTouchLastSeenDoesNotTouchUpdatedAt(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	u := seedUser(t, s, "alice")

	before, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}

	at := time.Now().Add(-30 * time.Minute)
	if err := s.TouchLastSeen(ctx, u.ID, at); err != nil {
		t.Fatalf("更新 last_seen_at 失败：%v", err)
	}

	after, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}
	ensureTimeClose(t, "last_seen_at", at, after.LastSeenAt)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("心跳不该推进 updated_at：before=%s after=%s", before.UpdatedAt, after.UpdatedAt)
	}

	// 零值 at 应该被补成"现在"，而不是写进 0001-01-01。
	if err := s.TouchLastSeen(ctx, u.ID, time.Time{}); err != nil {
		t.Fatalf("零值 at 应该被容忍：%v", err)
	}
	touched, err := s.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("读用户失败：%v", err)
	}
	if time.Since(touched.LastSeenAt) > time.Minute {
		t.Fatalf("零值 at 应补成当前时间，实际 %s", touched.LastSeenAt)
	}

	if err := s.TouchLastSeen(ctx, 999999, at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的用户应该返回 ErrNotFound，实际 %v", err)
	}
}

func TestListUsersSearchAndPagination(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	names := []string{"alice", "bob", "carol", "dave", "eve"}
	users := make([]*User, 0, len(names))
	for _, n := range names {
		users = append(users, seedUser(t, s, n))
	}

	// 搜索：username 子串（大小写不敏感）。
	got, total, err := s.ListUsers(ctx, UserQuery{Search: "ALI"})
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if len(got) != 1 || got[0].Username != "alice" {
		t.Fatalf("搜索 ALI 应该只命中 alice，实际 %+v", got)
	}
	if total != 1 {
		t.Fatalf("total 应该是 1，实际 %d", total)
	}

	// 搜索：display_name 子串（seedUser 用 "测试：" 前缀）。
	_, total, err = s.ListUsers(ctx, UserQuery{Search: "测试："})
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if total != int64(len(names)) {
		t.Fatalf("按昵称前缀应该命中全部 %d 个，实际 %d", len(names), total)
	}

	// 通配符必须按字面处理：未转义的话 "%" 会命中全部 5 行。
	got, total, err = s.ListUsers(ctx, UserQuery{Search: "%"})
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if total != 0 || len(got) != 0 {
		t.Fatalf("搜索 %% 应该按字面匹配（0 条），实际 %d 条", total)
	}
	_, total, err = s.ListUsers(ctx, UserQuery{Search: "_"})
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if total != 0 {
		t.Fatalf("搜索 _ 应该按字面匹配（0 条），实际 %d 条", total)
	}

	// 分页：total 恒为总数，结果按 id ASC 稳定排序。
	page1, total, err := s.ListUsers(ctx, UserQuery{Limit: 2})
	if err != nil {
		t.Fatalf("分页失败：%v", err)
	}
	if total != int64(len(names)) {
		t.Fatalf("分页时 total 仍应是 %d，实际 %d", len(names), total)
	}
	if len(page1) != 2 || page1[0].ID != users[0].ID || page1[1].ID != users[1].ID {
		t.Fatalf("第一页应该是前两个用户且按 id ASC：%+v", page1)
	}

	page2, _, err := s.ListUsers(ctx, UserQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("分页失败：%v", err)
	}
	if len(page2) != 2 || page2[0].ID != users[2].ID {
		t.Fatalf("第二页应该从第三个用户开始：%+v", page2)
	}

	// 越界 offset 返回空切片（不是 nil，调用方不必判空）。
	empty, total, err := s.ListUsers(ctx, UserQuery{Limit: 2, Offset: 100})
	if err != nil {
		t.Fatalf("分页失败：%v", err)
	}
	if len(empty) != 0 || empty == nil {
		t.Fatalf("越界页应该是空切片，实际 %+v", empty)
	}
	if total != int64(len(names)) {
		t.Fatalf("越界页的 total 仍应是 %d，实际 %d", len(names), total)
	}

	// 负 offset / 非正 limit / 超大 limit 都被归一化，不报错也不拖全表。
	all, _, err := s.ListUsers(ctx, UserQuery{Limit: 100000, Offset: -5})
	if err != nil {
		t.Fatalf("归一化参数后不该报错：%v", err)
	}
	if len(all) != len(names) {
		t.Fatalf("limit 100000 应被截到上限并返回全部 %d 条，实际 %d", len(names), len(all))
	}
	seedAll, _, err := s.ListUsers(ctx, UserQuery{})
	if err != nil {
		t.Fatalf("默认分页失败：%v", err)
	}
	if len(seedAll) != len(names) {
		t.Fatalf("默认分页应返回全部 %d 条，实际 %d", len(names), len(seedAll))
	}
}

// TestUserColumnDefaultsMatchGoDefaults 是"两处默认值不许漂移"的哨兵。
//
// 手法：绕开 applyUserDefaults，只用必填列插一行，让 DDL 的 DEFAULT 生效，
// 再把结果与 Go 侧常量比对。改动 migrations.go 的默认值而忘了 applyUserDefaults
// （或反过来）时，这个用例会红。
func TestUserColumnDefaultsMatchGoDefaults(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	const ins = `INSERT INTO users (username, display_name, password_hash) VALUES (?, ?, ?)`
	if err := s.DB().Exec(ins, "defaults", "默认值用例", "h").Error; err != nil {
		t.Fatalf("插入默认值行失败：%v", err)
	}
	u, err := s.UserByUsername(ctx, "defaults")
	if err != nil {
		t.Fatalf("查默认值行失败：%v", err)
	}
	if u.Role != RoleUser {
		t.Fatalf("DDL 的 role 默认值应等于 store.RoleUser，实际 %q", u.Role)
	}
	if u.Status != StatusActive {
		t.Fatalf("DDL 的 status 默认值应等于 store.StatusActive，实际 %q", u.Status)
	}
	if u.TokenVersion != 1 {
		t.Fatalf("DDL 的 token_version 默认值应是 1，实际 %d", u.TokenVersion)
	}
	if u.LastSeenAt.IsZero() {
		t.Fatalf("DDL 的 last_seen_at 默认值应是 now()，实际零值")
	}

	// 绕过应用层的写入也必须被 DDL 的 CHECK 挡住（username 统一小写存储）。
	if err := s.DB().Exec(ins, "UPPERCASE", "绕过大写", "h").Error; err == nil {
		t.Fatalf("CHECK (username = lower(username)) 必须挡住大写用户名")
	}
	// 角色取值同样有 CHECK 兜底。
	if err := s.DB().Exec(
		`INSERT INTO users (username, display_name, password_hash, role) VALUES (?, ?, ?, ?)`,
		"badrole", "非法角色", "h", "root",
	).Error; err == nil {
		t.Fatalf("CHECK (role IN ('user','admin')) 必须挡住非法角色")
	}
}
