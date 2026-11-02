package store

import (
	"errors"

	"gorm.io/gorm"
)

// 可见性谓词（票 #66）—— 「这个作者对这个人可不可见」的唯一实现。
//
// # 为什么必须只有一份
//
// 同一句话在三个地方要成立：
//
//	关注流（#66）      —— 我关注的人的帖子
//	他人公开档案（#68） —— 某个人自己的帖子
//	我的帖管理（#70）   —— 我自己发的帖
//
// 三处各自手写一份 SQL 的后果不是"多写几行"，而是**改口径时漏掉一处**：
// 拉黑过滤加上去了、封禁过滤忘了，症状是"某个列表里还能看见被封禁的人"——
// 界面上没有任何报错，只会被细心的人偶然发现。所以这里把谓词抽出来，
// 三处（以及将来任何"按作者筛帖子"的地方）都调它。
//
// # 谓词包含什么
//
//  1. **作者未封禁**（`users.status = 'active'`）：封禁账号在界面上不存在
//     （公开档案也回 404），把它的帖子留在流里就是"点进去一个 404"。
//  2. **双向无拉黑**：拉黑的意思就是"从我的世界里拿掉"，所以拉黑方与被拉黑方
//     **都**看不到对方（票 #42 §4）。注意"我→他"的关注边在拉黑时被删的是
//     **拉黑方自己那条**，所以"我关注的人里还留着一个拉黑我的人"是真实存在的
//     数据形态 —— 只看 follows 表挡不住它，这一条谓词才是那道闸。
//
// # 谓词**不**包含什么
//
// 「关注关系」不在里面：那是调用方的事（关注流要 `IN (我关注的)`，他人档案要
// `= 某个人`）。把关注关系也塞进来的话，他人档案页就没法复用了。

// ErrFlowNeedsViewer 表示"这个排序必须知道看客是谁"。
//
// 关注流对匿名没有意义 —— 返回空列表会把"没登录"伪装成"你还没关注谁"，
// 于是界面给出错误的引导（叫一个游客去关注别人）。所以这里是**明确的拒绝**，
// 由 handler 翻译成 401。
var ErrFlowNeedsViewer = errors.New("store: 关注流需要登录")

// applyAuthorVisibility 把"作者对 viewer 可见"的条件挂到查询上。
//
// authorCol 是作者列的全名（如 `p.author_id`）—— 传字符串而不是靠约定，
// 因为本包同时存在 `posts AS p` 与不带别名的查询，猜错别名的报错
// （`column "author_id" does not exist`）在 SQL 层，与这里的逻辑无关。
//
// viewerID <= 0（匿名）时**跳过拉黑那一条**：匿名没有"我与他的拉黑关系"，
// 而 `requester_id = 0` 匹配不到任何行，写了也只是白白多一个子查询。
// 封禁那一条对匿名同样生效 —— 封禁是账号级的事实，与谁在看无关。
func (s *Store) applyAuthorVisibility(db *gorm.DB, viewerID int64, authorCol string) *gorm.DB {
	db = db.Where(`EXISTS (SELECT 1 FROM users va WHERE va.id = `+authorCol+` AND va.status = ?)`, StatusActive)
	if viewerID > 0 {
		db = db.Where(`NOT EXISTS (
			SELECT 1 FROM friendships b
			 WHERE b.status = 'blocked'
			   AND ((b.requester_id = ? AND b.addressee_id = `+authorCol+`)
			     OR (b.requester_id = `+authorCol+` AND b.addressee_id = ?))
		)`, viewerID, viewerID)
	}
	return db
}

// applyFollowingScope 把"只看我关注的人"挂到查询上。//
// 与 `applyAuthorVisibility` 分开是刻意的：可见性是"能不能看见"，
// 关注范围是"要不要只挑这一批"。合成一个函数会让他人档案页（要可见性、
// 不要关注范围）无处可调，最后又变成两处手写。
//
// viewerID <= 0 时返回 `ErrFlowNeedsViewer`，**不返回空列表**（理由见该错误）。
func applyFollowingScope(db *gorm.DB, viewerID int64, authorCol string) (*gorm.DB, error) {
	if viewerID <= 0 {
		return nil, ErrFlowNeedsViewer
	}
	return db.Where(authorCol+` IN (SELECT ff.followee_id FROM follows ff WHERE ff.follower_id = ?)`, viewerID), nil
}
