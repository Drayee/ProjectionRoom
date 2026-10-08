package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// auditDetailOf 读回某一行的 detail 文本形态（jsonb 在 DB 侧的渲染结果）。
func auditDetailOf(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var detail string
	if err := s.DB().Raw(`SELECT detail::text FROM admin_audit WHERE id = ?`, id).Scan(&detail).Error; err != nil {
		t.Fatalf("读审计明细失败：%v", err)
	}
	return detail
}

// TestInsertAuditDetailNormalization 覆盖 detail 的三条规范化规则，
// 并断言落库后是**合法 JSON**（列类型是 jsonb，写不合法内容会直接失败）。
func TestInsertAuditDetailNormalization(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		in      string
		wantRaw any
	}{
		{name: "空串变成空对象", in: "", wantRaw: map[string]any{}},
		{name: "合法对象原样透传", in: ` {"reason":"刷屏","count":2} `, wantRaw: map[string]any{"reason": "刷屏", "count": float64(2)}},
		{name: "数组透传", in: `["a","b"]`, wantRaw: []any{"a", "b"}},
		{name: "普通文本按字符串值编码", in: "封禁理由：刷屏", wantRaw: "封禁理由：刷屏"},
		{name: "看着像 JSON 但非法 → 按字符串", in: `{"broken":`, wantRaw: `{"broken":`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &AdminAudit{
				ActorID:    1,
				Action:     "user.ban",
				TargetType: "user",
				TargetID:   "42",
				Detail:     c.in,
			}
			if err := s.InsertAudit(ctx, a); err != nil {
				t.Fatalf("写审计失败：%v", err)
			}
			if a.ID == 0 {
				t.Fatalf("写成功后应回填自增主键")
			}
			if a.CreatedAt.IsZero() {
				t.Fatalf("created_at 应由 GORM/DDL 填上")
			}

			raw := auditDetailOf(t, s, a.ID)
			if !json.Valid([]byte(raw)) {
				t.Fatalf("落库的 detail 必须是合法 JSON，实际 %q", raw)
			}
			var got any
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("解码 detail 失败：%v（原始 %q）", err, raw)
			}
			if !sameJSON(got, c.wantRaw) {
				t.Fatalf("detail 落库内容不对：want=%#v got=%#v（原始 %q）", c.wantRaw, got, raw)
			}
			// 内存里的字段也应被改成规范化后的形态，避免调用方以为写进去的是原样。
			if !json.Valid([]byte(a.Detail)) {
				t.Fatalf("InsertAudit 应把 a.Detail 改成规范化 JSON，实际 %q", a.Detail)
			}
		})
	}
}

// sameJSON 做 JSON 值的语义比较（jsonb 会重排键序与空白，字符串比较不可靠）。
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ja) == string(jb)
}

func TestInsertAuditValidationAndNormalization(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		audit *AdminAudit
		want  string
	}{
		{"nil", nil, "nil"},
		{"缺 actor_id", &AdminAudit{Action: "user.ban", TargetType: "user", TargetID: "1"}, "actor_id"},
		{"缺 action", &AdminAudit{ActorID: 1, TargetType: "user", TargetID: "1"}, "action"},
		{"缺 target_type", &AdminAudit{ActorID: 1, Action: "user.ban", TargetID: "1"}, "target_type"},
		{"缺 target_id", &AdminAudit{ActorID: 1, Action: "user.ban", TargetType: "user"}, "target_id"},
		{"action 只有空白", &AdminAudit{ActorID: 1, Action: "   ", TargetType: "user", TargetID: "1"}, "action"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.InsertAudit(ctx, c.audit)
			if err == nil {
				t.Fatalf("应该报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", c.want, err)
			}
		})
	}

	// 空 IP 写 NULL；有值的去掉控制符/空白。
	empty := "   "
	a := &AdminAudit{ActorID: 1, Action: "  user.ban  ", TargetType: " user ", TargetID: " 42 ", IP: &empty}
	if err := s.InsertAudit(ctx, a); err != nil {
		t.Fatalf("写审计失败：%v", err)
	}
	if a.IP != nil {
		t.Fatalf("空白 IP 应该被归一化成 NULL，实际 %q", *a.IP)
	}
	if a.Action != "user.ban" || a.TargetType != "user" || a.TargetID != "42" {
		t.Fatalf("文本字段应去掉首尾空白：%+v", a)
	}

	ip := "198.51.100.9"
	b := &AdminAudit{ActorID: 2, Action: "user.role", TargetType: "user", TargetID: "7", IP: &ip}
	if err := s.InsertAudit(ctx, b); err != nil {
		t.Fatalf("写审计失败：%v", err)
	}
	rows, err := s.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatalf("查审计失败：%v", err)
	}
	if rows[0].IP == nil || *rows[0].IP != ip {
		t.Fatalf("ip 往返失败：%v", rows[0].IP)
	}
	// 审计表没有 actor_id 外键：actor 不存在也写得进去（审计要活过用户删除）。
	if err := s.InsertAudit(ctx, &AdminAudit{ActorID: 999999, Action: "user.delete", TargetType: "user", TargetID: "7"}); err != nil {
		t.Fatalf("actor 不存在时审计仍应能落库（表上刻意没有外键）：%v", err)
	}
}

