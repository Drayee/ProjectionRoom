package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 公开房列表的缺省/上限：它是"元数据侧的候选集合"，调用方还要与内存实况求交，
// 所以比管理端分页大得多；上限只是防止失控（比如误把 limit 传成 1e6）。
const (
	defaultPublicRoomLimit = 100
	maxPublicRoomLimit     = 1000
)

// UpsertRoomMeta 登记房间元数据（建房时调用；同房间码再来一次走覆盖分支）。
//
// 覆盖语义（冲突时更新的列）：owner_user_id / title / is_public / has_password /
// last_seen_at / closed_at。**created_at 保持首次值**，不被覆盖 —— 它是"这个房间码
// 第一次出现的时间"。
//
// 为什么敢整体覆盖：调用方在建房/登记时掌握完整的期望状态（§6）；只想改一两项
// 请用 UpdateRoomMeta 的 patch 版本。closed_at 的覆盖同时也提供了"房间码复用后
// 重新开放"的路径（传入 nil 即清空）。
func (s *Store) UpsertRoomMeta(ctx context.Context, m *RoomMeta) error {
	if m == nil {
		return errors.New("store: UpsertRoomMeta 收到 nil")
	}
	m.RoomID = strings.TrimSpace(m.RoomID)
	if m.RoomID == "" {
		return errors.New("store: room_id 不能为空")
	}
	if m.OwnerUserID <= 0 {
		return errors.New("store: owner_user_id 不能为空（§6：建房必须绑定房主）")
	}
	if len([]rune(m.Title)) > maxTitleLen {
		return fmt.Errorf("store: title 超过 %d 个字符", maxTitleLen)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	if m.LastSeenAt.IsZero() {
		m.LastSeenAt = time.Now()
	}

	// ON CONFLICT 的赋值列表用 map（clause.Assignments 内部会按键名排序，
	// SQL 文本确定），值一律走参数绑定。
	//
	// 这张列表是**可变列的清单**，不是"这次要改的列"：UpsertRoomMeta 的语义是
	// 整体登记（调用方掌握完整期望状态），因此每一次 upsert 都会重写这些列。
	// created_at 不在其中（保持首次值）；is_official / official_rank（0014，
	// wayfinder #35）必须在其中，否则任何一次 upsert（自愈投递、房主改标题、
	// 管理端下架）都会把已设好的官方标记**静默抹掉** —— 而且那不会报错，
	// 只是官方区某天突然空了。这是"手工同步两处清单"的典型代价，也是
	// 为什么新增列时要先在这里加一行。
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "room_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"owner_user_id": m.OwnerUserID,
			"title":         m.Title,
			"is_public":     m.IsPublic,
			"has_password":  m.HasPassword,
			"is_official":   m.IsOfficial,
			"official_rank": m.OfficialRank,
			"last_seen_at":  m.LastSeenAt,
			"closed_at":     m.ClosedAt,
		}),
	}).Create(m).Error
	if err != nil {
		return fmt.Errorf("store: 登记房间元数据失败: %w", err)
	}
	return nil
}

// RoomMetaByID 按房间码查元数据；不存在返回 ErrNotFound。
func (s *Store) RoomMetaByID(ctx context.Context, roomID string) (*RoomMeta, error) {
	var m RoomMeta
	err := s.db.WithContext(ctx).Where("room_id = ?", roomID).Take(&m).Error
	if err != nil {
		return nil, wrapDBErr("按房间码查元数据", err)
	}
	return &m, nil
}

