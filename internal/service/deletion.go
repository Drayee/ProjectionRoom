package service

import (
	"context"
	"errors"
	"log"
	"time"

	"ProjectionRoom/internal/service/auth"
	"ProjectionRoom/internal/store"
)

// 账号注销的冷静期（票 #73 / 决议 #51 §5）。
//
// # 两阶段
//
//  1. **发起**：凭当前密码 + 前端勾选确认。只写 `users.deletion_requested_at`，
//     对外立刻全隐藏，但本人仍能登录（否则 30 天内撤销不了）。
//  2. **到期清扫**：后台协程按批硬删（CASCADE 带走帖子/评论/点赞/关注/收藏/私聊）。
//
// # 为什么发起要凭当前密码
//
// 与改密同一条理由（决议 #51 §3）：这是不可逆操作的第一步，而"已持有有效会话"
// 不足以证明是本人在操作（借用的浏览器、被偷的笔记本）。代价与改密一致 ——
// 只劫持到会话的人也能发起注销，但**撤销**不需要密码（他没法阻止本人撤销），
// 而 30 天窗口正是为此存在。
//
// # 为什么不清会话、不 +1 token_version
//
// 冷静期内用户要能登录看横幅、点撤销。会话的处置留给真正的删除 ——
// 那时整行 CASCADE 带走，`sessions` 也在其中。
//
// # 为什么撤销不校验密码
//
// 撤销是**降低风险**的方向（把账号救回来）。给它加一道密码门，反而会让
// "忘了密码但还没注销成功"的人卡在那里。而任何拿到会话的人撤销注销，
// 后果只是"账号继续存在"——那本来就是默认状态。

// ErrDeletionPending 表示账号已经在冷静期里（发起时重复调用）。
//
// 与改密的 ErrPasswordMismatch 不同：这里**不报错也能表达**（返回既有起点），
// 所以这个错误只用于需要区分"新发起"与"本来就在"的场景（当前没有）。
// 保留它是因为未来若要做"注销审计"，两者必须能分开。
var ErrDeletionPending = errors.New("account: 该账号已在注销冷静期里")

// ErrDeletionNotPending 表示账号不在冷静期里（撤销时）。
//
// 单独一个错误而不是静默成功：界面的"撤销注销"按钮只在横幅存在时出现，
// 若端点回 204，用户会以为撤销了而实际什么也没发生。
var ErrDeletionNotPending = errors.New("account: 该账号没有待执行的注销")

// RequestAccountDeletion 把当前账号置入冷静期。
//
// 返回值是**起点时刻**（若已在冷静期则返回原起点，见 store 的同名方法：
// 重复调用不延期，否则每刷新一次页面就多 30 天）。
func (s *AccountService) RequestAccountDeletion(ctx context.Context, userID int64, currentPassword string) (time.Time, error) {
	if userID <= 0 {
		return time.Time{}, ErrSessionInvalid
	}
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return time.Time{}, ErrSessionInvalid
		}
		return time.Time{}, err
	}
	// 被封禁的账号不该走注销：它已经不可见，而冷静期结束后会真删 ——
	// 那等于让封禁变成"数据删除"的快捷方式（撤销封禁就没有数据可恢复了）。
	if u.Status != store.StatusActive {
		return time.Time{}, ErrUserForbidden
	}

	ok, busyErr := s.verifyPassword(u.PasswordHash, currentPassword)
	if busyErr != nil {
		return time.Time{}, busyErr
	}
	// 旧项目的 MD5 行：与 ChangeMyPassword 同一条兼容（那些账号能登录，
	// 却会卡在"当前密码不对"）。这里同样不需要补齐等价 CPU —— 已经通过会话认证。
	if !ok && auth.IsLegacyMD5Hash(u.PasswordHash) {
		ok = auth.VerifyLegacyMD5(u.PasswordHash, currentPassword)
	}
	if !ok {
		return time.Time{}, ErrPasswordMismatch
	}

	at, err := s.store.RequestAccountDeletion(ctx, userID, s.now())
	if err != nil {
		return time.Time{}, err
	}
	log.Printf("[INFO] 账号已进入注销冷静期：user=%d（对外已隐藏，到期后由清扫协程真删）", userID)
	return at, nil
}

// CancelAccountDeletion 撤销注销申请。
//
// 刻意**不校验密码**（理由见文件头）：撤销是降低风险的方向。
func (s *AccountService) CancelAccountDeletion(ctx context.Context, userID int64) error {
	if userID <= 0 {
		return ErrSessionInvalid
	}
	err := s.store.CancelAccountDeletion(ctx, userID)
	if errors.Is(err, store.ErrDeletionNotRequested) {
		return ErrDeletionNotPending
	}
	if errors.Is(err, store.ErrNotFound) {
		return ErrSessionInvalid
	}
	if err != nil {
		return err
	}
	log.Printf("[INFO] 账号已撤销注销：user=%d", userID)
	return nil
}

// DeletionState 读当前账号的冷静期状态（给 /me 的横幅用）。
func (s *AccountService) DeletionState(ctx context.Context, userID int64) (store.DeletionState, error) {
	if userID <= 0 {
		return store.DeletionState{}, ErrSessionInvalid
	}
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.DeletionState{}, ErrSessionInvalid
		}
		return store.DeletionState{}, err
	}
	return store.DeletionStateOf(u, s.deletionGrace), nil
}
