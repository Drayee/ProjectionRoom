package service

// 像素头像的领域规则（票 #74 / 决议 #54）。
//
// # 为什么单独一个文件
//
// 改头像不是一个"改一列"的动作，它有三条跨模块的规则：
//
//  1. **URL 必须是本桶的合法对象**（否则就是外链直填 → 追踪像素 / 混合内容）；
//  2. **旧对象要在事后删掉**（否则桶里会积累永远不再被引用的图片，
//     而且它们仍然公开可访问 —— "我换了头像"并不等于"旧头像消失了"）；
//  3. **对象存储未配置时整条路径拒绝**（不是"静默存一个 URL 进库"）。
//
// 三条都属于领域规则，不属于 HTTP。放在这里之后，本人换头像（本票）与
// 管理员重置头像（票 #75）可以共用同一份，而不是各写一遍。

import (
	"context"
	"errors"
	"log"
	"strings"

	"ProjectionRoom/internal/store"
)

// ErrAvatarUnavailable 表示对象存储未配置，改头像这条路径不可用（→ 503）。
//
// 与 storage.ErrUnavailable 分开：那个是"存储层调用失败"，这个是
// "这个部署根本没有存储能力"。前者可能是一次网络抖动（可以重试），
// 后者重试一万次也一样 —— 前端要给的下一步完全不同。
var ErrAvatarUnavailable = errors.New("头像功能暂不可用：本部署未启用对象存储")

// ErrAvatarURLInvalid 表示传进来的 URL 不是本桶的对象（→ 400）。
var ErrAvatarURLInvalid = errors.New("头像地址不合法：请先通过上传接口取得地址")

// AvatarBackend 是头像所需的对象存储能力（窄接口，票 #74）。
//
// 只要两个方法：判"这个 URL 是不是我们发的"，以及"删掉这个对象"。
// 上传本身走 `POST /api/uploads`（决议 #54 §8 的「零新端点」），
// 不在这条接口上 —— 账号用例不该有"往桶里写任意内容"的能力。
//
// *storage.Client 满足它；测试可以只实现这两个。
type AvatarBackend interface {
	// OwnsURL 判断 URL 是否为本桶的合法对象（形态校验，不查存在）。
	OwnsURL(u string) bool
	// Remove 删除对象；失败只记日志（见 UpdateMyAvatarURL）。
	Remove(ctx context.Context, publicURL string) error
}

// UpdateMyAvatarURL 改本人头像（票 #74 的 PATCH /api/me 的 avatarUrl 字段）。
//
// # 参数语义
//
// avatarURL == nil → **清除**（回落到字母头）；非 nil → 设成这个 URL。
// 与 patchMeRequest.AvatarURL 的指针判据一一对应：nil 是"清除"，
// 不传字段是另一件事（由 handler 判，见 patchMe）。
//
// # 旧对象的删除为什么是异步的
//
// 铁律 I4：请求路径上不做多余的 DB/网络往返。删对象是一次 S3 往返
// （几十毫秒到几秒），而它对**这次响应**没有任何影响 —— 用户看到的已经是新头像。
// 失败也只记日志：一张图没删掉比"换头像报错"轻得多（同 store.Remove 的注释）。
//
// # 为什么先落库再删旧图
//
// 反过来的顺序有一个真实的坏窗口：删完旧图、写库失败 → 用户的头像变成 404，
// 而库里还指着那张已经被删的图。先落库则最坏情况是"旧图还在桶里但没人引用"
// —— 那是可清理的垃圾，不是用户可见的故障。
//
// # 为什么清除时不删
//
// `nil` 的语义是"回落到字母头"，它**不**意味着"把那张图从桶里拿掉"：
// 管理端重置（票 #75）需要先置空（让所有人立刻看到字母头）再删对象，
// 两步分开才能在"删失败"时仍然保住"用户看到字母头"这个结果。
// 所以清除路径不动桶 —— 对象由调用方（或票 #75 的重置）负责删。
//
// # 每日频控挂在这里（票 #75）
//
// 决议 #54 §8 定的「零新端点」把额度判据落在 `PATCH /api/me`：上传接口保持通用
// （它也服务帖子配图），"把这张图设成我的头像"才是要限的动作。所以扣额度发生在
// 本函数的写库之前，而不是 uploads 那条路上 —— 详见 avatar_reset.go 文件头的
// 「为什么频控记在 PATCH 面」与「为什么额度在写库之前扣」两节。
func (s *AccountService) UpdateMyAvatarURL(ctx context.Context, userID int64, avatarURL *string) (Profile, error) {
	if userID <= 0 {
		return Profile{}, ErrUserNotFound
	}
	if s.avatar == nil {
		return Profile{}, ErrAvatarUnavailable
	}

	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Profile{}, ErrUserNotFound
		}
		return Profile{}, err
	}
	if u.Status != store.StatusActive {
		return Profile{}, ErrUserForbidden
	}

	// 归一化：空串等价于 nil（"没有"只有一种表示，见 store.UpdateAvatarURL）。
	var next *string
	if avatarURL != nil {
		trimmed := strings.TrimSpace(*avatarURL)
		if trimmed != "" {
			// 形态校验：必须指向本桶。**这一步不能省**，理由见 OwnsURL 的注释
			//（外链直填 = 拿我们的页面做追踪像素 + 混合内容来源）。
			if !s.avatar.OwnsURL(trimmed) {
				return Profile{}, ErrAvatarURLInvalid
			}
			next = &trimmed
		}
	}

	// 同一个 URL 再设一次是幂等的：直接回当前档案，既不写库也不删对象
	//（删了就把刚设的那张图删了）。
	if sameOptionalString(u.AvatarURL, next) {
		return ProfileOf(u), nil
	}

	// 频控（票 #75）：**只在"真的要变"的时候扣**，且必须在写库之前。
	//
	// 两个细节都是刻意的：
	//   - 幂等短路在上面 —— 原样再设一次不算改头像，不该吃掉额度
	//     （前端重挂、网络重试都会产生这种请求）；
	//   - 清除（next == nil）不扣 —— 回落到字母头是降低风险的方向，
	//     给它设计数等于让违规图的紧急下架也要等到明天（见 avatar_reset.go 文件头）。
	if next != nil {
		if err := s.consumeAvatarChange(ctx, userID); err != nil {
			return Profile{}, err
		}
	}

	if err := s.store.UpdateAvatarURL(ctx, userID, next); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Profile{}, ErrUserNotFound
		}
		return Profile{}, err
	}

	// 回读：客户端应当拿到**库里现在是什么**（与 UpdateMyDisplayName 同一理由）。
	after, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return Profile{}, err
	}

	// 旧对象进异步单写通道（I4）。
	if old := u.AvatarURL; old != nil && *old != "" && !sameOptionalString(old, next) {
		oldURL := *old
		s.submit("删除旧头像对象", func(ctx context.Context) error {
			if err := s.avatar.Remove(ctx, oldURL); err != nil {
				log.Printf("[WARN] 删除旧头像对象失败（%s）：%v", oldURL, err)
			}
			return nil
		})
	}

	return ProfileOf(after), nil
}

// sameOptionalString 判两个可空字符串是否相等（nil 与空串视为相同）。
func sameOptionalString(a, b *string) bool {
	return derefOrEmpty(a) == derefOrEmpty(b)
}

// derefOrEmpty 取指针指向的字符串，nil 返回空串。
func derefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
