package store

import (
	"context"
	"errors"
	"testing"
)

// TestAnimeJSONBRoundTrip 验证 aliases 在 []string 与 jsonb 之间的往返，
// 以及 title 唯一约束被翻译成 ErrAnimeTitleTaken。
//
// 为什么这两条值得单独测：`serializer:json` 是 GORM 的隐式行为，
// 一旦驱动或 GORM 版本变化导致它对 jsonb 列的写/读不对称，症状是
// "写进去是空的 / 读出来是 null"，而列表页会静默少掉别名而不是报错。
func TestAnimeJSONBRoundTrip(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	title := "单元测试番剧"
	alias := []string{"unit alias", "单元别名"}
	cover := "https://example.com/a.jpg"
	row := &Anime{Title: title, Aliases: alias, CoverURL: &cover, Summary: "简介"}
	if err := s.CreateAnime(ctx, row); err != nil {
		t.Fatalf("CreateAnime: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteAnime(ctx, row.ID) })

	if row.ID <= 0 {
		t.Fatalf("CreateAnime 未回填 id: %+v", row)
	}

	got, err := s.AnimeByID(ctx, row.ID)
	if err != nil {
		t.Fatalf("AnimeByID: %v", err)
	}
	if len(got.Aliases) != len(alias) || got.Aliases[0] != alias[0] || got.Aliases[1] != alias[1] {
		t.Fatalf("aliases 往返不一致: got %v want %v", got.Aliases, alias)
	}
	if got.CoverURL == nil || *got.CoverURL != cover {
		t.Fatalf("cover_url 往返不一致: %v", got.CoverURL)
	}

	// 唯一约束 → 语义错误（handler 据此返回 409）
	dup := &Anime{Title: title}
	err = s.CreateAnime(ctx, dup)
	if !errors.Is(err, ErrAnimeTitleTaken) {
		t.Fatalf("重复标题期望 ErrAnimeTitleTaken，得到 %v", err)
	}
}

// TestAnimePartialUpdateKeepsUntouchedFields 钉住 PATCH 的局部语义。
//
// 这条用例的由来是一个真 bug：handler 曾经无条件把 `&req.CoverURL` 传给
// UpdateAnime，于是"客户端没传 coverUrl"被当成"显式清空封面" —— 症状是
// "只改简介，封面消失"。store 层的契约是"参数为 nil 就不动这一列"，
// 用例把这个契约钉死，避免将来又有人在调用方把 nil 包成非 nil 的地址。
func TestAnimePartialUpdateKeepsUntouchedFields(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	title := "局部更新番剧"
	alias := []string{"alias-keep"}
	cover := "https://example.com/keep.jpg"
	row := &Anime{Title: title, Aliases: alias, CoverURL: &cover, Summary: "初始简介"}
	if err := s.CreateAnime(ctx, row); err != nil {
		t.Fatalf("CreateAnime: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteAnime(ctx, row.ID) })

	// 只改 summary：title / aliases / coverUrl 一律传 nil（"不动"）
	newSummary := "新简介"
	updated, err := s.UpdateAnime(ctx, row.ID, nil, nil, nil, &newSummary)
	if err != nil {
		t.Fatalf("UpdateAnime: %v", err)
	}
	if updated.Summary != newSummary {
		t.Fatalf("summary 未更新: %q", updated.Summary)
	}
	if updated.CoverURL == nil || *updated.CoverURL != cover {
		t.Fatalf("只改 summary 却动了 cover_url: %v", updated.CoverURL)
	}
	if len(updated.Aliases) != 1 || updated.Aliases[0] != alias[0] {
		t.Fatalf("只改 summary 却动了 aliases: %v", updated.Aliases)
	}

	// 显式清空封面。注意 coverURL 是 `**string`，三态语义：
	//   nil              → 不动这一列
	//   &指向 nil 的指针 → 清空（存 NULL）
	//   &指向 "http..."  → 改成该值
	empty := ""
	emptyPtr := &empty
	cleared, err := s.UpdateAnime(ctx, row.ID, nil, nil, &emptyPtr, nil)
	if err != nil {
		t.Fatalf("UpdateAnime(清空封面): %v", err)
	}
	if cleared.CoverURL != nil {
		t.Fatalf("空串应折成 NULL，得到 %v", *cleared.CoverURL)
	}
}

// TestAnimeListSearchMatchesTitleAndAlias 验证 ?q= 同时匹配标题与别名。
//
// 别名用 jsonb 存，搜索靠 `aliases::text ILIKE`；这条用例防的是
// "转成 text 之后的形状与预期不同"（例如带引号导致匹配不到）这类静默失效。
func TestAnimeListSearchMatchesTitleAndAlias(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	a := &Anime{Title: "SearchTargetTitle", Aliases: []string{"alias-only-token"}}
	b := &Anime{Title: "AnotherEntry", Aliases: []string{"unrelated"}}
	for _, row := range []*Anime{a, b} {
		if err := s.CreateAnime(ctx, row); err != nil {
			t.Fatalf("CreateAnime(%s): %v", row.Title, err)
		}
	}
	t.Cleanup(func() {
		_ = s.DeleteAnime(ctx, a.ID)
		_ = s.DeleteAnime(ctx, b.ID)
	})

	byTitle, totalTitle, err := s.ListAnimes(ctx, "SearchTargetTitle", 10, 0)
	if err != nil {
		t.Fatalf("ListAnimes(title): %v", err)
	}
	if totalTitle == 0 || byTitle[0].ID != a.ID {
		t.Fatalf("按标题搜索未命中: total=%d", totalTitle)
	}

	byAlias, totalAlias, err := s.ListAnimes(ctx, "alias-only-token", 10, 0)
	if err != nil {
		t.Fatalf("ListAnimes(alias): %v", err)
	}
	if totalAlias == 0 || byAlias[0].ID != a.ID {
		t.Fatalf("按别名搜索未命中: total=%d", totalAlias)
	}
}
