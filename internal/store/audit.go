package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// InsertAudit 写入一条管理端审计行（§8：所有写操作都要有审计）。
//
// 校验策略是"宁可写进去也别丢"：审计是 must 落点，因为 detail 格式不漂亮而
// 丢掉一条"谁封禁了谁"的记录，比格式难看严重得多。所以：
//   - actor_id / action / target_type / target_id 必须给出（缺了这条记录就没意义，
//     这种是调用方 bug，直接报错）；
//   - detail 一律被归一化成合法 JSON（见 canonicalAuditDetail），不会写失败；
//   - ip 为空串时写 NULL（不写字面空串，让"没有来源 IP"只有一种表示）。
//
// 成功后 a.ID / a.CreatedAt / a.Detail 都是落库后的实际值。
func (s *Store) InsertAudit(ctx context.Context, a *AdminAudit) error {
	if a == nil {
		return errors.New("store: InsertAudit 收到 nil")
	}
	if a.ActorID <= 0 {
		return errors.New("store: audit 缺少 actor_id")
	}
	a.Action = strings.TrimSpace(a.Action)
	a.TargetType = strings.TrimSpace(a.TargetType)
	a.TargetID = strings.TrimSpace(a.TargetID)
	if a.Action == "" || a.TargetType == "" || a.TargetID == "" {
		return errors.New("store: audit 的 action/target_type/target_id 不能为空")
	}
	detail, err := canonicalAuditDetail(a.Detail)
	if err != nil {
		return err
	}
	a.Detail = detail
	if a.IP != nil {
		v := trimControl(*a.IP)
		if v == "" {
			a.IP = nil
		} else {
			a.IP = &v
		}
	}

	if err := s.db.WithContext(ctx).Create(a).Error; err != nil {
		return fmt.Errorf("store: 写审计失败: %w", err)
	}
	return nil
}

// ListAudit 按分页返回审计行，最新的在前（管理端 /api/admin/audit，§10）。
//
// 排序用 id DESC 而不是 created_at DESC：id 是唯一的单调列，分页不会因为
// 同一秒内的多行而抖动或漏行。
func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]AdminAudit, error) {
	limit, offset = clampPage(limit, offset)

	rows := make([]AdminAudit, 0, limit)
	err := s.db.WithContext(ctx).
		Model(&AdminAudit{}).
		Order("id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: 查询审计失败: %w", err)
	}
	return rows, nil
}

// canonicalAuditDetail 把 detail 规范化成合法 JSON 文本（列类型是 jsonb）。
//
// 三条确定性规则：
//  1. 空串 → {}，与 DDL 的 DEFAULT '{}' 一致；
//  2. 以 { 或 [ 开头且本身是合法 JSON → 原样透传（调用方刻意给了结构化明细）；
//  3. 其余 → 当作**字符串值**用 json.Marshal 编码（例如"封禁理由：刷屏"）。
//
// 规则 2/3 的区分点选在"首字符 + 合法性"上，是为了让"我传了 JSON"和"我传了一段
// 文本"这两种意图都能表达，而不用给签名加第二个参数。
func canonicalAuditDetail(detail string) (string, error) {
	d := strings.TrimSpace(detail)
	if d == "" {
		return "{}", nil
	}
	if (strings.HasPrefix(d, "{") || strings.HasPrefix(d, "[")) && json.Valid([]byte(d)) {
		return d, nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("store: 审计明细编码失败: %w", err)
	}
	return string(b), nil
}
