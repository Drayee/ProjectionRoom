package store

import (
	"context"
	"strings"
	"testing"
)

// TestMigrateFirstRunAppliesAllThenSecondRunIsNoop 是"迁移幂等 + 有序"的主证据。
//
// 先回到空库（本用例会 DROP 本期表，见 dropPhaseOneTables 的说明），
// 这样"首次应用"才是真的首次：报里的 Applied 顺序就是迁移清单的顺序。
func TestMigrateFirstRunAppliesAllThenSecondRunIsNoop(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	total := len(migrations)

	dropPhaseOneTables(t, s)

	first, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	if len(first.Applied) != total {
		t.Fatalf("首次迁移应该应用全部 %d 步，实际 %v", total, first.Applied)
	}
	if first.AlreadyApplied != 0 {
		t.Fatalf("空库上不该有已应用的步骤，实际 %d", first.AlreadyApplied)
	}
	for i, m := range migrations {
		if first.Applied[i] != m.version {
			t.Fatalf("Applied 顺序应与迁移清单一致：第 %d 项 want=%s got=%s", i, m.version, first.Applied[i])
		}
	}
	t.Logf("首次 Migrate：Applied=%v，AlreadyApplied=%d", first.Applied, first.AlreadyApplied)

	second, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("第二次迁移失败（幂等性要求它不能报错）：%v", err)
	}
	if len(second.Applied) != 0 {
		t.Fatalf("第二次不该再应用任何步骤，实际 %v", second.Applied)
	}
	if second.AlreadyApplied != total {
		t.Fatalf("第二次的 AlreadyApplied 应该等于总步数 %d，实际 %d", total, second.AlreadyApplied)
	}
	t.Logf("第二次 Migrate：Applied=%v，AlreadyApplied=%d", second.Applied, second.AlreadyApplied)

	// 账本行数与步数一致（没有重复记录、没有漏记）。
	var logged int64
	if err := s.DB().Model(&schemaMigration{}).Count(&logged).Error; err != nil {
		t.Fatalf("统计 schema_migrations 失败：%v", err)
	}
	if int(logged) != total {
		t.Fatalf("账本应有 %d 行，实际 %d", total, logged)
	}
	t.Logf("账本 schema_migrations 行数=%d（等于迁移步数 %d）", logged, total)

	// 第三次仍然幂等（不是"第二次恰好"）。
	third, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("第三次迁移失败：%v", err)
	}
	if len(third.Applied) != 0 || third.AlreadyApplied != total {
		t.Fatalf("第三次也应完全跳过：Applied=%v AlreadyApplied=%d", third.Applied, third.AlreadyApplied)
	}
}

// TestMigrateCreatesPhaseOneTablesOnly 断言本期的表边界（ACCOUNTS §4 + 计划 T2）：
// 五张表都在，二三期的表与房间快照表都不建。
func TestMigrateCreatesPhaseOneTablesOnly(t *testing.T) {
	s := requireTestDB(t)

	for _, name := range []string{"schema_migrations", "users", "sessions", "rooms_meta", "admin_audit", "password_resets"} {
		if !tableExists(t, s, name) {
			t.Fatalf("表 %s 应该存在", name)
		}
	}
	for _, name := range []string{"friendships", "messages", "room_snapshots"} {
		if tableExists(t, s, name) {
			t.Fatalf("表 %s 属于二期/三期，本期不该建", name)
		}
	}
}

// TestMigrationPlanIsOrderedAndAdditiveOnly 静态检查迁移清单本身：
// 版本号严格递增、没有任何破坏性语句、也没有提前建二三期的表。
func TestMigrationPlanIsOrderedAndAdditiveOnly(t *testing.T) {
	plan, err := planMigrations()
	if err != nil {
		t.Fatalf("迁移清单本身不合法：%v", err)
	}
	if len(plan) < 5 {
		t.Fatalf("本期应有至少 5 步（账本 + 4 张表），实际 %d", len(plan))
	}
	prev := ""
	for _, m := range plan {
		if m.version <= prev {
			t.Fatalf("版本号必须严格递增：%s 之后出现了 %s", prev, m.version)
		}
		prev = m.version
	}

	var all strings.Builder
	for _, m := range plan {
		for _, stmt := range m.statements {
			// 只按"语句首词"判破坏性：ON DELETE CASCADE 这类从句里出现 delete
			// 是合法且必须的（§4 的外键），不能一刀切地禁用关键字子串。
			first := strings.ToLower(strings.Fields(stmt)[0])
			switch first {
			case "drop", "truncate", "delete", "update":
				t.Fatalf("迁移 %s 里出现了破坏性语句（首词 %q）：违反「只做加法」", m.version, first)
			case "alter":
				// ALTER 本身不是破坏性的做法，**破坏性的形态**才是。
				//
				// 这里只放行唯一一种"纯加列"形态：`ALTER TABLE <t> ADD COLUMN [IF NOT EXISTS] …`。
				// 它是幂等的（IF NOT EXISTS）且只增不减；其余一切 ALTER
				//（DROP COLUMN / ALTER COLUMN … TYPE / DROP CONSTRAINT / RENAME / SET NOT NULL …）
				// 都会改动既有结构或既有数据，继续拒绝。
				//
				// 为什么必须放行它（这条规则在 0006 之前是"一律禁止 alter"）：
				// 给一张**已经存在**的表补列没有别的幂等写法 —— 不能写进 CREATE TABLE
				//（表已存在，IF NOT EXISTS 会让整条 CREATE 变成空操作），
				// 而 `ADD CONSTRAINT` 恰恰不能重复执行（见 migrations.go 的第 2 条规则）。
				// 也就是说"一律禁止 alter"这条规则与"给既有表加列"这件事**不可能同时成立**，
				// 而我们显然要允许后者。收窄成"只允许加列"才是这条规则的意图。
				if !isAddColumnOnly(stmt) {
					t.Fatalf("迁移 %s 里的 ALTER 不是允许的「纯加列」形态：%s", m.version, stmt)
				}
			}
			all.WriteString(strings.ToLower(stmt))
			all.WriteString("\n")
		}
	}
	sql := all.String()
	for _, forbidden := range []string{"friendships", "messages", "room_snapshots"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("迁移 DDL 里出现了 %q：违反了期数边界（本期只建 5 张表）", forbidden)
		}
	}
	// DDL 里不能出现占位符字符：GORM 的 Exec 会把它当参数位置，导致语句跑不成。
	if strings.Contains(sql, "?") {
		t.Fatalf("迁移 DDL 里不能出现 '?'（会被当作绑定参数）")
	}
}

