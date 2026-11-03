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

// ErrPostForbidden 表示"这篇帖子不是你的"（票 #70 的删帖）。
//
// 与 ErrCommentForbidden 同一套理由：判据是数据事实（author_id == 请求者），
// 落在持有那张表的一层；handler 只把它翻译成 403。**不是**在 handler 里
// 先查一次再判断 —— 那会把"谁有权删"复制到 HTTP 层，两处迟早分叉。
var ErrPostForbidden = errors.New("store: 只能删除自己的帖子")

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

	// AuthorAvatarURL 同样是 JOIN 产物：作者的像素头像（票 #74）；NULL = 没有。
	//
	// 与 AuthorName 一起取出而不是让前端另打一发：列表里每一行都要头像，
	// 分开取就是 N 次请求，而那个 N 恰好等于"这一页有几条"。
	// 注销账号这里恒为 NULL（`maskedAvatarSQL` 遮掉了）—— 照片比名字更能
	// 指认一个人，署名脱敏了却还留着照片等于只遮了一半。
	AuthorAvatarURL *string `gorm:"column:author_avatar_url;->"`

	// Liked 同样是查询时算出的**只读**字段（前端 Post.liked 是必填形状）。
	Liked bool `gorm:"->"`

	// Favorited 是 viewer 是否把这篇收进了私人书签（票 #64）。
	//
	// 与 Liked **同一批查询**回填（见 ListPosts / PostByID）：两个布尔读的是
	// 两张表，但"每个列表页都要知道这两件事"是同一个需求，分两次循环会让
	// "以后再加一个布尔"变成再加一次全表扫。它**没有对应的公开计数** ——
	// 收藏是私人书签（决议 #48 §3），所以只有这一个布尔，没有 favorite_count。
	Favorited bool `gorm:"->"`
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
	// AuthorAvatarURL 见 Post.AuthorAvatarURL（票 #74）。
	AuthorAvatarURL *string `gorm:"column:author_avatar_url;->"`
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

	// PostsSortFollowing 是"只看我关注的人"（决议 #52 §1-§2 / 票 #66）。
	//
	// 排序**严格按时间倒序**，与 latest 同口径：不做热度加权、不搞"智能排序"——
	// 加权参数要调，而空窗期会让流整天不更新；时间线永远可预测。
	//
	// 它需要登录（`ErrFlowNeedsViewer`）：匿名视角下"我关注的人"没有意义。
	PostsSortFollowing ListPostsSort = "following"
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

	db := s.db.WithContext(ctx).
		Table("posts AS p").
		Select("p.*, " + maskedDisplayNameSQL("u") + " AS author_name, " +
			maskedAvatarSQL("u") + " AS author_avatar_url").
		Joins("JOIN users u ON u.id = p.author_id").
		Order(order + ", p.id DESC")

	// 关注流（票 #66）：先缩到"我关注的人"，再统一过可见性谓词。
	//
	// 两件事分开调是刻意的（见 flow.go 的注释）：`applyFollowingScope` 回答
	// "要不要只挑这一批"，`applyAuthorVisibility` 回答"这批里谁能被看见"。
	// 合成一个函数会让他人档案页（#68）无处复用后半段。
	if sort == PostsSortFollowing {
		var err error
		db, err = applyFollowingScope(db, viewerID, "p.author_id")
		if err != nil {
			return nil, false, err
		}
		db = s.applyAuthorVisibility(db, viewerID, "p.author_id")
	}

	var rows []Post
	err := db.
		Limit(limit + 1).Offset(offset). // 多取一行：hasMore 的判据
		Find(&rows).Error
	if err != nil {
		return nil, false, fmt.Errorf("store: 查询帖子列表失败: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	if err := s.fillPostInteractionFlags(ctx, viewerID, rows); err != nil {
		return nil, false, err
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
		Select("p.*, "+maskedDisplayNameSQL("u")+" AS author_name, "+
			maskedAvatarSQL("u")+" AS author_avatar_url").
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
		favSet, err := s.favoritedPostIDs(ctx, viewerID, ids)
		if err != nil {
			return nil, false, err
		}
		for i := range rows {
			_, rows[i].Liked = set[rows[i].ID]
			_, rows[i].Favorited = favSet[rows[i].ID]
		}
	}
	return rows, hasMore, nil
}

