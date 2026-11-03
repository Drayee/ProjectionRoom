package service

// 头像的**频控与违规处置**（票 #75 / 决议 #54 §3 的审核四件套：人机验证、
// 每日五次、管理端一键重置、人工处置）。
//
// # 为什么频控记在 PATCH 面而不是上传面
//
// 决议 #54 §8 定的是「上传接口保持通用、不感知头像语义」：`POST /api/uploads`
// 也服务帖子配图，如果它知道"这是头像"，就会长出第二套配额与第二套错误码。
// 所以额度的判据落在"把这张图设成我的头像"这个动作上 —— 也就是 `PATCH /api/me`。
//
// 一个直接后果是：用户一天可以上传 20 次、只有 5 次能真的生效。这是对的 ——
// 上传是廉价且无副作用的（只是往桶里写了一个对象），"换头像"才是需要限的东西。
//
// # 额度为什么在写库**之前**扣
//
// 反过来的顺序（先写库、成功后再扣）有一个真实的坏窗口：两个请求同时进来，
// 两边都写成功、两边都去扣，于是五次限制被用掉了六次。先扣（一条原子 UPDATE，
// 见 store.ConsumeAvatarChange）则不会超额。
//
// 代价是"扣了额度但写库失败"会白吃一次 —— 那种失败只可能是数据库层故障，
// 而那时用户看到的是一条 500，本来也要重试。相比"限不住"，这是更小的问题。
//
// # 为什么清除头像不扣额度
//
// 从像素图回落到字母头是**降低风险**的方向（违规图的紧急处置正是这条路），
// 给它设计数等于让用户在"想撤掉那张图"和"留到明天"之间选后者 —— 正好反了。
// 与注销冷静期"撤销不要密码"是同一条判断：降风险的动作不该有摩擦。

import (
	"context"
	"errors"
	"fmt"
	"log"

	"ProjectionRoom/internal/store"
)

// DefaultAvatarChangesPerDay 是每人每日的头像变更上限（决议 #54 §3）。
//
// 5 次的来源是经验值：正常用户一年改几次，5 次足以覆盖"试了几张都不满意"
// 的那天，而批量换图做恶作剧的人会在第 6 次被拦住。
const DefaultAvatarChangesPerDay = 5

// ErrAvatarQuotaExceeded 表示今天的头像变更次数已用满（→ 429）。
//
// 文案里**带上具体数字与"明天"**：429 如果不告诉用户还能不能做、什么时候能做，
// 它和一个坏掉的按钮没有区别。计数口径是 UTC 日（见 store.dateOnly）。
var ErrAvatarQuotaExceeded = errors.New("今天改头像的次数已用满（每天 5 次），明天再试")

// avatarChangesPerDay 返回本实例的限额；<=0 视为不限额（0 也是默认值的语义）。
func (s *AccountService) avatarChangesPerDay() int {
	return s.avatarDailyLimit
}

// consumeAvatarChange 扣一次今天的额度；不限额时直接返回。
//
// 分成一个小函数是因为它有两个调用点（本人换图、以及将来可能的"代客重置"），
// 而"限额从哪里读、日期从哪里来"这件事只能有一份实现 ——
// 时间源必须是 s.now（可注入），否则跨日口径测不了。
func (s *AccountService) consumeAvatarChange(ctx context.Context, userID int64) error {
	limit := s.avatarChangesPerDay()
	if limit <= 0 {
		return nil
	}
	err := s.store.ConsumeAvatarChange(ctx, userID, s.now(), limit)
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrAvatarQuotaExceeded) {
		return ErrAvatarQuotaExceeded
	}
	if errors.Is(err, store.ErrNotFound) {
		return ErrUserNotFound
	}
	return err
}

// ResetUserAvatar 是管理端的一键重置（票 #75：违规头像回到字母头）。
//
// # 与本人换头像的两点不同
//
//  1. **不扣额度**：管理员处置不该消耗被处置者的配额（他明天还要能传正经图）。
//  2. **对象存储未配置时仍然可用**：清除只写库（`avatar_url = NULL`），
//     删对象那一步才需要存储。所以这里不能像 UpdateMyAvatarURL 那样在第一行
//     就把 s.avatar == nil 当错误 —— 那会让"部署没开存储"变成
//     "管理员也没法把违规头像下架"，而后者恰恰是更紧急的那件事。
//
// # 顺序：先置空，再删对象
//
// 用户/他人看到的是库里的值，所以置空立刻生效（"所有人视角回到字母头"）；
// 删对象是事后清理，失败只记日志。反过来（先删图再置空）有一段真实的坏窗口：
// 图片 404 了而库里还指着它，前端只能靠 onerror 兜底 —— 那是能看到的破图。
func (s *AccountService) ResetUserAvatar(ctx context.Context, actorID, targetID int64, ip string) error {
	if actorID <= 0 || targetID <= 0 {
		return ErrUserNotFound
	}

	u, err := s.store.UserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrUserNotFound
		}
		return err
	}
	old := u.AvatarURL

	// 已经是字母头：仍然记一条审计（"管理员看过并处置过"也是事实），
	// 但不再写库、不再删对象 —— 幂等。
	if old == nil || *old == "" {
		s.auditAvatarReset(ctx, actorID, targetID, ip, true)
		return nil
	}

	if err := s.store.UpdateAvatarURL(ctx, targetID, nil); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrUserNotFound
		}
		return err
	}

	// 置空成功之后才删对象（见上面的顺序说明）。
	if s.avatar != nil {
		oldURL := *old
		s.submit("删除被重置的头像对象", func(ctx context.Context) error {
			if err := s.avatar.Remove(ctx, oldURL); err != nil {
				log.Printf("[WARN] 删除被重置的头像对象失败（%s）：%v", oldURL, err)
			}
			return nil
		})
	}

	s.auditAvatarReset(ctx, actorID, targetID, ip, false)
	return nil
}

// auditAvatarReset 写一条 `user.avatar.reset` 审计。
//
// 走 s.submit（异步单写，I4），与 SetRole / setStatus 同一条通道。
// `unchanged` 区分"确实撤掉了一张图"与"本来就是字母头"——
// 两者对事后复盘的含义不同：前者要能回答"撤掉的是哪张"，后者只是确认。
func (s *AccountService) auditAvatarReset(ctx context.Context, actorID, targetID int64, ip string, unchanged bool) {
	detail := auditDetail(map[string]any{"unchanged": unchanged})
	target := targetID
	s.submit("写重置头像审计", func(ctx context.Context) error {
		return s.store.InsertAudit(ctx, &store.AdminAudit{
			ActorID:    actorID,
			Action:     "user.avatar.reset",
			TargetType: "user",
			TargetID:   fmt.Sprintf("%d", target),
			Detail:     detail,
			IP:         optString(ip),
		})
	})
}
