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

	if err := s.db.WithContext(ctx).Create(p).Error; err != nil {
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
		if err := tx.Exec(`UPDATE posts SET comment_count = comment_count + 1,
			updated_at = now() WHERE id = ?`, c.PostID).Error; err != nil {
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
		return tx.Exec(`UPDATE posts SET comment_count = GREATEST(comment_count - 1, 0),
			updated_at = now() WHERE id = ?`, row.PostID).Error
	})
}
