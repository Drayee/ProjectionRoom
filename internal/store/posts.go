package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 帖子域的长度上限。**必须与 DDL 的列上限、handler 的校验同值**
// （同一条纪律见 anime.go 与 roommeta.go：不一致的症状是"表单能填、保存报 500"）。
const (
	maxPostTitleLen   = 60
	maxPostContentLen = 5000 // content 列是 text，上限由这里管（见 DDL 注释）
	maxPostImages     = 9    // 与前端发帖表单的图片数上限一致
	maxPostImageLen   = 512  // 单条外链 URL 的上限（与 anime.cover_url 同值）
	maxCommentLen     = 1000 // 评论正文上限
	defaultPostLimit  = 20
	maxPostListLimit  = 100
)

// ErrPostNotFound 表示按 id 查不到帖子/评论。
var ErrPostNotFound = errors.New("store: 帖子不存在")

// ErrCommentNotFound 表示按 id 查不到评论。
var ErrCommentNotFound = errors.New("store: 评论不存在")

// ErrCommentForbidden 表示"这条评论不是你的"。
//
// 为什么 store 里会有"权限"错误：判据是**数据事实**（author_id == 请求者），
// store 是最知道这件事的一层；handler 拿它翻译成 403。这与 I3 不冲突 ——
// 授权判定仍然 100% 在服务端，只是落在持有事实的那一层。
var ErrCommentForbidden = errors.New("store: 只能删除自己的评论")

// Post 是帖子行。Images 的 jsonb 往返方式与 Anime.Aliases 相同（serializer:json）。
type Post struct {
	ID           int64     `gorm:"column:id;primaryKey"`
	AuthorID     int64     `gorm:"column:author_id"`
	Title        string    `gorm:"column:title"`
	Content      string    `gorm:"column:content"`
	Images       []string  `gorm:"column:images;serializer:json;type:jsonb"`
	LikeCount    int       `gorm:"column:like_count"`
	CommentCount int       `gorm:"column:comment_count"`
	AnimeID      *int64    `gorm:"column:anime_id"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`

	// LastReplyAt 是"这个主题串最近一次有人来"的时刻（wayfinder #38）。
	//
	// 反范式列，与 like_count / comment_count 同一套纪律：**必须与纪录写入同事务**
	//（见 CreatePost / AddPostComment）。DDL 注释里有"为什么不用 join 聚合"的完整理由。
	//
	// 为什么用指针而不是 time.Time：NULL 与"零值时刻"是两件事。写成 time.Time
	// 会让"没有这个维度"变成公元 1 年，而排序键里混进一个假的极值是最难查的那类 bug。
	//
	// **NULL 的实际含义只有一种**：迁移 0013 之前创建的行（那一版刻意不回填老数据，
	// 理由见 DDL 注释）。0013 之后**任何**帖子（含普通帖子）在 CreatePost 里都会
	// 拿到一个真实时刻 —— 普通帖子拿到的是发帖时刻，虽然它不在任何板块里、
	// 也用不到这个键，但"每行都有值"让排序列不需要 COALESCE。
	//
	// 正因为还存在 NULL 行（老数据），板块列表的 ORDER BY 必须写 `NULLS LAST`：
	// PostgreSQL 在 `DESC` 下默认是 **NULLS FIRST**，于是"将来给一篇老帖挂上板块"
	// 会让它瞬间跳到板块第一位。索引也按 NULLS LAST 建，两者口径必须一致
	//（见迁移 0013；口径不一致时索引会被放弃，退化成排序全量）。
	LastReplyAt *time.Time `gorm:"column:last_reply_at"`

	// AuthorName 是查询时 JOIN users 带出的**显示名**，不是 posts 的列。
	// 为什么允许它出现在模型里：列表与详情都要显示作者昵称，没有别的取法
	//（N 次单查更糟）。`gorm:"->"` 让它只读，Create/Update 不会试图写这一列。
	AuthorName string `gorm:"column:author_name;->"`

	// Liked 同样是查询时算出的**只读**字段（前端 Post.liked 是必填形状）。
	Liked bool `gorm:"->"`
}

// TableName 固定表名（GORM 默认复数化成 "posts"，恰好一致，仍显式声明防漂移）。
func (Post) TableName() string { return "posts" }

