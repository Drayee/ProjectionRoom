package store

import (
	"sort"
	"strings"
	"testing"
)

// columnSpec 是 information_schema.columns 里我们要锁定的四个属性。
type columnSpec struct {
	dataType string // data_type：character varying / bigint / timestamptz / jsonb / boolean …
	maxLen   int64  // character_maximum_length（非字符类型为 0）
	nullable bool
}

// TestSchemaMatchesAccountsSpec 把 ACCOUNTS §4 的列定义钉成可执行断言。
//
// 为什么值得单独测一遍：模型层只做列映射，表结构的真相在 DDL 里；一旦有人
// 手滑把 varchar(254) 写成 varchar(32)、把 char(64) 写成 varchar(64)、
// 或者把某个 NOT NULL 写掉，单元测试全都察觉不到（GORM 会照样读写）。
// 这个用例直接从 information_schema 读回真实结构，比对 §4 的契约。
func TestSchemaMatchesAccountsSpec(t *testing.T) {
	s := requireTestDB(t)

	expected := map[string]map[string]columnSpec{
		"users": {
			"id":            {"bigint", 0, false},
			"username":      {"character varying", 20, false},
			"display_name":  {"character varying", 32, false},
			"password_hash": {"character varying", 72, false},
			"email":         {"character varying", 254, true},
			"role":          {"character varying", 8, false},
			"status":        {"character varying", 8, false},
			"token_version": {"integer", 0, false},
			"banned_reason": {"text", 0, true},
			"created_at":    {"timestamp with time zone", 0, false},
			"updated_at":    {"timestamp with time zone", 0, false},
			"last_seen_at":  {"timestamp with time zone", 0, false},
		},
		"sessions": {
			"id":         {"bigint", 0, false},
			"user_id":    {"bigint", 0, false},
			"token_hash": {"character", 64, false}, // §4 明确是 char(64)
			"user_agent": {"character varying", 255, true},
			"ip":         {"character varying", 45, true},
			"issued_at":  {"timestamp with time zone", 0, false},
			"expires_at": {"timestamp with time zone", 0, false},
			"revoked_at": {"timestamp with time zone", 0, true},
		},
		"rooms_meta": {
			"room_id":       {"character varying", 12, false},
			"owner_user_id": {"bigint", 0, false},
			"title":         {"character varying", 60, false},
			"is_public":     {"boolean", 0, false},
			"has_password":  {"boolean", 0, false},
			"created_at":    {"timestamp with time zone", 0, false},
			"last_seen_at":  {"timestamp with time zone", 0, false},
			"closed_at":     {"timestamp with time zone", 0, true},
		},
		"admin_audit": {
			"id":          {"bigint", 0, false},
			"actor_id":    {"bigint", 0, false},
			"action":      {"character varying", 32, false},
			"target_type": {"character varying", 16, false},
			"target_id":   {"character varying", 64, false},
			"detail":      {"jsonb", 0, false},
			"ip":          {"character varying", 45, true},
			"created_at":  {"timestamp with time zone", 0, false},
		},
		"schema_migrations": {
			"version":    {"character varying", 32, false},
			"applied_at": {"timestamp with time zone", 0, false},
		},
	}

	for table, want := range expected {
		t.Run(table, func(t *testing.T) {
			got := readColumns(t, s, table)
			for col, spec := range want {
				actual, ok := got[col]
				if !ok {
					t.Fatalf("表 %s 缺少列 %s（实际列：%v）", table, col, sortedKeys(got))
				}
				if actual != spec {
					t.Fatalf("表 %s 的列 %s 定义不符：want=%+v got=%+v", table, col, spec, actual)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("表 %s 列数不符：want=%d got=%d（多出来的列往往是意外新增，需要同步 §4）", table, len(want), len(got))
			}
		})
	}
}

// TestSchemaConstraintsMatchSpec 断言主键/唯一/外键的落点，尤其是两处**刻意**的差异：
// sessions.token_hash 唯一、admin_audit 没有 actor 外键（审计要活过用户删除）。
func TestSchemaConstraintsMatchSpec(t *testing.T) {
	s := requireTestDB(t)

	type constraint struct {
		kind   string // PRIMARY KEY / UNIQUE / FOREIGN KEY
		column string
	}
	cases := map[string][]constraint{
		"users":    {{"PRIMARY KEY", "id"}, {"UNIQUE", "username"}},
		"sessions": {{"PRIMARY KEY", "id"}, {"UNIQUE", "token_hash"}, {"FOREIGN KEY", "user_id"}},
		"rooms_meta": {
			{"PRIMARY KEY", "room_id"},
			{"FOREIGN KEY", "owner_user_id"},
		},
		"admin_audit":       {{"PRIMARY KEY", "id"}},
		"schema_migrations": {{"PRIMARY KEY", "version"}},
	}

	for table, want := range cases {
		t.Run(table, func(t *testing.T) {
			got := readConstraints(t, s, table)
			for _, c := range want {
				key := c.kind + ":" + c.column
				if !got[key] {
					t.Fatalf("表 %s 缺少约束 %s（实际：%v）", table, key, sortedKeysBool(got))
				}
			}
			if table == "admin_audit" {
				// 审计表的 actor_id 刻意不设外键：删用户时审计行必须留下。
				for key := range got {
					if strings.HasPrefix(key, "FOREIGN KEY:") {
						t.Fatalf("admin_audit 不该有外键（审计要活过用户删除），实际 %s", key)
					}
				}
			}
		})
	}
}

// TestSchemaIndexesExist 断言迁移里建的索引真的在（公开房列表与过期清理都依赖它们）。
func TestSchemaIndexesExist(t *testing.T) {
	s := requireTestDB(t)

	for _, name := range []string{
		"sessions_user_id_idx",
		"sessions_expires_at_idx",
		"rooms_meta_public_idx",
	} {
		var exists bool
		const q = `SELECT EXISTS (
	SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND indexname = ?
)`
		if err := s.DB().Raw(q, name).Scan(&exists).Error; err != nil {
			t.Fatalf("查询索引 %s 失败：%v", name, err)
		}
		if !exists {
			t.Fatalf("索引 %s 不存在", name)
		}
	}
}

// readColumns 从 information_schema 读回某张表的列定义。
func readColumns(t *testing.T, s *Store, table string) map[string]columnSpec {
	t.Helper()
	type row struct {
		ColumnName string
		DataType   string
		// 必须显式写 column 标签：GORM 的 SmartScan 按 snake_case 名匹配，
		// MaxLen 会被猜成 max_len 而对不上 information_schema 的列名（拿不到值）。
		MaxLen     *int64 `gorm:"column:character_maximum_length"`
		IsNullable string
	}
	var rows []row
	const q = `SELECT column_name, data_type, character_maximum_length, is_nullable
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = ?`
	if err := s.DB().Raw(q, table).Scan(&rows).Error; err != nil {
		t.Fatalf("读取表 %s 的列定义失败：%v", table, err)
	}
	out := make(map[string]columnSpec, len(rows))
	for _, r := range rows {
		var maxLen int64
		if r.MaxLen != nil {
			maxLen = *r.MaxLen
		}
		out[r.ColumnName] = columnSpec{
			dataType: r.DataType,
			maxLen:   maxLen,
			nullable: strings.EqualFold(r.IsNullable, "YES"),
		}
	}
	return out
}

// readConstraints 读回某张表的约束集合，键形如 "UNIQUE:username"。
func readConstraints(t *testing.T, s *Store, table string) map[string]bool {
	t.Helper()
	type row struct {
		ConstraintType string
		ColumnName     string
	}
	var rows []row
	const q = `SELECT tc.constraint_type, kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON kcu.constraint_name = tc.constraint_name AND kcu.table_schema = tc.table_schema
WHERE tc.table_schema = 'public' AND tc.table_name = ?`
	if err := s.DB().Raw(q, table).Scan(&rows).Error; err != nil {
		t.Fatalf("读取表 %s 的约束失败：%v", table, err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.ConstraintType+":"+r.ColumnName] = true
	}
	return out
}

func sortedKeys(m map[string]columnSpec) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysBool(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
