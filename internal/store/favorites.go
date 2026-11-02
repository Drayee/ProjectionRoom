package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 收藏域（wayfinder #48 / 票 #64）。
//
// # 为什么是**一张多态表**而不是三张
//
// 收藏的动作语义在三种对象上是**同一个**（"私人书签，仅自己可见，不计任何公开数"），
// 差别只在"被收藏的东西长什么样"。三张表会让"列出我的全部收藏"变成 UNION，
// 而那个查询是 /me 收藏区块的主路径（票 #65）。单表 + target_type 让"按类型倒序
// 分页"是一次索引范围扫描。
//
// # 为什么不建三个 nullable 外键
//
// 那样每加一种可收藏对象就要改表（加列 + 加 CHECK），而这三列里同时只有一列非空
// —— 一个 2/3 恒为 NULL 的列组是"用表结构表达枚举"的典型误用。
// 代价是**跨表完整性由这一层保证**：对象不存在就返回 ErrFavoriteTargetNotFound、
// 不落行（见 AddFavorite）。没有外键替我们挡，所以它是硬约束而不是"顺手校验"。
//
// # target_id 为什么是**文本**而不是 bigint
//
// 帖子与番剧的主键是 bigint，但**放映室的主键是房间码**（varchar(12)）。
// 三种类型共用一个列，只能取能装下房间码的那一种。把房间折成数字是做不到的
//（房间码是用户可见的业务主键，不是自增 id）。
//
// # 为什么没有公开计数
//
// 收藏是私人书签，不是公开表态 —— 那是点赞的分工（决议 #48 §3 的对照表）。
// 表上因此没有任何反范式计数列，也就没有"计数与行数漂移"这一类问题。

// FavoriteType 是可收藏对象的类型。
type FavoriteType string

const (
	FavoritePost  FavoriteType = "post"
	FavoriteRoom  FavoriteType = "room"
	FavoriteAnime FavoriteType = "anime"
)

// favoriteTypes 是白名单。**必须与 DDL 的 CHECK 同值**（同一条纪律见 posts.go 的
// 长度上限：不一致的症状是"应用层放过、数据库报 500"）。
var favoriteTypes = map[FavoriteType]struct{}{
	FavoritePost:  {},
	FavoriteRoom:  {},
	FavoriteAnime: {},
}

// Valid 报告这个类型是否在白名单里。
func (t FavoriteType) Valid() bool {
	_, ok := favoriteTypes[t]
	return ok
}

const (
	defaultFavoriteLimit = 20
	maxFavoriteLimit     = 100
)

var (
	// ErrFavoriteBadType 表示 target_type 不在白名单里。
	ErrFavoriteBadType = errors.New("store: 未知的收藏类型")

	// ErrFavoriteTargetNotFound 表示要收藏的对象不存在（含"房间码不是有效码"）。
	ErrFavoriteTargetNotFound = errors.New("store: 收藏对象不存在")

	// ErrFavoriteBadCursor 表示游标不是本函数产出的形态。
	ErrFavoriteBadCursor = errors.New("store: 收藏游标不合法")
)