// PostComment 是评论行。
type PostComment struct {
	ID         int64     `gorm:"column:id;primaryKey"`
	PostID     int64     `gorm:"column:post_id"`
	AuthorID   int64     `gorm:"column:author_id"`
	Content    string    `gorm:"column:content"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	AuthorName string    `gorm:"column:author_name;->"`
}

func (PostComment) TableName() string { return "post_comments" }

// normalizePostInput 校验并归一发帖输入（清洗在 handler 层已做完）。
func normalizePostInput(title, content string, images []string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("store: 帖子标题不能为空")
	}
	if len([]rune(title)) > maxPostTitleLen {
		return fmt.Errorf("store: 帖子标题超过 %d 个字符", maxPostTitleLen)
	}
	if strings.TrimSpace(content) == "" {
		return errors.New("store: 帖子正文不能为空")
	}
	if len([]rune(content)) > maxPostContentLen {
		return fmt.Errorf("store: 帖子正文超过 %d 个字符", maxPostContentLen)
	}
	if len(images) > maxPostImages {
		return fmt.Errorf("store: 图片最多 %d 张", maxPostImages)
	}
	for i, img := range images {
		if len(strings.TrimSpace(img)) > maxPostImageLen {
			return fmt.Errorf("store: 第 %d 条图片地址超过 %d 个字符", i+1, maxPostImageLen)
		}
	}
	return nil
}

// ErrAnimeHasThreads 表示"这个番剧下还有主题串，不能删"（验收第 2 条，handler → 409）。
//
// 为什么它由 store 定义：判据是**数据事实**（posts 里还有没有 anime_id = 这个 id 的行），
// 而 store 是持有那张表的一层。handler 只把它翻译成状态码，不自己去 COUNT ——
// 那会把"删番剧"的准入条件复制到 HTTP 层，两处迟早分叉。
//
// 与数据库上的 `ON DELETE RESTRICT` 是**两层**：这一层给用户可读的 409，
// 那一层保证并发下也不可能留下孤儿主题串（见迁移 0013 的注释）。
var ErrAnimeHasThreads = errors.New("store: 该番剧下还有主题串")

// CreatePost 插入帖子并回填权威行（id / created_at / 计数初值）。
//
// 同步写（票里写"异步单写协程"，这里刻意偏离并说明理由）：响应必须回带 id 与
// createdAt —— 前端发帖后要立刻跳详情页，纯异步拿不到它们。先例是 account.go 的
// CreateUser（同步 + syncWriteReason 注释）：结果本身就是响应的一部分时，同步是唯一
// 诚实的做法。真正该异步的是非响应必要的附带写入（审计、last_seen）。
func (s *Store) CreatePost(ctx context.Context, p *Post) error {
	if p == nil {
		return errors.New("store: CreatePost 收到 nil")
	}
	if p.AuthorID <= 0 {
		return errors.New("store: author_id 不能为空")
	}
	if err := normalizePostInput(p.Title, p.Content, p.Images); err != nil {
		return err
	}
	p.Title = strings.TrimSpace(p.Title)
	if p.Images == nil {
		p.Images = []string{}
	}
	now := time.Now()
	p.CreatedAt = now
	p.UpdatedAt = now
	p.LikeCount = 0
	p.CommentCount = 0
	// last_reply_at 初值 = 发帖时刻（wayfinder #38）。
	//
	// 为什么不是 NULL：在 `sort=latest-reply` 下 NULL 会排到哪一端取决于数据库的
	// NULLS 处理，而"没人回复的新帖"在语义上就是"最近一次有人来 = 发帖那一刻"。
	// 给它一个真实时刻，排序键对每一行都成立，不需要在 SQL 里写 COALESCE。
	p.LastReplyAt = &now

	// 主题串要落进某个板块时，番剧必须存在。
	//
	// 为什么不用"靠外键报错再翻译"：外键错误是 23503，而 store 层已经有一套
	// 唯一冲突的翻译（isUniqueViolation）；为它再加一条 23503 → 语义错误的路径
	// 会让"哪一列的外键"这件事渗进 store。这里显式查一次更直白，而这次查询本身
	// 就是板块列表页要用的同一个索引点（anime 主键）。
	//
	// 竞态窗口（查到了 → 番剧被删 → 插入）由外键兜住：那时插入会因 23503 失败，
	// 而下面把它也翻译成 ErrAnimeNotFound —— 于是"番剧刚刚被删"与"番剧从来不存在"
	// 给用户**同一个**答案（404）。这比"偶尔 500"诚实：两种时序在用户看来是同一件事。
	if p.AnimeID != nil {
		if *p.AnimeID <= 0 {
			return errors.New("store: anime_id 不合法")
		}
		var exists int64
		if err := s.db.WithContext(ctx).Table("anime").Where("id = ?", *p.AnimeID).Count(&exists).Error; err != nil {
			return fmt.Errorf("store: 检查番剧存在性失败: %w", err)
		}
		if exists == 0 {
			return ErrAnimeNotFound
		}
	}

	if err := s.db.WithContext(ctx).Create(p).Error; err != nil {
		// 外键失败（23503）= 上面的 COUNT 与这条 INSERT 之间番剧被删了。
		// 与 ErrAnimeNotFound 收敛成同一个语义错误（见上面的说明）。
		// 注意这里只对"确实是外键冲突"的情况收口，其余错误照旧上抛 —— 否则
		// 一次基础设施故障会被伪装成"番剧不存在"。
		if p.AnimeID != nil && isForeignKeyViolation(err) {
			return ErrAnimeNotFound
		}
		return fmt.Errorf("store: 创建帖子失败: %w", err)
	}
	return nil
}

// ListPostsSort 是列表的排序方式。
type ListPostsSort string

const (
	PostsSortLatest ListPostsSort = "latest" // created_at DESC（默认）
	PostsSortHot    ListPostsSort = "hot"    // (like+comment) DESC, created_at DESC
)

// ListAnimeThreadsSort 是**板块内**主题串列表的排序方式（wayfinder #38）。
//
// 与 ListPostsSort 是两个类型而不是往那个里加两个值：板块的排序键与全站列表
// 不同（前者围绕"这个番剧下的讨论活跃度"，后者围绕"全站信息流"），
// 而共用枚举会让"sort=hot 在板块里是什么"变成一个问题 —— 本票的界面没有它。
type ListAnimeThreadsSort string

const (
	// AnimeThreadsSortLatestReply 是板块默认：最近有人回复的排前面。
	AnimeThreadsSortLatestReply ListAnimeThreadsSort = "latest-reply"
	// AnimeThreadsSortLatest 按发帖时间倒序（与全站列表同口径）。
	AnimeThreadsSortLatest ListAnimeThreadsSort = "latest"
)

// ListPosts 返回一页帖子（带作者显示名）与 hasMore。
//
// 三个刻意的形状决定（对齐前端门面契约 types/posts.ts）：
//
//  1. **不 COUNT 总数**：无限滚动只需要 hasMore。多取一行（limit+1）判断
//     "还有没有"，省掉每页一次全表 COUNT —— 帖子表会长得比 anime 快得多。
//  2. **liked 在这里算**：前端的 `Post.liked` 是必填字段。登录者用**一条**
//     `WHERE user_id = ? AND post_id IN (本页)` 批量取回点赞集合，
//     不是每行一查；匿名请求全部 false。
//  3. 排序同分时按 id 定序：否则同一秒发的两篇帖在翻页边界上会重复或漏行。
func (s *Store) ListPosts(ctx context.Context, sort ListPostsSort, limit, offset int, viewerID int64) ([]Post, bool, error) {
	if limit <= 0 {
		limit = defaultPostLimit
	}
	if limit > maxPostListLimit {
		limit = maxPostListLimit
	}
	if offset < 0 {
		offset = 0
	}

	order := "p.created_at DESC"
	if sort == PostsSortHot {
		order = "(p.like_count + p.comment_count) DESC, p.created_at DESC"
	}

	var rows []Post
	err := s.db.WithContext(ctx).
		Table("posts AS p").
		Select("p.*, u.display_name AS author_name").
		Joins("JOIN users u ON u.id = p.author_id").
		Order(order + ", p.id DESC").
		Limit(limit + 1).Offset(offset). // 多取一行：hasMore 的判据
		Find(&rows).Error
	if err != nil {
		return nil, false, fmt.Errorf("store: 查询帖子列表失败: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	if viewerID > 0 && len(rows) > 0 {
		ids := make([]int64, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
		}
		var likedIDs []int64
		if err := s.db.WithContext(ctx).Table("post_likes").
			Where("user_id = ? AND post_id IN ?", viewerID, ids).
			Pluck("post_id", &likedIDs).Error; err != nil {
			return nil, false, fmt.Errorf("store: 查询点赞状态失败: %w", err)
		}
		set := make(map[int64]struct{}, len(likedIDs))
		for _, id := range likedIDs {
			set[id] = struct{}{}
		}
		for i := range rows {
			_, rows[i].Liked = set[rows[i].ID]
		}
	}
	return rows, hasMore, nil
}

// ListAnimeThreads 返回某个番剧下的一页主题串（带作者显示名）与 hasMore。
//
// 形状与 ListPosts 一致（无限滚动靠 hasMore、liked 在这里批量算），
// 因此前端的板块页可以复用同一个 Post 形状，不需要第二套适配。
//
// 排序口径（验收第 1 条：排序口径与界面一致）：
//
//	latest-reply → last_reply_at DESC NULLS LAST, id DESC（默认，见迁移 0013 的理由）
//	latest       → created_at DESC, id DESC
//
// 两条都走各自的复合索引（posts_anime_last_reply_idx / posts_anime_created_idx），
// 所以 `LIMIT 20` 是一次索引范围扫描而不是"取全量再排"。
//
// 番剧不存在时返回 ErrAnimeNotFound 而不是空列表：「这个板块不存在」与
// 「这个板块还没有主题串」在界面上是两句不同的话，返回空列表会把前者伪装成后者。
func (s *Store) ListAnimeThreads(ctx context.Context, animeID int64, sort ListAnimeThreadsSort, limit, offset int, viewerID int64) ([]Post, bool, error) {
	if animeID <= 0 {
		return nil, false, ErrAnimeNotFound
	}
	if limit <= 0 {
		limit = defaultPostLimit
	}
	if limit > maxPostListLimit {
		limit = maxPostListLimit
	}
	if offset < 0 {
		offset = 0
	}

	if _, err := s.AnimeByID(ctx, animeID); err != nil {
		return nil, false, err
	}

	// ⚠ `NULLS LAST` 不能省：PostgreSQL 在 `DESC` 下默认 NULLS FIRST，
	// 而迁移 0013 之前的老行 last_reply_at 是 NULL（那一版刻意不回填）。
	// 少了它，"给一篇老帖挂上板块"会让它直接跳到板块第一位。
	// 索引也按 NULLS LAST 建（posts_anime_last_reply_idx），口径必须一致 ——
	// 不一致时 PostgreSQL 无法用索引满足排序，退化成"取全量再排"。
	order := "p.last_reply_at DESC NULLS LAST"
	if sort == AnimeThreadsSortLatest {
		order = "p.created_at DESC"
	}

	var rows []Post
	err := s.db.WithContext(ctx).
		Table("posts AS p").
		Select("p.*, u.display_name AS author_name").
		Joins("JOIN users u ON u.id = p.author_id").
		Where("p.anime_id = ?", animeID).
		Order(order + ", p.id DESC").
		Limit(limit + 1).Offset(offset). // 多取一行：hasMore 的判据（同 ListPosts）
		Find(&rows).Error
	if err != nil {
		return nil, false, fmt.Errorf("store: 查询板块主题串失败: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	if viewerID > 0 && len(rows) > 0 {
		ids := make([]int64, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
		}
		var likedIDs []int64
		if err := s.db.WithContext(ctx).Table("post_likes").
			Where("user_id = ? AND post_id IN ?", viewerID, ids).
			Pluck("post_id", &likedIDs).Error; err != nil {
			return nil, false, fmt.Errorf("store: 查询点赞状态失败: %w", err)
		}
		set := make(map[int64]struct{}, len(likedIDs))
		for _, id := range likedIDs {
			set[id] = struct{}{}
		}
		for i := range rows {
			_, rows[i].Liked = set[rows[i].ID]
		}
	}
	return rows, hasMore, nil
}

// CountAnimeThreads 返回某个番剧下的主题串条数（删除前的准入判据）。
//
// 单独一个方法而不是复用 ListAnimeThreads(...).len()：判据是"有没有"，
// 而 `SELECT count(*) WHERE anime_id = ?` 走 posts_anime_last_reply_idx 的部分索引，
// 是几十微秒的事；取一页帖子回来只为了数长度会顺带把 users JOIN 一遍。
func (s *Store) CountAnimeThreads(ctx context.Context, animeID int64) (int64, error) {
	if animeID <= 0 {
		return 0, ErrAnimeNotFound
	}
	var n int64
	if err := s.db.WithContext(ctx).Table("posts").
		Where("anime_id = ?", animeID).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("store: 统计主题串数量失败: %w", err)
	}
	return n, nil
}

// PostByID 取一篇帖子（带作者显示名与 viewer 的点赞态）。查不到返回 ErrPostNotFound。
func (s *Store) PostByID(ctx context.Context, id int64, viewerID int64) (*Post, error) {
	if id <= 0 {
		return nil, ErrPostNotFound
	}
	var row Post
	err := s.db.WithContext(ctx).
		Table("posts AS p").
		Select("p.*, u.display_name AS author_name").
		Joins("JOIN users u ON u.id = p.author_id").
		Where("p.id = ?", id).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPostNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询帖子失败: %w", err)
	}
	if viewerID > 0 {
		var cnt int64
		if err := s.db.WithContext(ctx).Table("post_likes").
			Where("post_id = ? AND user_id = ?", id, viewerID).Count(&cnt).Error; err != nil {
			return nil, fmt.Errorf("store: 查询点赞状态失败: %w", err)
		}
		row.Liked = cnt > 0
	}
	return &row, nil
}

// TogglePostLike 点赞/取消点赞，返回**新状态**（true = 已赞）。
//
// 一个事务里做两件事：改 post_likes 行 + 原子调整 posts.like_count。
// 分开写（先改行、后更新计数）在中途失败时会永久漂移 —— 反范式列的全部代价
// 就在这里（见 DDL 注释）。
//
// 方向判断不靠"先查有没有"：先 DELETE，RowsAffected==0 说明本来没赞，再 INSERT。
// INSERT 撞唯一主键（并发双击的另一半先插了）时按"已赞"收敛 —— 两条路径都收敛到
// 一致状态，不需要额外加锁。
func (s *Store) TogglePostLike(ctx context.Context, postID, userID int64) (bool, error) {
	if postID <= 0 || userID <= 0 {
		return false, ErrPostNotFound
	}

	liked := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 帖子必须存在：否则点赞行会被外键拒绝，但错误信息要可读。
		var exists int64
		if err := tx.Table("posts").Where("id = ?", postID).Count(&exists).Error; err != nil {
			return fmt.Errorf("store: 检查帖子存在性失败: %w", err)
		}
		if exists == 0 {
			return ErrPostNotFound
		}

		del := tx.Exec(`DELETE FROM post_likes WHERE post_id = ? AND user_id = ?`, postID, userID)
		if del.Error != nil {
			return fmt.Errorf("store: 取消点赞失败: %w", del.Error)
		}
		if del.RowsAffected > 0 {
			// 取消成功：计数 -1。GREATEST 兜底：CHECK >= 0 会让"减成负数"直接报错，
			// 而报错的代价是**整个事务回滚**（连删除也撤销），用户会永远卡在"取消不了"。
			if err := tx.Exec(`UPDATE posts SET like_count = GREATEST(like_count - 1, 0),
				updated_at = now() WHERE id = ?`, postID).Error; err != nil {
				return fmt.Errorf("store: 更新点赞计数失败: %w", err)
			}
			liked = false
			return nil
		}

		ins := tx.Exec(`INSERT INTO post_likes (post_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, postID, userID)
		if ins.Error != nil {
			return fmt.Errorf("store: 点赞失败: %w", ins.Error)
		}
		if ins.RowsAffected > 0 {
			if err := tx.Exec(`UPDATE posts SET like_count = like_count + 1,
				updated_at = now() WHERE id = ?`, postID).Error; err != nil {
				return fmt.Errorf("store: 更新点赞计数失败: %w", err)
			}
		}
		// RowsAffected==0：并发对手刚插入。状态仍是"已赞"，计数由对方事务负责加。
		liked = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return liked, nil
}

