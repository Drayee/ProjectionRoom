package store

import "gorm.io/gorm"

// 「这个账号在社区里可见吗」的唯一实现（票 #73）。
//
// # 为什么必须抽出来
//
// 注销（票 #73）让"可见"这件事多了一个维度：`status = 'active'` 还不够，
// 还要 `deletion_requested_at IS NULL`。这条谓词在至少五处成立：
//
//	公开档案（GET /api/users/:id → 404）
//	按用户名/昵称搜索（`/api/users/search` 里消失）
//	关注 / 粉丝 / 互关名单（三份名单里都不该出现）
//	粉丝红点的计数（不亮）
//	帖子流谓词（`applyAuthorVisibility`，关注流与作者帖列表都走它）
//
// 每一处各自手写一份的后果不是"多写几行"，而是**加维度时漏掉一处**：
// 注销过滤加上去了、搜索忘了，症状是"注销之后还能被搜到"。界面上没有任何
// 报错，只会被细心的人偶然发现 —— 这正是 flow.go 里那条谓词被抽出来的同一条理由。
//
// # 为什么是 SQL 片段而不是 Go 函数判
//
// 这些查询都是"在库里筛"，不是"取回来再筛"。用 Go 判要先全量取回，
// 分页与 LIMIT 都会算错（LIMIT 20 里 3 个是注销账号，用户就只看到 17 条）。
//
// # 为什么条件里写死字面量而不是参数绑定
//
// `status` 是一个**封闭取值**的枚举（DDL 的 CHECK 只有 active / banned），
// 不是用户输入。写成参数会让每一处调用都要多传一个 `StatusActive`，
// 而"忘了传"在编译期查不出来 —— 那正是这条谓词最怕的失败方式。

// visibleAccountCondition 是**不带表别名**的可见性条件（单表查询用）。
const visibleAccountCondition = `status = 'active' AND deletion_requested_at IS NULL`

// applyVisibleAccount 把可见性条件挂到带别名的查询上。
//
// alias 是表别名（如 `u` / `p` / `va`）。传空串表示不带别名。
// 与 `applyAuthorVisibility` 的分工：那个是"按作者列的可见性"（还要处理拉黑，
// 需要 viewer），这个是"单行账号自身的可见性"（不需要 viewer，注销对所有人一样）。
func applyVisibleAccount(db *gorm.DB, alias string) *gorm.DB {
	if alias == "" {
		return db.Where(visibleAccountCondition)
	}
	return db.Where(alias + `.status = 'active' AND ` + alias + `.deletion_requested_at IS NULL`)
}

// maskedDisplayNameSQL 返回"作者名，注销后换成占位名"的 SQL 表达式。
//
// # 为什么是占位名而不是 NULL / 空串
//
// 帖子不随作者注销消失（决议 #51 §5：内容仍在，署名要诚实）。留空会让卡片上
// 出现一个没有名字的作者行，而 `NULL` 会让前端的 `displayName` 变成 undefined，
// 每个用到它的地方都要补一个兜底 —— 那就等于把这条规则抄了 N 遍。
// 一个确定的占位名让"这条内容属于一个已经走了的人"成为界面事实。
//
// # 为什么用 CASE 而不是 LEFT JOIN + COALESCE
//
// 那些查询本来就有 `JOIN users u ON u.id = ...`（作者一定存在，注销只是打了个
// 时间戳），所以问题不是"行没了"而是"名字不该显示"。COALESCE 在这里会
// 把"未注销"也一起兜底，判据就没了。
//
// # 占位名与前端常量必须一致
//
// `client/src/types/posts.ts` 侧不做任何处理（后端已换好），真机脚本按这个
// 字符串断言。改它要同时改 `DeletedUserDisplayName` 与文档。
func maskedDisplayNameSQL(alias string) string {
	return `CASE WHEN ` + alias + `.deletion_requested_at IS NULL THEN ` + alias + `.display_name ELSE '` + DeletedUserDisplayName + `' END`
}

// DeletedUserDisplayName 是注销账号的公开署名（决议 #51 §5 / GLOSSARY「Deleted user display」）。
const DeletedUserDisplayName = "已注销用户"
