/**
 * 帖子域 · 真实 HTTP 实现（wayfinder #27 后端落地后填实，#26 切真）。
 *
 * 两份约定（`api/admin.ts` 已验证过的写法，这里照抄）：
 *   1. 一律 `requestJSON(authedFetch, path, …)`，绝不自己 `fetch` —— 401 自动刷新一次
 *      的语义只有 `auth.authedFetch` 一处实现；
 *   2. 用 `toCapabilityUnavailable('posts', err)` 包一层，保留"整域 404 = 能力未上线"。
 *
 * 与 `api/admin.ts` 的两个差别，都是刻意的：
 *
 *   - **自己取 authedFetch**（admin.ts 是调用方传进来）。帖子三页是公开浏览场景，
 *     让每个页面都持有一个 fetch 引用只是把同一个 store 实例多传一手；而
 *     `useAuthStore()` 在组件之外调用是安全的（Pinia 的 store 是单例）。
 *     401 的处理因此仍走 `requestJSON` 的默认路径（`redirectOnAuthFailure` 不显式关）。
 *   - **读端点不要求登录**。服务端 `optionalViewer` 的语义是"有合法 token 就认出登录者、
 *     没有就当匿名"，所以这里读端点也照样带 authedFetch：带着 token 去，服务端才会回
 *     `liked`；匿名去也能正常读到列表与详情。
 *
 * 服务端契约（#27 已冻结）：
 *   GET    /api/posts?sort=latest|hot&limit&offset → {items, hasMore}
 *   GET    /api/posts/:id                          → Post
 *   POST   /api/posts                              → Post（201）
 *   POST   /api/posts/:id/like                     → {liked, post}
 *   GET    /api/posts/:id/comments?limit&offset    → {items, total, limit, offset}
 *   POST   /api/posts/:id/comments                 → Comment（201）
 *   DELETE /api/comments/:id                       → 204
 *
 * ⚠ 两处**形状适配**（服务端字段名与前端类型不一致，转换只发生在这里）：
 *   - 服务端 `author: {id, displayName}`，前端 `author` 还要 `avatarChar` / `avatarColor`。
 *     那两个是纯 UI 派生（首字母 + 名字哈希取色，逻辑与 `ui/AppHeader.vue` 一致）——
 *     **后端契约不掺显示层**，派生放在这一层做。
 *   - 服务端 `coverUrl`，帖子域对应的是 `images: string[]`（数组）。
 */
import { requestJSON, query, type AuthedFetch } from '../http'
import { toCapabilityUnavailable, toCapabilityUnavailableIf } from '../mode'
import type { Comment, ListPostsInput, ListPostsResult, Post } from '../../types/posts'
import { useAuthStore } from '../../stores/auth'
// 头像派生（首字母 + 稳定取色）与好友域共用一份：utils/avatar.ts。
import { avatarOf } from '../../utils/avatar'

/** 页面大小：与 store 的 defaultPostLimit 同值（20）。 */
const PAGE_SIZE = 20

/**
 * 取认证 fetch。
 *
 * 延迟到调用时才 `useAuthStore()`：模块加载期 store 可能还没装配（SSR / 测试环境），
 * 而函数体只在真实请求路径上跑，那时 Pinia 一定已就绪。
 */
function fetcher(): AuthedFetch {
  return useAuthStore().authedFetch
}


/** 服务端帖子行的最小形状（只声明本文件用到的键）。 */
interface ServerPost {
  id: number
  title: string
  content: string
  authorId: number
  author: { id: number; displayName: string }
  images: string[]
  likeCount: number
  commentCount: number
  liked: boolean
  createdAt: string
  /** 主题串所属板块；普通帖子服务端 omitempty 掉这个键（wayfinder #38）。 */
  animeId?: number
  /** 最后回复时间（只有主题串有）。 */
  lastReplyAt?: string
}

interface ServerComment {
  id: number
  postId: number
  authorId: number
  author: { id: number; displayName: string }
  content: string
  createdAt: string
}

/**
 * 服务端帖子行 → 前端形状（**导出给 forum 域复用**：板块主题串与帖子是同一份数据，
 * 抄第二份归一化会让 `animeId` 这类可选字段在一处有、另一处漏）。
 */