// ListPostComments 返回一篇帖子的评论分页（老→新）与总数。
func (s *Store) ListPostComments(ctx context.Context, postID int64, limit, offset int) ([]PostComment, int64, error) {
	if limit <= 0 {
		limit = defaultPostLimit
	}
	if limit > maxPostListLimit {
		limit = maxPostListLimit
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&PostComment{}).
		Where("post_id = ?", postID).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("store: 统计评论数量失败: %w", err)
	}

	var rows []PostComment
	err := s.db.WithContext(ctx).
		Table("post_comments AS c").
		Select("c.*, u.display_name AS author_name").
		Joins("JOIN users u ON u.id = c.author_id").
		Where("c.post_id = ?", postID).
		Order("c.created_at ASC, c.id ASC").
		Limit(limit).Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询评论列表失败: %w", err)
	}
	return rows, total, nil
}

// AddPostComment 插入评论并把 comment_count +1（同一事务，理由同 TogglePostLike）。
func (s *Store) AddPostComment(ctx context.Context, c *PostComment) error {
	if c == nil {
		return errors.New("store: AddPostComment 收到 nil")
	}
	if c.PostID <= 0 || c.AuthorID <= 0 {
		return ErrPostNotFound
	}
	content := strings.TrimSpace(c.Content)
	if content == "" {
		return errors.New("store: 评论内容不能为空")
	}
	if len([]rune(content)) > maxCommentLen {
		return fmt.Errorf("store: 评论超过 %d 个字符", maxCommentLen)
	}
	c.Content = content

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exists int64
		if err := tx.Table("posts").Where("id = ?", c.PostID).Count(&exists).Error; err != nil {
			return fmt.Errorf("store: 检查帖子存在性失败: %w", err)
		}
		if exists == 0 {
			return ErrPostNotFound
		}
		c.CreatedAt = time.Now()
		if err := tx.Create(c).Error; err != nil {
			return fmt.Errorf("store: 创建评论失败: %w", err)
		}
		// comment_count 与 last_reply_at 在**同一条 UPDATE** 里更新（wayfinder #38）：
		// 两个都是反范式列，分成两条语句只会多一个"前半成功后半失败"的窗口，
		// 而它们本来就由同一个事实（多了一条评论）驱动。
		if err := tx.Exec(`UPDATE posts SET comment_count = comment_count + 1,
			last_reply_at = now(), updated_at = now() WHERE id = ?`, c.PostID).Error; err != nil {
			return fmt.Errorf("store: 更新评论计数失败: %w", err)
		}
		return nil
	})
}