// UpdateRoomMeta 局部更新房间元数据（房主改标题/公开性，§10 的 PATCH
// /api/rooms/:id/meta）；patch 里为 nil 的字段不改，目标不存在返回 ErrNotFound。
//
// 空 patch 返回错误而不是静默 no-op：这类调用几乎总是调用方算错了要改哪一项，
// 静默成功会让"界面点了却没反应"变成一个没人查得出来的 bug。
func (s *Store) UpdateRoomMeta(ctx context.Context, roomID string, patch RoomMetaPatch) error {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return errors.New("store: room_id 不能为空")
	}
	updates := map[string]any{}
	if patch.Title != nil {
		if len([]rune(*patch.Title)) > maxTitleLen {
			return fmt.Errorf("store: title 超过 %d 个字符", maxTitleLen)
		}
		updates["title"] = *patch.Title
	}
	if patch.IsPublic != nil {
		updates["is_public"] = *patch.IsPublic
	}
	if patch.HasPassword != nil {
		updates["has_password"] = *patch.HasPassword
	}
	if patch.LastSeenAt != nil {
		updates["last_seen_at"] = *patch.LastSeenAt
	}
	if patch.ClosedAt != nil {
		updates["closed_at"] = *patch.ClosedAt
	}
	// 运营标记（0014）。两个字段各自独立判定 nil，与上面的公开性改动同一套
	// 语义：撤销官方时调用方一次传 `IsOfficial=&false, OfficialRank=&0`。
	//
	// ⚠ 这里**不做**"把 IsOfficial 置 false 就连带清 rank"的隐式联动：
	// 隐式联动会让"只改 rank"与"撤销官方"两个意图共用一条代码路径，
	// 于是想单独降 rank 的调用方看到 rank 没变（被联动覆盖），
	// 而那种"传了没生效"在本层是查不出来的。要一起改就显式一起传。
	if patch.IsOfficial != nil {
		updates["is_official"] = *patch.IsOfficial
	}
	if patch.OfficialRank != nil {
		if *patch.OfficialRank < 0 {
			return errors.New("store: official_rank 不能为负数")
		}
		updates["official_rank"] = *patch.OfficialRank
	}
	if len(updates) == 0 {
		return errors.New("store: RoomMetaPatch 为空（没有任何要更新的字段）")
	}

	res := s.db.WithContext(ctx).Model(&RoomMeta{}).Where("room_id = ?", roomID).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("store: 更新房间元数据失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListPublicRoomIDs 返回"元数据侧认为公开且未关闭"的房间码（§6 公开房列表的元数据半边）。
//
// 排序 last_seen_at DESC：同名需求下"最近还活着的房间"更有价值，
// 也让 limit 截断时留下的是最可能仍有主播的房间。实况（是否真有主播、
// 是否在离线宽限期）由内存侧求交，本方法只负责元数据筛选。
//
// 返回空切片而不是 nil：调用方（JSON 列表）不必再判空。
func (s *Store) ListPublicRoomIDs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = defaultPublicRoomLimit
	}
	if limit > maxPublicRoomLimit {
		limit = maxPublicRoomLimit
	}

	ids := make([]string, 0, limit)
	err := s.db.WithContext(ctx).
		Model(&RoomMeta{}).
		Where("is_public = ? AND closed_at IS NULL", true).
		Order("last_seen_at DESC").
		Limit(limit).
		Pluck("room_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询公开房间列表失败: %w", err)
	}
	return ids, nil
}

// ListRoomMetas 按房间码批量取元数据，返回 room_id → 元数据 的映射（T1）。
//
// 为什么是批量而不是循环调用 RoomMetaByID：公开房列表（T2）与管理端房间列表（T4）
// 都是"内存里 N 个房间 → 补 N 条元数据"的形态。逐个查会变成 N 次往返，
// 而 N 的上限就是同时在册房间数（PR_MAX_ROOMS，默认 256）——
// 一次 IN 查询与 256 次查询的差别在这个端点上就是"能不能用"的差别。
//
// 三条约定：
//   - **空 ids 返回空 map 且不报错**：调用方（列表端点）经常会拿到空集合，
//     把它当成错误会让"当前没有房间"变成一个 500；
//   - **缺行的房间码不出现在 map 里**（不是补零值）：调用方据此区分
//     "元数据缺失，需要降级 + 自愈投递"与"元数据存在但字段为空"；
//   - ids 会被去重（重复的房间码在 IN 列表里既无意义又浪费）。
func (s *Store) ListRoomMetas(ctx context.Context, ids []string) (map[string]RoomMeta, error) {
	out := make(map[string]RoomMeta, len(ids))
	if len(ids) == 0 {
		// 空集合：不发查询，也不报错。
		return out, nil
	}

	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return out, nil
	}

	// IN 列表走参数绑定（GORM 展开成 $1..$n），房间码从不进 SQL 文本。
	rows := make([]RoomMeta, 0, len(unique))
	if err := s.db.WithContext(ctx).Model(&RoomMeta{}).
		Where("room_id IN ?", unique).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: 批量查询房间元数据失败: %w", err)
	}
	for _, m := range rows {
		out[m.RoomID] = m
	}
	return out, nil
}

// ListOfficialRoomIDs 返回"元数据侧认为官方且未关闭"的房间码，按运营位次排序
// （0014，wayfinder #35 的公开读半边）。
//
// 与 ListPublicRoomIDs 的分工（**两条独立查询，不是一个参数**）：
//
//	ListPublicRoomIDs   is_public = true  ORDER BY last_seen_at DESC   → /rooms 公开列表
//	ListOfficialRoomIDs  is_official = true ORDER BY rank DESC, room_id → /official 官方区
//
// 为什么不是给 ListPublicRoomIDs 加一个 official 参数：两者的排序键毫无共同前缀
// （最近活跃 vs 运营位次），合成一个函数只会得到"两个分支各自排序"的 if-else；
// 更重要的是**语义不同**——公开列表回答"现在有什么能进"（实况优先），
// 官方区回答"运营推了什么"（排序是刻意的人工结果，admin 调 rank 就是为了它）。
// 把后者按 last_seen_at 排会让"置顶"这个操作失去意义。
//
// 三个筛选与排序细节：
//
//   - `closed_at IS NULL` 与公开列表同口径：已关闭的房间不在任何"当前列表"里；
//   - 位次**降序**（大者在前）而不是升序：运营语义是"rank 越大越靠前"，
//     与 0010/0013 的 created_at DESC 也是同一个方向，少一次认知切换；
//   - 必须补第二排序键 room_id：official_rank 相同的行极常见（默认值全是 0），
//     只按它排时 PostgreSQL 的返回顺序不保证稳定 —— 那会让分页出现重复/漏行
//     （与 QueryRoomMetas 的 last_seen_at+room_id 是同一条理由）。
//
// 返回空切片而不是 nil：调用方（JSON 列表）不必再判空。
func (s *Store) ListOfficialRoomIDs(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = defaultPublicRoomLimit
	}
	if limit > maxPublicRoomLimit {
		limit = maxPublicRoomLimit
	}

	ids := make([]string, 0, limit)
	err := s.db.WithContext(ctx).
		Model(&RoomMeta{}).
		Where("is_official = ? AND closed_at IS NULL", true).
		Order("official_rank DESC, room_id ASC").
		Limit(limit).
		Pluck("room_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询官方房间列表失败: %w", err)
	}
	return ids, nil
}

