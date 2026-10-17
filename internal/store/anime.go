package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// anime 目录的长度上限。**必须与 DDL 里的列上限、handler 的校验同值**：
// 三处不一致时的症状是"表单能填、点保存报 500"，排查要从 HTTP 一路追到 SQL 层。
// （同一条纪律见 roommeta.go 的 maxRoomTitleLen 与 rooms_meta.title。）
const (
	maxAnimeTitleLen    = 120
	maxAnimeAliasLen    = 60
	maxAnimeAliases     = 10
	maxAnimeCoverURLLen = 512
	maxAnimeSummaryLen  = 2000
)

// ErrAnimeNotFound 表示按 id 查不到条目。
var ErrAnimeNotFound = errors.New("store: 番剧条目不存在")

// ErrAnimeTitleTaken 表示标题重复（DDL 上 title 有唯一约束）。
//
// 为什么把它翻译成一个**语义错误**而不是把 pq 的唯一冲突直接抛上去：
// handler 要据此返回 409，而"哪一列冲突了"只有 store 知道（SQL 文本是它写的）。
// 让 handler 去匹配错误字符串，等于把 SQL 细节泄漏到 HTTP 层。
var ErrAnimeTitleTaken = errors.New("store: 番剧标题已存在")

// Anime 是目录条目。
//
// Aliases 用 `serializer:json` 交给 GORM 在 Go 的 []string 与列上的 jsonb 之间转换：
// 这比自定义 driver.Valuer 少一个类型，也避免"传 string 给 jsonb 列"在参数类型推断上
// 的模糊（不同驱动对 text→jsonb 的隐式转换处理不一致）。
type Anime struct {
	ID        int64     `gorm:"column:id;primaryKey"`
	Title     string    `gorm:"column:title"`
	Aliases   []string  `gorm:"column:aliases;serializer:json;type:jsonb"`
	CoverURL  *string   `gorm:"column:cover_url"`
	Summary   string    `gorm:"column:summary"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName 固定表名（GORM 默认会复数化成 "animes"，而 DDL 里是 anime）。
func (Anime) TableName() string { return "anime" }

// normalizeAnime 做 store 层的边界校验与归一。
//
// 它**不**做内容清洗（去零宽/Bidi 是 service.StripControl 的职责，HTTP 层调用）：
// 各自只做自己那一层的事，重复清洗会让"到底谁负责"变得含糊。
func normalizeAnime(a *Anime) error {
	if a == nil {
		return errors.New("store: anime 收到 nil")
	}
	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" {
		return errors.New("store: 番剧标题不能为空")
	}
	if len([]rune(a.Title)) > maxAnimeTitleLen {
		return fmt.Errorf("store: 番剧标题超过 %d 个字符", maxAnimeTitleLen)
	}
	if a.Aliases == nil {
		a.Aliases = []string{}
	}
	if len(a.Aliases) > maxAnimeAliases {
		return fmt.Errorf("store: 别名最多 %d 个", maxAnimeAliases)
	}
	for i, alias := range a.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return errors.New("store: 别名不能是空串")
		}
		if len([]rune(alias)) > maxAnimeAliasLen {
			return fmt.Errorf("store: 第 %d 个别名超过 %d 个字符", i+1, maxAnimeAliasLen)
		}
		a.Aliases[i] = alias
	}
	if a.CoverURL != nil {
		u := strings.TrimSpace(*a.CoverURL)
		if u == "" {
			// 空串等价于"没有封面"：存 NULL，而不是让 "" 与 NULL 两种"没有"并存。
			a.CoverURL = nil
		} else {
			if len(u) > maxAnimeCoverURLLen {
				return fmt.Errorf("store: 封面地址超过 %d 个字符", maxAnimeCoverURLLen)
			}
			a.CoverURL = &u
		}
	}
	if len([]rune(a.Summary)) > maxAnimeSummaryLen {
		return fmt.Errorf("store: 简介超过 %d 个字符", maxAnimeSummaryLen)
	}
	a.Summary = strings.TrimSpace(a.Summary)
	return nil
}

// ListAnimes 返回目录的一页（按创建时间倒序）与总数。
//
// q 非空时按 **title 或 aliases** 做大小写不敏感的子串匹配。aliases 是 jsonb，
// 转成 text 再匹配是刻意的：别名是给搜索用的，不是结构化的查询维度，
// 为它建 GIN 索引的成本在当前表规模下换不到收益（见 DDL 注释）。
func (s *Store) ListAnimes(ctx context.Context, q string, limit, offset int) ([]Anime, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	tx := s.db.WithContext(ctx).Model(&Anime{})
	if kw := strings.TrimSpace(q); kw != "" {
		// 转义 LIKE 的通配符：用户输入的 % 与 _ 是**字面字符**，不是通配符。
		// 不转义时搜 "a%" 会匹配到一切以 a 开头的东西，看起来像搜索坏了。
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(kw)
		pattern := "%" + esc + "%"
		tx = tx.Where(`title ILIKE ? OR aliases::text ILIKE ?`, pattern, pattern)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("store: 统计番剧数量失败: %w", err)
	}

	var rows []Anime
	if err := tx.Order("created_at DESC, id DESC").
		Limit(limit).Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("store: 查询番剧列表失败: %w", err)
	}
	return rows, total, nil
}

// AnimeByID 按主键取一条。查不到返回 ErrAnimeNotFound。
func (s *Store) AnimeByID(ctx context.Context, id int64) (*Anime, error) {
	if id <= 0 {
		return nil, ErrAnimeNotFound
	}
	var row Anime
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAnimeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查询番剧失败: %w", err)
	}
	return &row, nil
}

// CreateAnime 插入一条并回填 id / created_at / updated_at。
//
// 同步写：响应要把权威行（含 id 与时间）回给调用方 —— 与 account.go 的 CreateUser
// 同一条理由（结果本身就是响应的一部分）。非响应必要的附带写入（审计）才走异步队列。
func (s *Store) CreateAnime(ctx context.Context, a *Anime) error {
	if err := normalizeAnime(a); err != nil {
		return err
	}
	now := time.Now()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = a.CreatedAt

	err := s.db.WithContext(ctx).Create(a).Error
	if isUniqueViolation(err) {
		return ErrAnimeTitleTaken
	}
	if err != nil {
		return fmt.Errorf("store: 创建番剧失败: %w", err)
	}
	return nil
}

// UpdateAnime 局部更新：只写调用方明确给了的字段（nil = 不动）。
//
// `coverURL` 是三态指针 `**string`，三种取值含义不同：
//
//	nil                 → 不动 cover_url
//	&p，其中 *p == nil  → 把 cover_url 置 NULL（清空封面）
//	&p，其中 *p != nil  → 改成 *p（空串会被归一成 NULL）
//
// 为什么要三态而不是"传空串表示清空"：**"没传"与"传了空"必须是两件事**。
// 合成两态后，"只改简介"就会顺手把封面清掉 —— 这个 bug 真的发生过（见
// anime_test.go 的 TestAnimePartialUpdateKeepsUntouchedFields）。
// 调用方必须用「传不传 `&req.CoverURL`」来表达"有没有传"，不能无条件取地址。
//
// 用 map 做 Updates 而不是整体 Save：整体保存会把没传的字段写成零值，
// 于是一次"只改简介"会把别名和封面清掉 —— 那是数据丢失，不是更新。
func (s *Store) UpdateAnime(ctx context.Context, id int64, title *string, aliases *[]string, coverURL **string, summary *string) (*Anime, error) {
	if id <= 0 {
		return nil, ErrAnimeNotFound
	}

	updates := map[string]any{"updated_at": time.Now()}

	// 复用 normalizeAnime 的逐项校验：把"给了的值"先塞进一个临时对象里过一遍，
	// 避免在校验代码里维护第二套规则（两套规则迟早会分叉）。
	probe := &Anime{Title: "probe"}
	if title != nil {
		probe.Title = *title
	}
	if aliases != nil {
		probe.Aliases = *aliases
	}
	if coverURL != nil {
		probe.CoverURL = *coverURL
	}
	if summary != nil {
		probe.Summary = *summary
	}
	if err := normalizeAnime(probe); err != nil {
		return nil, err
	}

	if title != nil {
		updates["title"] = probe.Title
	}
	if aliases != nil {
		updates["aliases"] = probe.Aliases
	}
	if coverURL != nil {
		// 传空串 = 清空封面（normalizeAnime 已把空串折成 nil）。
		updates["cover_url"] = probe.CoverURL
	}
	if summary != nil {
		updates["summary"] = probe.Summary
	}

	res := s.db.WithContext(ctx).Model(&Anime{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return nil, ErrAnimeTitleTaken
		}
		return nil, fmt.Errorf("store: 更新番剧失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// 两种可能：id 不存在，或者提交的值与库里完全一致（PG 的 UPDATE 会把
		// 未变更计为 0 行）。用一次存在性查询区分，否则"没改动"会被误报成 404。
		if _, err := s.AnimeByID(ctx, id); err != nil {
			return nil, err
		}
	}
	return s.AnimeByID(ctx, id)
}

// DeleteAnime 硬删一条。返回 ErrAnimeNotFound 表示本来就没有。
func (s *Store) DeleteAnime(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrAnimeNotFound
	}
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&Anime{})
	if res.Error != nil {
		return fmt.Errorf("store: 删除番剧失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrAnimeNotFound
	}
	return nil
}
