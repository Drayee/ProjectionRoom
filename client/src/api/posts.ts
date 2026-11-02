/**
 * 帖子域门面（唯一被页面 import 的出口）。
 *
 * 形状在 #5 定稿、#22 之后**逐字不变**：
 *   listPosts({sort, page}) → {items, hasMore}
 *   getPost(id) → Post
 *   createPost({title, content, images}) → Post
 *   toggleLike(postId) → {liked, likeCount}
 *   listComments(postId) → Comment[]
 *   addComment({postId, content}) → Comment
 *   deleteComment(id) → void
 *   deletePost(id) → void            （票 #70）
 *
 * 实现由 `api/mode.ts` 的 `VITE_MOCK_POSTS` 在**模块加载时**二选一：
 * 默认 mock（`api/mock/posts.ts`），配成 `0`/`false`/`off`/`no` 时用真实实现
 * （`api/real/posts.ts`）。页面不需要知道自己在跟谁说话。
 */
import { isMock } from './mode'
import * as mock from './mock/posts'
import * as real from './real/posts'

/**
 * 两份实现形状必须一致——类型标注就是这条契约的编译期检查点。
 * 任何一边签名漂移，这里立刻报错，而不是等到页面在运行期炸。
 */
type PostsApi = {
  listPosts(input?: import('../types/posts').ListPostsInput): Promise<import('../types/posts').ListPostsResult>
  getPost(id: number): Promise<import('../types/posts').Post>
  createPost(input: { title: string; content: string; images?: string[] }): Promise<import('../types/posts').Post>
  toggleLike(postId: number): Promise<{ liked: boolean; likeCount: number }>
  listComments(postId: number): Promise<import('../types/posts').Comment[]>
  addComment(input: { postId: number; content: string }): Promise<import('../types/posts').Comment>
  deleteComment(id: number): Promise<void>
  /**
   * 删除自己的帖子（票 #70）。
   *
   * 服务端判据是 I3（授权 100% 在服务端）：不是作者 → 403，不存在 → 404。
   * 页面据此区分"这帖已经被删了"（从列表里移除即可）与"你不能删它"（提示）。
   */
  deletePost(id: number): Promise<void>
}

const impl: PostsApi = isMock('posts') ? mock : real

export const listPosts = impl.listPosts
export const getPost = impl.getPost
export const createPost = impl.createPost
export const toggleLike = impl.toggleLike
export const listComments = impl.listComments
export const addComment = impl.addComment
export const deleteComment = impl.deleteComment
export const deletePost = impl.deletePost

/** 类型再导出：页面从门面拿类型，不必知道 `types/posts.ts` 的位置。 */
export type { Comment, ListPostsInput, ListPostsResult, Post } from '../types/posts'