// RoomMetaQuery 是管理端房间列表的查询条件（T1 / §8）。
//
// 三个可选筛选各自独立，互不牵连：Search 是子串匹配（房间码或标题），
// PublicOnly / PrivateOnly 是公开性筛选。**两者同时为 true 时返回空集**，
// 不做"取并集"这种自作聪明的解释 —— "只要公开"与"只要非公开"的交集就是空，
// 让调用方看到空结果比让它在两个互相矛盾的条件下拿到一堆数据更诚实。
type RoomMetaQuery struct {
	Search string
	// PublicOnly 只返回 is_public = true 的行。
	PublicOnly bool
	// PrivateOnly 只返回 is_public = false 的行。
	PrivateOnly bool
	// OfficialOnly 只返回 is_official = true 的行（0014，管理端按官方标记筛选）。
	//
	// 与 PublicOnly/PrivateOnly 是**三个并列**的筛选而不是嵌套关系：
	// 官方标记与公开性是两个正交的维度（一个房间可以又官方又未公开，
	// 例如运营先标记、等开播前再放出来）。把它们做成"公开性下面的子选项"
	// 会在语义上排除掉这种形态。
	OfficialOnly bool
	// Limit <= 0 取默认值（50），> 200 被截断；Offset < 0 视为 0（与 store.clampPage 同口径）。
	Limit  int
	Offset int
}

// QueryRoomMetas 按筛选条件分页返回房间元数据与其总数（管理端房间列表，T4）。
//
// 排序固定为 last_seen_at DESC, room_id ASC：
//
//   - 第一顺位是"最近还活着的房间更有价值"（与 ListPublicRoomIDs 同口径）；
//   - **必须补第二顺位**：last_seen_at 是 timestamptz，同一次测试里写入的多行很可能
//     落在同一微秒上，只按它排序时 PostgreSQL 的返回顺序不保证稳定 ——
//     那会让 offset 分页出现"第 2 页重复第 1 页的某一行"这种查不出来的抖动。
//     room_id 是主键（唯一、单调可比），加上它之后顺序完全确定。
//
// 返回空切片而不是 nil：调用方（JSON 列表）不必再判空。
func (s *Store) QueryRoomMetas(ctx context.Context, q RoomMetaQuery) ([]RoomMeta, int64, error) {
	limit, offset := clampPage(q.Limit, q.Offset)

	// filter 每次返回一个全新的查询：Count 与 Find 必须各自构造
	//（复用同一个 *gorm.DB 会把 Count 的 SELECT 串进 Find，见 ListUsers 的同一条说明）。
	filter := func() *gorm.DB {
		tx := s.db.WithContext(ctx).Model(&RoomMeta{})
		if pattern := likePattern(q.Search); pattern != "" {
			tx = tx.Where(`room_id ILIKE ? ESCAPE '\' OR title ILIKE ? ESCAPE '\'`, pattern, pattern)
		}
		if q.PublicOnly && q.PrivateOnly {
			// 互斥条件同时成立：交集为空。用一条恒假条件表达，而不是在 Go 侧提前返回，
			// 这样"为什么是空"在下一次读代码时仍然是显式的（而不是靠记得某个 if 分支）。
			tx = tx.Where("1 = 0")
		} else if q.PublicOnly {
			tx = tx.Where("is_public = ?", true)
		} else if q.PrivateOnly {
			tx = tx.Where("is_public = ?", false)
		}
		if q.OfficialOnly {
			tx = tx.Where("is_official = ?", true)
		}
		return tx
	}

	var total int64
	if err := filter().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("store: 统计房间元数据数失败: %w", err)
	}

	rows := make([]RoomMeta, 0, limit)
	if err := filter().
		Order("last_seen_at DESC, room_id ASC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("store: 查询房间元数据列表失败: %w", err)
	}
	return rows, total, nil
}
