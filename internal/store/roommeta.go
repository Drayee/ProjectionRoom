package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "room_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"owner_user_id": m.OwnerUserID,
			"title":         m.Title,
			"is_public":     m.IsPublic,
			"has_password":  m.HasPassword,
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