func TestListAuditNewestFirstAndPaging(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()

	ids := make([]int64, 0, 3)
	for _, action := range []string{"user.ban", "user.role", "room.close"} {
		a := &AdminAudit{ActorID: 1, Action: action, TargetType: "user", TargetID: "1"}
		if err := s.InsertAudit(ctx, a); err != nil {
			t.Fatalf("写审计失败：%v", err)
		}
		ids = append(ids, a.ID)
		time.Sleep(2 * time.Millisecond)
	}

	firstPage, err := s.ListAudit(ctx, 2, 0)
	if err != nil {
		t.Fatalf("查审计失败：%v", err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("第一页应有 2 行，实际 %d", len(firstPage))
	}
	if firstPage[0].ID != ids[2] || firstPage[1].ID != ids[1] {
		t.Fatalf("应按 id DESC（最新在前），实际 %+v", []int64{firstPage[0].ID, firstPage[1].ID})
	}
	// 回读的 detail 必须是合法 JSON（jsonb 的文本形态）。
	if !json.Valid([]byte(firstPage[0].Detail)) {
		t.Fatalf("回读 detail 应是合法 JSON，实际 %q", firstPage[0].Detail)
	}

	secondPage, err := s.ListAudit(ctx, 2, 2)
	if err != nil {
		t.Fatalf("查审计失败：%v", err)
	}
	if len(secondPage) != 1 || secondPage[0].ID != ids[0] {
		t.Fatalf("第二页应该只剩最早那条，实际 %+v", secondPage)
	}

	// 默认分页（limit<=0 → 默认值）与越界页。
	all, err := s.ListAudit(ctx, 0, 0)
	if err != nil {
		t.Fatalf("默认分页失败：%v", err)
	}
	if len(all) != 3 {
		t.Fatalf("默认分页应返回全部 3 行，实际 %d", len(all))
	}
	empty, err := s.ListAudit(ctx, 10, 100)
	if err != nil {
		t.Fatalf("越界页失败：%v", err)
	}
	if len(empty) != 0 || empty == nil {
		t.Fatalf("越界页应返回空切片，实际 %v", empty)
	}

	// 超大 limit 被截断且不报错。
	if _, err := s.ListAudit(ctx, 100000, 0); err != nil {
		t.Fatalf("超大 limit 不该报错：%v", err)
	}
}

// TestCanonicalAuditDetail 直接覆盖纯函数（不必每次都过数据库）。
func TestCanonicalAuditDetail(t *testing.T) {
	if got, err := canonicalAuditDetail("  "); err != nil || got != "{}" {
		t.Fatalf("空白应变成 {}：got=%q err=%v", got, err)
	}
	if got, err := canonicalAuditDetail(`{"a":1}`); err != nil || got != `{"a":1}` {
		t.Fatalf("合法对象应原样透传：got=%q err=%v", got, err)
	}
	got, err := canonicalAuditDetail("纯文本")
	if err != nil {
		t.Fatalf("普通文本不该报错：%v", err)
	}
	var s string
	if err := json.Unmarshal([]byte(got), &s); err != nil || s != "纯文本" {
		t.Fatalf("普通文本应被编码成 JSON 字符串：got=%q err=%v", got, err)
	}
}