// ListPostsByAuthor 返回某个作者的一页公开帖（票 #68 的他人档案页）。
//
// 与 ListPosts 的关系：**同一套形状，只多一条"按作者筛"**。刻意不合并成一个
// `ListPosts(…, authorID)` —— 那会让信息流与档案页共用一个参数位，而两者的
// 准入完全不同（信息流分 sort 枚举，档案页永远按时间倒序；档案页还多一道
// "这个人本身可不可见"的判断）。共用一个函数会让下一个改 sort 的人
// 不得不先读懂档案页为什么传了 sort 却被忽略。
//
// 三条口径：
//
//  1. **可见性走 applyAuthorVisibility**（与关注流同一个实现，见 flow.go）：
//     作者被封禁 → 一条都不给；看客与作者任一方拉黑 → 一条都不给。
//     这是"档案 404 但子资源还能读到"那条泄漏的防线。
//  2. **时间倒序**（created_at DESC, id DESC）：与 `/api/posts?sort=latest` 同口径。
//  3. **liked / favorited 在这里批量算**（同 ListPosts）：前端复用同一个 Post 形状。
//
// authorID <= 0 时返回空列表而不是报错：调用方（handler）已经在更早的一步
// 校验过 id 的合法性，这里再报一次错只会多一条不可达的分支。
func (s *Store) ListPostsByAuthor(ctx context.Context, authorID int64, limit, offset int, viewerID int64) ([]Post, bool, error) {
	if authorID <= 0 {
		return []Post{}, false, nil
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

	db := s.db.WithContext(ctx).
		Table("posts AS p").
		Select("p.*, "+maskedDisplayNameSQL("u")+" AS author_name, "+
			maskedAvatarSQL("u")+" AS author_avatar_url").
		Joins("JOIN users u ON u.id = p.author_id").
		Where("p.author_id = ?", authorID).
		Order("p.created_at DESC, p.id DESC")
	db = s.applyAuthorVisibility(db, viewerID, "p.author_id")

	var rows []Post
	if err := db.Limit(limit + 1).Offset(offset).Find(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("store: 查询作者帖列表失败: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	if err := s.fillPostInteractionFlags(ctx, viewerID, rows); err != nil {
		return nil, false, err
	}
	return rows, hasMore, nil
}

// fillPostInteractionFlags 给一页帖子补 liked / favorited（viewerID <= 0 时全 false）。
//
// 抽出来的理由与 favoritedPostIDs 相同：这是**第三处**做同一件事的地方
// （ListPosts / ListAnimeThreads / ListPostsByAuthor），而"哪些行亮着"的口径
// 必须只有一份 —— 否则加一个新列表就会漏掉收藏位。
func (s *Store) fillPostInteractionFlags(ctx context.Context, viewerID int64, rows []Post) error {
	if viewerID <= 0 || len(rows) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	var likedIDs []int64
	if err := s.db.WithContext(ctx).Table("post_likes").
		Where("user_id = ? AND post_id IN ?", viewerID, ids).
		Pluck("post_id", &likedIDs).Error; err != nil {
		return fmt.Errorf("store: 查询点赞状态失败: %w", err)
	}
	set := make(map[int64]struct{}, len(likedIDs))
	for _, id := range likedIDs {
		set[id] = struct{}{}
	}
	favSet, err := s.favoritedPostIDs(ctx, viewerID, ids)
	if err != nil {
		return err
	}
	for i := range rows {
		_, rows[i].Liked = set[rows[i].ID]
		_, rows[i].Favorited = favSet[rows[i].ID]
	}
	return nil
}

// favoritedPostIDs 是 liked 批查询的收藏版：一次问清"这 N 篇里哪些已收藏"。
//
// 抽成一个函数而不是在三处（信息流列表 / 板块主题串 / 详情）各写一遍：
// 三处的 ids 来源不同，但"读哪张表、用什么口径"必须只有一份 ——
// 否则改口径时漏掉一处的症状是"列表里亮着、点进详情变灰"。
func (s *Store) favoritedPostIDs(ctx context.Context, viewerID int64, postIDs []int64) (map[int64]struct{}, error) {
	out := make(map[int64]struct{}, len(postIDs))
	if viewerID <= 0 || len(postIDs) == 0 {
		return out, nil
	}
	raw := make([]string, 0, len(postIDs))
	for _, id := range postIDs {
		raw = append(raw, strconv.FormatInt(id, 10))
	}
	states, err := s.FavoriteStatesFor(ctx, viewerID, FavoritePost, raw)
	if err != nil {
		return nil, err
	}
	for _, id := range postIDs {
		if states[strconv.FormatInt(id, 10)] {
			out[id] = struct{}{}
		}
	}
	return out, nil
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
		Select("p.*, "+maskedDisplayNameSQL("u")+" AS author_name, "+
			maskedAvatarSQL("u")+" AS author_avatar_url").
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
		rows := []Post{row}
		if err := s.fillPostInteractionFlags(ctx, viewerID, rows); err != nil {
			return nil, err
		}
		row = rows[0]
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
		Select("c.*, "+maskedDisplayNameSQL("u")+" AS author_name, "+
			maskedAvatarSQL("u")+" AS author_avatar_url").
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

// DeletePost 删除一篇帖子；actor 不是作者时返回 ErrPostForbidden（票 #70）。
//
// # 为什么是硬删而不是软删
//
// 用户删自己的帖是"我发错了 / 不想留了"，软删只会让列表多一个"你看不到但
// 数据库里还在"的状态，而正文仍然躺在库里 —— 对一个说"删掉"的人这是欺骗。
// 软删真正需要的场景是**账号注销的冷静期**（决议 #51：30 天内可撤销），
// 那里软删的是 users 行，与这里无关。
//
// # 子行怎么清
//
// 靠数据库的 ON DELETE CASCADE（迁移 0010：post_comments / post_likes 的外键），
// **不在这里手写 DELETE**。手写会漏掉将来新增的子表，而漏掉的症状是
// "帖删了、评论还在"，且没有任何报错。
//
// # 一个刻意的例外：收藏条目保留
//
// favorites 表**故意没有外键**（迁移 0016 的注释），所以删帖不会带走收藏行。
// 这不是疏漏，是决议 #48 §5 的语义：对象失效时条目保留、列表显示失效态、
// 由用户自行移除 —— "我收藏过的东西后来没了"本身是用户需要知道的信息。
// store/post_delete_test.go 的 TestDeletePostKeepsFavoritesEntry 钉住它。
//
// # 为什么先查作者再删，而不是 DELETE ... WHERE id=? AND author_id=?
//
// 一条带条件的 DELETE 无法区分"帖子不存在"与"帖子存在但不是你的"：两者都
// RowsAffected==0。而这两件事必须回不同的状态码（404 vs 403）—— 把"别人的
// 帖子"回成 404 看似更安全，但它会让"我的帖子哪去了"变成一次无解的排查。
// 同一事务里先 Take 再判，多一次主键查询，换一个确定的错误。
func (s *Store) DeletePost(ctx context.Context, postID, actorID int64) error {
	if postID <= 0 {
		return ErrPostNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Post
		err := tx.Select("id, author_id").Where("id = ?", postID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPostNotFound
		}
		if err != nil {
			return fmt.Errorf("store: 查询帖子失败: %w", err)
		}
		if row.AuthorID != actorID {
			return ErrPostForbidden
		}
		if err := tx.Delete(&Post{}, postID).Error; err != nil {
			return fmt.Errorf("store: 删除帖子失败: %w", err)
		}
		return nil
	})
}
