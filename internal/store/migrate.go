package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// MigrateReport 是一次 Migrate 的结果。
type MigrateReport struct {
	// Applied 是本次真正执行的版本号，按执行顺序。
	Applied []string
	// AlreadyApplied 是本次跳过的步数（此前已应用）。
	AlreadyApplied int
}

// migrationLockKey 是迁移用的咨询锁 key（int64 常量，无外部输入）。
// 值是 "prmig" 的 ASCII，便于在 pg_locks 里一眼认出是谁持有的。
const migrationLockKey int64 = 0x70_726D_6967

// Migrate 按版本顺序补齐表结构；重复调用只做已应用检查（幂等）。
//
// 整体语义是「要么全部应用，要么什么都没变」，靠三件事成立：
//  1. **一个事务包住全部步骤**：PostgreSQL 的 DDL 是事务性的，任一步失败即回滚，
//     不会留下"表建了一半、账本却记了版本号"的中间态；
//  2. **事务级咨询锁**：多进程/多实例同时启动时只有一个在改 schema，另一个等它
//     提交后再读账本（然后正常跳过）；
//  3. **账本表先无条件建**（见 schemaMigrationsDDL 的注释）。
//
// Applied 只在事务提交成功后回填，所以调用方拿到的报告不会"虚报已应用"。
func (s *Store) Migrate(ctx context.Context) (*MigrateReport, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store: Store 未初始化")
	}
	plan, err := planMigrations()
	if err != nil {
		return nil, err
	}

	report := &MigrateReport{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMigrations(tx); err != nil {
			return err
		}
		if err := execStatements(tx, "建 schema_migrations", schemaMigrationsDDL); err != nil {
			return err
		}
		applied, err := appliedVersions(tx)
		if err != nil {
			return err
		}
		for _, m := range plan {
			if _, ok := applied[m.version]; ok {
				report.AlreadyApplied++
				continue
			}
			if err := execStatements(tx, "执行迁移 "+m.version, m.statements); err != nil {
				return err
			}
			if err := recordVersion(tx, m.version); err != nil {
				return err
			}
			report.Applied = append(report.Applied, m.version)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if report.Applied == nil {
		report.Applied = []string{}
	}
	return report, nil
}

// planMigrations 校验迁移清单：版本号非空且严格递增、每步至少一条语句。
//
// 为什么要在运行时也校验（而不只在测试里）：迁移清单是最容易在后续期数里被
// 插错位置的东西，而"顺序错了"在幂等路径上很安静 —— 与其等到某次上线才发现，
// 不如每次启动花 O(5) 判一次。
func planMigrations() ([]migration, error) {
	out := make([]migration, 0, len(migrations))
	prev := ""
	for _, m := range migrations {
		if m.version == "" {
			return nil, fmt.Errorf("store: 迁移版本号为空")
		}
		if len(m.statements) == 0 {
			return nil, fmt.Errorf("store: 迁移 %s 没有任何语句", m.version)
		}
		if prev != "" && m.version <= prev {
			return nil, fmt.Errorf("store: 迁移版本号必须严格递增（%s 之后是 %s）", prev, m.version)
		}
		prev = m.version
		out = append(out, m)
	}
	return out, nil
}

// lockMigrations 取事务级咨询锁；事务结束（提交或回滚）自动释放。
func lockMigrations(tx *gorm.DB) error {
	// 常量 key + 占位符绑定，没有字符串拼接。
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, migrationLockKey).Error; err != nil {
		return fmt.Errorf("store: 获取迁移咨询锁失败: %w", err)
	}
	return nil
}

// appliedVersions 读出账本里已应用的版本号集合。
func appliedVersions(tx *gorm.DB) (map[string]struct{}, error) {
	var rows []schemaMigration
	if err := tx.Model(&schemaMigration{}).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: 读取 schema_migrations 失败: %w", err)
	}
	set := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		set[r.Version] = struct{}{}
	}
	return set, nil
}

// recordVersion 把版本号记进账本；applied_at 用 DDL 的 DEFAULT now()。
func recordVersion(tx *gorm.DB, version string) error {
	if err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version).Error; err != nil {
		return fmt.Errorf("store: 记录迁移 %s 失败: %w", version, err)
	}
	return nil
}

// execStatements 按序执行一组 DDL 语句（每条都是一个独立命令，
// 避免 pgx 扩展协议下"一条命令里塞多条语句"的问题）。
func execStatements(tx *gorm.DB, what string, statements []string) error {
	for i, stmt := range statements {
		if err := tx.Exec(stmt).Error; err != nil {
			return fmt.Errorf("store: %s 第 %d 条语句失败: %w", what, i+1, err)
		}
	}
	return nil
}
