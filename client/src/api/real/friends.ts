/**
 * 好友域 · 真实 HTTP 实现。
 *
 * ⚠ **票 #59 之后本文件只剩三条端点**（好友请求流水线整体退役）：
 *
 *   POST   /api/blocks                 {userId}  → blockView
 *   GET    /api/users/search?q=                 → {items: searchHitView[]}
 *   GET    /api/blocks                          → {items: blockedEntryView[]}
 *   DELETE /api/blocks/:userId                  → 204
 *
 * 退役的四条（`GET /api/friends`、`GET|POST /api/friends/requests`、
 * `POST /api/friends/requests/:id/{accept,reject}`、`DELETE /api/friends/:id`）
 * 现在一律 404 —— 服务端把它们从路由表删掉了（与 `/api/auth/forgot` 同一先例），
 * 落到 `NoRoute` 的 `/api` 前缀拦截，返回中文错误而不是静默成功。
 *
 * 为什么「好友列表」不需要替代端点：好友 ≡ 互关（GLOSSARY），
 * 那个列表就是 `GET /api/follows?tab=mutual`（`api/follows.ts` 的 `listFollowList`）。
 * 同一个列表两个端点，早晚会出现"这边有他、那边没有"。
 *
 * 两条与 `api/admin.ts` / `api/real/posts.ts` 一致的写法：一律走
 * `requestJSON(authedFetch, …)`（401 自动刷新一次的语义只在 `stores/auth.ts` 一处实现），
 * 并用 `toCapabilityUnavailable*` 保留"整域 404 = 能力未上线"的判据。
 *
 * ⚠ **404 的两种含义必须分开**：`unblock` 的 404 是**语义**错误——服务端对
 * "这人不在你的黑名单里"回 404（防存在性探测），它跟"整域路由没注册"长得一模一样。
 * 所以它走 `toCapabilityUnavailableIf`，用一次列表探针回答"域到底在不在"。
 */
import { requestJSON, query, type AuthedFetch } from '../http'
import { toCapabilityUnavailable, toCapabilityUnavailableIf } from '../mode'
import { avatarOf } from '../../utils/avatar'
import { useAuthStore } from '../../stores/auth'
import type { BlockedEntry, SearchHit } from '../../types/friends'

/** 取认证 fetch（延迟到调用时：模块加载期 Pinia 可能还没装配）。 */
function fetcher(): AuthedFetch {
  return useAuthStore().authedFetch
}

/**
 * 服务端搜索结果的一行（`GET /api/users/search`）。
 *
 * 关系事实**整组来自关注域**：服务端一次算好四个布尔 + 一个 blocked。
 * 票 #59 删掉了旧的 `relation` 字符串（那是 friendships 流水线的语言）。
 */
interface ServerSearchHit {
  userId: number
  username: string
  displayName: string
  avatarUrl?: string | null
  following?: boolean
  followedBy?: boolean
  mutual?: boolean
  canDm?: boolean
  /** 存在拉黑（不区分方向）：存续期不给关注按钮。 */
  blocked?: boolean
}

/** 服务端黑名单的一行（`GET /api/blocks`）。 */
interface ServerBlockedEntry {
  userId: number
  displayName: string
  username: string
  avatarUrl?: string | null
  blockedAt: string
}

/** 服务端 `blockView`（`POST /api/blocks` 的响应）。 */
interface ServerBlockView {
  relationshipId: number
  userId: number
  displayName: string
  status: string
}

function toPerson(userId: number, displayName: string, avatarUrl?: string | null) {
  const person = { id: userId, displayName: displayName || '未知用户', ...avatarOf(userId, displayName) }
  // 像素头像只是覆盖层（票 #74）：有值才带上，没值让 UserAvatar 回落字母头。
  return typeof avatarUrl === 'string' && avatarUrl !== '' ? { ...person, avatarUrl } : person
}

/**
 * 按用户名找人：`GET /api/users/search?q=<关键词>`。
 *
 * 服务端返回 `{items: [{userId, username, displayName, following, followedBy,
 * mutual, canDm, blocked}]}`：关系事实一次算好（它按本次结果的 id 批量查，不让我们
 * 每个结果再问一次），**被拉黑的人也出现在结果里** —— 那是"我拉黑了谁"的唯一可见入口。
 *
 * 两条与既有端点一致的取舍：
 *   - 空 `q` 服务端回 400，因此这里**不**在本地先拦：让服务端的文案统一出口
 *     （页面本来就禁用了空查询的提交按钮，双拦只会让两边文案不一致）。
 *   - 走 `toCapabilityUnavailable`：这个路径整组没注册时（域未上线）是 404，
 *     与黑名单同一个降级语义。注意搜索的 404 不会出现 —— 服务端对"没有匹配"
 *     回的是 200 + 空数组（"搜不到"与"接口没有"必须可分辨）。
 */
export async function searchUsers(keyword: string): Promise<SearchHit[]> {
  try {
    const body = await requestJSON<{ items: ServerSearchHit[] }>(
      fetcher(),
      `/api/users/search${query({ q: keyword })}`,
    )
    return (body?.items ?? []).map((raw) => ({
      ...toPerson(raw.userId, raw.displayName, raw.avatarUrl),
      following: raw.following === true,
      followedBy: raw.followedBy === true,
      mutual: raw.mutual === true,
      canDm: raw.canDm === true,
      blocked: raw.blocked === true,
    }))
  } catch (err) {
    throw toCapabilityUnavailable('friends', err)
  }
}

/**
 * 拉黑。
 *
 * 服务端在**一个事务**里做两件事：写 blocked 行（覆盖任何历史状态）+ 删掉
 * 「我→TA」的关注边（保留「TA→我」，票 #42 §4）。所以拉黑一个好友 = 这段互关结束
 * + 双方名单互不可见，不需要再调一次取关。
 */
export async function blockUser(userId: number): Promise<void> {
  try {
    await requestJSON<ServerBlockView>(fetcher(), '/api/blocks', { method: 'POST', body: { userId } })
  } catch (err) {
    throw toCapabilityUnavailable('friends', err)
  }
}

/** 黑名单列表：`GET /api/blocks`。只列**我拉黑的**人。 */
export async function listBlocked(): Promise<BlockedEntry[]> {
  try {
    const body = await requestJSON<{ items: ServerBlockedEntry[] }>(fetcher(), '/api/blocks')
    return (body?.items ?? []).map((raw) => ({
      user: toPerson(raw.userId, raw.displayName, raw.avatarUrl),
    }))
  } catch (err) {
    throw toCapabilityUnavailable('friends', err)
  }
}

/**
 * 解除拉黑：`DELETE /api/blocks/:userId`。
 *
 * 服务端对"这人不在你的黑名单里"回 **404**（与 #29 同一决定：越权与不存在不可分辨，
 * 避免存在性探测）。所以这里要用 `toCapabilityUnavailableIf` 配一次列表探针 ——
 * 否则"已经解除过了"会被说成"后端没上线"。
 */
export async function unblock(userId: number): Promise<void> {
  try {
    await requestJSON<null>(fetcher(), `/api/blocks/${userId}`, { method: 'DELETE' })
  } catch (err) {
    throw await toCapabilityUnavailableIf('friends', err, () => listBlocked())
  }
}