// isAddColumnOnly 判断一条 ALTER 语句是否属于唯一被允许的"纯加列"形态。
//
// 期望形态：`ALTER TABLE <name> ADD COLUMN [IF NOT EXISTS] <col> <type> …`
// （按空白分词后：alter / table / <name> / add / column / …）。
// 它只做形状判断，不看列定义 —— 列定义本身由 schema_test.go 的真库断言钉住。
func isAddColumnOnly(stmt string) bool {
	words := strings.Fields(strings.ToLower(stmt))
	if len(words) < 5 {
		return false
	}
	return words[0] == "alter" && words[1] == "table" &&
		words[3] == "add" && words[4] == "column"
}

// TestPlanMigrationsRejectsInvalidPlan 覆盖清单校验的三条失败路径。
//
// 这里直接替换包级变量 migrations —— 本包用例串行执行（没有 t.Parallel），
// 所以安全；替换后一律在 Cleanup 里还原。
func TestPlanMigrationsRejectsInvalidPlan(t *testing.T) {
	saved := migrations
	t.Cleanup(func() { migrations = saved })

	cases := []struct {
		name  string
		plan  []migration
		quote string
	}{
		{"版本号为空", []migration{{version: "", statements: []string{"SELECT 1"}}}, "版本号为空"},
		{"没有语句", []migration{{version: "0001_x"}}, "没有任何语句"},
		{"顺序颠倒", []migration{
			{version: "0002_b", statements: []string{"SELECT 1"}},
			{version: "0001_a", statements: []string{"SELECT 1"}},
		}, "严格递增"},
		{"版本号重复", []migration{
			{version: "0001_a", statements: []string{"SELECT 1"}},
			{version: "0001_a", statements: []string{"SELECT 1"}},
		}, "严格递增"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			migrations = c.plan
			_, err := planMigrations()
			if err == nil {
				t.Fatalf("应该报错")
			}
			if !strings.Contains(err.Error(), c.quote) {
				t.Fatalf("错误信息应包含 %q，实际 %v", c.quote, err)
			}
		})
	}
}

// TestMigrateFailureRollsBackEverything 验证"要么全部应用、要么什么都没变"。
//
// 手法：在清单末尾追加一个含坏语句的步骤。若迁移不是整体事务，
// 前面几步建的表会留下、账本也会记下坏版本号 —— 都是真实事故的形态。
func TestMigrateFailureRollsBackEverything(t *testing.T) {
	s := requireTestDB(t)
	ctx := context.Background()
	total := len(migrations)

	dropPhaseOneTables(t, s)

	saved := migrations
	migrations = append(append([]migration{}, saved...), migration{
		version: "9999_broken",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS migrate_probe (id int)`,
			`THIS IS NOT VALID SQL`,
		},
	})
	t.Cleanup(func() { migrations = saved })

	report, err := s.Migrate(ctx)
	if err == nil {
		t.Fatalf("坏语句必须让 Migrate 失败")
	}
	if report != nil {
		t.Fatalf("失败时不该返回报告（否则调用方会以为应用了某些步骤）：%+v", report)
	}
	t.Logf("注入坏语句后 Migrate 的错误：%v", err)

	// 整体回滚：连坏步骤之前那些合法语句的效果都不该留下。
	if tableExists(t, s, "users") {
		t.Fatalf("失败后 users 表不该存在（迁移必须是整体事务）")
	}
	if tableExists(t, s, "migrate_probe") {
		t.Fatalf("失败步骤自己的语句也不该留下")
	}
	if tableExists(t, s, "schema_migrations") {
		t.Fatalf("失败后账本表本身也不该留下")
	}

	// 还原清单后必须能正常迁移（失败不留残局）。
	migrations = saved
	recovered, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("还原后重新迁移失败：%v", err)
	}
	if len(recovered.Applied) != total {
		t.Fatalf("还原后应该应用全部 %d 步，实际 %v", total, recovered.Applied)
	}

	var broken int64
	if err := s.DB().Model(&schemaMigration{}).Where("version = ?", "9999_broken").Count(&broken).Error; err != nil {
		t.Fatalf("查账本失败：%v", err)
	}
	if broken != 0 {
		t.Fatalf("失败的版本号绝不能进账本，实际有 %d 行", broken)
	}
}