// DeletePostComment 删除评论并把 comment_count -1；actor 不是作者时返回
// ErrCommentForbidden（服务端判，I3）。
func (s *Store) DeletePostComment(ctx context.Context, commentID, actorID int64) error {
	if commentID <= 0 {
		return ErrCommentNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row PostComment
		err := tx.Where("id = ?", commentID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCommentNotFound
		}
		if err != nil {
			return fmt.Errorf("store: 查询评论失败: %w", err)
		}
		if row.AuthorID != actorID {
			return ErrCommentForbidden
		}
		if err := tx.Delete(&PostComment{}, commentID).Error; err != nil {
			return fmt.Errorf("store: 删除评论失败: %w", err)
		}
		// 同 TogglePostLike：GREATEST 防"减成负数触发 CHECK → 整个事务回滚"。
		//
		// **last_reply_at 刻意不回退**（wayfinder #38）：回退要聚合"剩下的最新一条评论"，
		// 而删评论是幂等的轻动作。完整理由见迁移 0013 的注释 —— 一句话：
		// 排序键偏新的代价是零，引入一次聚合 + 一个漂移窗口的代价不是。
		return tx.Exec(`UPDATE posts SET comment_count = GREATEST(comment_count - 1, 0),
			updated_at = now() WHERE id = ?`, row.PostID).Error
	})
}
