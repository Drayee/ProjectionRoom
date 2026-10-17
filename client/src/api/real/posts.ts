/**
 * 帖子域 · 真实 HTTP 实现（wayfinder #22 的样板；后端票 #27 落地后填实现）。
 *
 * 当前状态：`/api/posts/*` 这组路由在服务端**尚未注册**，所以每个函数都抛
 * `CapabilityUnavailable` —— 页面据此渲染「该分区后端尚未上线」空态，而不是裸的
 * 「请求失败（HTTP 404）」。这正是本适配层存在的意义：后端票落地时，只需把下面每个
 * 函数体换成一次 `requestJSON`，门面与页面一行都不用动。
 *
 * 填实现时的两条约定（已在 `api/admin.ts` 里验证过的写法）：
 *   1. 一律 `requestJSON(authedFetch, path, …)`，绝不自己 `fetch` —— 401 自动刷新一次
 *      的语义只有 `auth.authedFetch` 一处实现；
 *   2. 用 `toCapabilityUnavailable('posts', err)` 包一层 catch，保留"整域 404"的语义。
 *
 * 服务端契约（票 #27 冻结后以它为准，这里先按门面形状记下）：
 *   GET    /api/posts?sort=latest|hot&page=1   → {items, hasMore}
 *   GET    /api/posts/:id                      → Post
 *   POST   /api/posts                          → Post
 *   POST   /api/posts/:id/like                 → {liked, likeCount}
 *   GET    /api/posts/:id/comments             → Comment[]
 *   POST   /api/posts/:id/comments             → Comment
 *   DELETE /api/posts/comments/:commentId      → 204
 */
import { CapabilityUnavailable } from '../mode'
import type { Comment, ListPostsInput, ListPostsResult, Post } from '../../types/posts'

/** 整域未上线的统一抛点：改实现时逐个替换，不必记文案。 */
function unavailable(): never {
  throw new CapabilityUnavailable('posts')
}

export async function listPosts(_input: ListPostsInput = {}): Promise<ListPostsResult> {
  return unavailable()
}

export async function getPost(_id: number): Promise<Post> {
  return unavailable()
}

export async function createPost(_input: { title: string; content: string; images?: string[] }): Promise<Post> {
  return unavailable()
}

export async function toggleLike(_postId: number): Promise<{ liked: boolean; likeCount: number }> {
  return unavailable()
}

export async function listComments(_postId: number): Promise<Comment[]> {
  return unavailable()
}

export async function addComment(_input: { postId: number; content: string }): Promise<Comment> {
  return unavailable()
}

export async function deleteComment(_id: number): Promise<void> {
  return unavailable()
}