export function toPost(raw: ServerPost): Post {
  const post: Post = {
    id: raw.id,
    title: raw.title,
    content: raw.content,
    authorId: raw.authorId,
    author: { ...raw.author, ...avatarOf(raw.authorId, raw.author.displayName) },
    images: raw.images ?? [],
    likeCount: raw.likeCount,
    commentCount: raw.commentCount,
    liked: raw.liked ?? false,
    createdAt: raw.createdAt,
  }
  if (typeof raw.animeId === 'number') post.animeId = raw.animeId
  if (typeof raw.lastReplyAt === 'string' && raw.lastReplyAt !== '') post.lastReplyAt = raw.lastReplyAt
  return post
}

/** 服务端帖子行类型（forum 域需要声明同一个形状的请求体）。 */
export type { ServerPost }

function toComment(raw: ServerComment): Comment {
  return {
    id: raw.id,
    postId: raw.postId,
    authorId: raw.authorId,
    author: { ...raw.author, ...avatarOf(raw.authorId, raw.author.displayName) },
    content: raw.content,
    createdAt: raw.createdAt,
  }
}

export async function listPosts(input: ListPostsInput = {}): Promise<ListPostsResult> {
  try {
    // 前端契约是 page（1 起），服务端是 offset —— 换算只发生在这一层。
    const page = input.page && input.page > 0 ? input.page : 1
    const offset = (page - 1) * PAGE_SIZE
    const body = await requestJSON<{ items: ServerPost[]; hasMore: boolean }>(
      fetcher(),
      `/api/posts${query({ sort: input.sort ?? 'latest', limit: PAGE_SIZE, offset })}`,
    )
    return { items: (body?.items ?? []).map(toPost), hasMore: body?.hasMore ?? false }
  } catch (err) {
    throw toCapabilityUnavailable('posts', err)
  }
}

export async function getPost(id: number): Promise<Post> {
  try {
    const body = await requestJSON<ServerPost>(fetcher(), `/api/posts/${id}`)
    if (!body) throw new Error('帖子不存在')
    return toPost(body)
  } catch (err) {
    // 带探针的版本：404 时先问一次列表"这个域到底在不在"。
    // 少了这一步，「帖子被删了」会被说成「该分区后端尚未上线」——用户会以为后端挂了。
    throw await toCapabilityUnavailableIf('posts', err, () => listPosts({ page: 1 }))
  }
}

export async function createPost(input: { title: string; content: string; images?: string[] }): Promise<Post> {
  try {
    const body = await requestJSON<ServerPost>(fetcher(), '/api/posts', {
      method: 'POST',
      body: { title: input.title, content: input.content, images: input.images ?? [] },
    })
    if (!body) throw new Error('发帖失败')
    return toPost(body)
  } catch (err) {
    throw toCapabilityUnavailable('posts', err)
  }
}

export async function toggleLike(postId: number): Promise<{ liked: boolean; likeCount: number }> {
  try {
    const body = await requestJSON<{ liked: boolean; post: ServerPost }>(
      fetcher(),
      `/api/posts/${postId}/like`,
      { method: 'POST' },
    )
    // 服务端回的是整行（含权威计数）；门面契约只要 {liked, likeCount}。
    return { liked: body?.liked ?? false, likeCount: body?.post?.likeCount ?? 0 }
  } catch (err) {
    throw toCapabilityUnavailable('posts', err)
  }
}

export async function listComments(postId: number): Promise<Comment[]> {
  try {
    const body = await requestJSON<{ items: ServerComment[] }>(
      fetcher(),
      `/api/posts/${postId}/comments${query({ limit: PAGE_SIZE })}`,
    )
    return (body?.items ?? []).map(toComment)
  } catch (err) {
    throw toCapabilityUnavailable('posts', err)
  }
}

export async function addComment(input: { postId: number; content: string }): Promise<Comment> {
  try {
    const body = await requestJSON<ServerComment>(fetcher(), `/api/posts/${input.postId}/comments`, {
      method: 'POST',
      body: { content: input.content },
    })
    if (!body) throw new Error('评论失败')
    return toComment(body)
  } catch (err) {
    throw toCapabilityUnavailable('posts', err)
  }
}

export async function deleteComment(id: number): Promise<void> {
  try {
    await requestJSON<null>(fetcher(), `/api/comments/${id}`, { method: 'DELETE' })
  } catch (err) {
    throw toCapabilityUnavailable('posts', err)
  }
}