// Favorite 是收藏行。
type Favorite struct {
	UserID     int64     `gorm:"column:user_id;primaryKey"`
	TargetType string    `gorm:"column:target_type;primaryKey"`
	TargetID   string    `gorm:"column:target_id;primaryKey"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (Favorite) TableName() string { return "favorites" }

// FavoriteEntry 是收藏列表的一行（已带上对象标题与失效态）。
type FavoriteEntry struct {
	TargetID string
	// Title 是对象当前的标题；对象已删时为空串（前端据 Valid 决定是否显示）。
	Title string
	// Valid 表示对象此刻是否还可见。**失效条目照样返回**（决议 #48 §5）：
	// 收藏莫名变少比看到「已失效」更让人困惑。
	Valid     bool
	CreatedAt time.Time
	// Cursor 是这一行自己的翻页游标，直接回给调用方当下一页的 before。
	Cursor string
}

// normalizeFavoriteTarget 校验类型与 id，并把 id 归一成文本。
//
// 数字型对象（post/anime）在这里就把"不是数字"挡掉：让它走到 SQL 里会变成
// 一次类型转换失败，最终以 500 的形式出现 —— 而它明明是客户端传错了。
func normalizeFavoriteTarget(typ FavoriteType, targetID string) (string, error) {
	if !typ.Valid() {
		return "", ErrFavoriteBadType
	}
	id := strings.TrimSpace(targetID)
	if id == "" {
		return "", ErrFavoriteTargetNotFound
	}
	if typ == FavoritePost || typ == FavoriteAnime {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 {
			return "", ErrFavoriteTargetNotFound
		}
		return strconv.FormatInt(n, 10), nil
	}
	// 房间码：只做长度与空白归一，格式的权威判据在 handler（房间码正则）与
	// rooms_meta 的主键 —— 这里再写一遍正则就是第三个真源。
	if len(id) > 12 {
		return "", ErrFavoriteTargetNotFound
	}
	return id, nil
}

// favoriteTargetExists 判断对象此刻是否存在（房间还要求"已公开且未关闭"）。
//
// 返回 (exists, err)。err 只用于真正的数据库故障；"不存在"走 exists=false。
func (s *Store) favoriteTargetExists(ctx context.Context, typ FavoriteType, targetID string) (bool, error) {
	var cnt int64
	var err error
	switch typ {
	case FavoritePost:
		err = s.db.WithContext(ctx).Table("posts").Where("id = ?", targetID).Count(&cnt).Error
	case FavoriteAnime:
		err = s.db.WithContext(ctx).Table("anime").Where("id = ?", targetID).Count(&cnt).Error
	case FavoriteRoom:
		// 房间的"存在"按**可见性**算：关闭或未公开的房间对收藏者来说已经没了。
		// 与公开房列表（is_public ∧ closed_at IS NULL）同口径 —— 否则会出现
		// "能收藏一个列表里根本看不到的房间"。
		err = s.db.WithContext(ctx).Table("rooms_meta").
			Where("room_id = ? AND is_public = ? AND closed_at IS NULL", targetID, true).
			Count(&cnt).Error
	default:
		return false, ErrFavoriteBadType
	}
	if err != nil {
		return false, fmt.Errorf("store: 检查收藏对象存在性失败: %w", err)
	}
	return cnt > 0, nil
}

// AddFavorite 收藏一个对象，返回是否**新建**了行（false = 本来就收藏过）。
//
// 幂等：主键 (user_id, target_type, target_id) 是这条语义的唯一 owner，
// 并发下两次同时收藏也只能落一行。这与 TogglePostLike 的 toggle 语义**刻意不同**：
// 收藏的端点形状是 PUT/DELETE（幂等 set），不是 POST toggle ——
// 前端的乐观更新与网络重试都需要"再发一次没有副作用"。
func (s *Store) AddFavorite(ctx context.Context, userID int64, typ FavoriteType, targetID string) (bool, error) {
	id, err := normalizeFavoriteTarget(typ, targetID)
	if err != nil {
		return false, err
	}
	if userID <= 0 {
		return false, ErrFavoriteTargetNotFound
	}

	ok, err := s.favoriteTargetExists(ctx, typ, id)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrFavoriteTargetNotFound
	}

	res := s.db.WithContext(ctx).Exec(
		`INSERT INTO favorites (user_id, target_type, target_id) VALUES (?, ?, ?)
		 ON CONFLICT DO NOTHING`, userID, string(typ), id)
	if res.Error != nil {
		return false, fmt.Errorf("store: 收藏失败: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// RemoveFavorite 取消收藏，返回是否**真的删掉了**一行（false = 本来就没收藏）。
//
// 与 AddFavorite 对称：幂等，不因为"没这行"而报错 —— 前端重试不该看到失败。
func (s *Store) RemoveFavorite(ctx context.Context, userID int64, typ FavoriteType, targetID string) (bool, error) {
	id, err := normalizeFavoriteTarget(typ, targetID)
	if err != nil {
		return false, err
	}
	if userID <= 0 {
		return false, nil
	}
	res := s.db.WithContext(ctx).Exec(
		`DELETE FROM favorites WHERE user_id = ? AND target_type = ? AND target_id = ?`,
		userID, string(typ), id)
	if res.Error != nil {
		return false, fmt.Errorf("store: 取消收藏失败: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// FavoriteStatesFor 批量回答"这些对象里哪些已被我收藏"。
//
// 返回 map[targetID]bool。**一次查询**：这条接口的形状本身就是「禁 N+1」的
// 可执行形态 —— 调用方拿不到"逐个查"的机会（与 ListPosts 的 liked 批量回填同律）。
// 空输入不查库（列表为空时调用方不该为此写 if）。
func (s *Store) FavoriteStatesFor(ctx context.Context, userID int64, typ FavoriteType, targetIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(targetIDs))
	if userID <= 0 || len(targetIDs) == 0 {
		return out, nil
	}
	if !typ.Valid() {
		return nil, ErrFavoriteBadType
	}
	ids := make([]string, 0, len(targetIDs))
	for _, raw := range targetIDs {
		if id, err := normalizeFavoriteTarget(typ, raw); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	var hit []string
	if err := s.db.WithContext(ctx).Table("favorites").
		Where("user_id = ? AND target_type = ? AND target_id IN ?", userID, string(typ), ids).
		Pluck("target_id", &hit).Error; err != nil {
		return nil, fmt.Errorf("store: 查询收藏状态失败: %w", err)
	}
	for _, id := range hit {
		out[id] = true
	}
	return out, nil
}

// favoriteCursor 把 (created_at, target_id) 编成一个不透明串。
//
// 为什么游标要带 target_id：收藏是"同一微秒内可能有多条"的写入（批量导入、
// 脚本收藏）。只按时间翻页会在这种边界上漏行或重复行 —— 而收藏重复一条
// 比漏掉更容易被发现（用户会以为自己的书签坏了）。
func favoriteCursor(t time.Time, targetID string) string {
	return strconv.FormatInt(t.UTC().UnixMicro(), 10) + ":" + targetID
}

// parseFavoriteCursor 解析游标。空串 = 第一页。
func parseFavoriteCursor(raw string) (time.Time, string, error) {
	if raw == "" {
		return time.Time{}, "", nil
	}
	micro, id, ok := strings.Cut(raw, ":")
	if !ok {
		return time.Time{}, "", ErrFavoriteBadCursor
	}
	n, err := strconv.ParseInt(micro, 10, 64)
	if err != nil {
		return time.Time{}, "", ErrFavoriteBadCursor
	}
	if id == "" {
		return time.Time{}, "", ErrFavoriteBadCursor
	}
	return time.UnixMicro(n).UTC(), id, nil
}

// ListFavorites 返回我的某一类收藏（收藏时间倒序，游标翻页）。
//
// before 是上一页最后一行的 Cursor（空串 = 第一页）。排序键是
// (created_at DESC, target_id DESC)，与索引 favorites_user_type_idx 同序。
//
// 每条都带对象当前的标题与 Valid：**对象没了也照样返回**（决议 #48 §5），
// 前端据 Valid 显示「已失效」。这就是"没有外键"换来的能力 ——
// 有 ON DELETE CASCADE 的话这条会静默消失。
func (s *Store) ListFavorites(ctx context.Context, userID int64, typ FavoriteType, before string, limit int) ([]FavoriteEntry, bool, error) {
	if !typ.Valid() {
		return nil, false, ErrFavoriteBadType
	}
	if limit <= 0 {
		limit = defaultFavoriteLimit
	}
	if limit > maxFavoriteLimit {
		limit = maxFavoriteLimit
	}
	if userID <= 0 {
		return nil, false, nil
	}
	curTime, curID, err := parseFavoriteCursor(before)
	if err != nil {
		return nil, false, err
	}

	base := func() *gorm.DB {
		tx := s.db.WithContext(ctx).Table("favorites AS f").
			Where("f.user_id = ? AND f.target_type = ?", userID, string(typ))
		if !curTime.IsZero() {
			// 行值比较：与 ORDER BY (created_at DESC, target_id DESC) 严格配对。
			// 写成 `created_at < ? OR (created_at = ? AND target_id < ?)` 也对，
			// 但行值比较让"排序键"与"过滤键"在文本上就是同一个元组。
			tx = tx.Where("(f.created_at, f.target_id) < (?, ?)", curTime, curID)
		}
		return tx.Order("f.created_at DESC, f.target_id DESC").Limit(limit + 1)
	}

	type rawRow struct {
		TargetID  string    `gorm:"column:target_id"`
		CreatedAt time.Time `gorm:"column:created_at"`
		Title     *string   `gorm:"column:title"`
	}

	var rows []rawRow
	// 三支分别 LEFT JOIN 各自的对象表：标题与"是否还有效"都来自那张表。
	// LEFT JOIN 而不是 JOIN 是**核心语义**（失效条目必须留下）。
	switch typ {
	case FavoritePost:
		err = base().
			Select("f.target_id, f.created_at, p.title").
			Joins("LEFT JOIN posts p ON p.id::text = f.target_id").
			Scan(&rows).Error
	case FavoriteAnime:
		err = base().
			Select("f.target_id, f.created_at, a.title").
			Joins("LEFT JOIN anime a ON a.id::text = f.target_id").
			Scan(&rows).Error
	case FavoriteRoom:
		// 房间的有效性还要求"已公开且未关闭" —— 只判行存在会让一个被下架的
		// 房间看起来仍然可点（而列表页里根本找不到它）。
		err = base().
			Select("f.target_id, f.created_at, r.title").
			Joins("LEFT JOIN rooms_meta r ON r.room_id = f.target_id AND r.is_public = ? AND r.closed_at IS NULL", true).
			Scan(&rows).Error
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: 查询收藏列表失败: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]FavoriteEntry, 0, len(rows))
	for _, r := range rows {
		e := FavoriteEntry{
			TargetID:  r.TargetID,
			CreatedAt: r.CreatedAt,
			Cursor:    favoriteCursor(r.CreatedAt, r.TargetID),
			Valid:     r.Title != nil,
		}
		if r.Title != nil {
			e.Title = *r.Title
		}
		out = append(out, e)
	}
	return out, hasMore, nil
}
