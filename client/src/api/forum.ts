/**
 * 番剧论坛域门面（唯一被页面 import 的出口）。wayfinder #36。
 *
 * 形状（四层里的前两层；后两层复用帖子域）：
 *   listAnimes({q, limit, offset})            → AnimePage   目录（搜索 + 分页）
 *   getAnime(animeId)                         → Anime       板块头部
 *   listAnimeThreads({animeId, sort, ...})    → {items, hasMore} 板块下的主题串
 *
 * **主题串详情与回复不在本门面**：主题串就是带 `animeId` 的帖子，详情页与评论
 * 直接用 `api/posts.ts`（票面要求"不造第二套"）。页面因此同时 import 两个门面，
 * 这是刻意的：它们本来就是两个域，而主题串是两者的接缝。
 *
 * 实现由 `api/mode.ts` 的 `VITE_MOCK_FORUM` 在**模块加载时**二选一：默认 mock，
 * 配成 `0`/`false`/`off`/`no` 时用真实实现（`api/real/forum.ts`）。
 *
 * ⚠ 主题串详情走帖子域的开关：mock 态下必须**两个域都是 mock** 才对得上（共用
 * `mock/animeSeed.ts` 的 id）。翻真时 `VITE_MOCK_FORUM` 与 `VITE_MOCK_POSTS` 要一起翻。
 */
import { isMock } from './mode'
import * as mock from './mock/forum'
import * as real from './real/forum'

/**
 * 两份实现形状必须一致 —— 类型标注就是这条契约的编译期检查点。
 */
type ForumApi = {
  listAnimes(input?: {
    q?: string
    limit?: number
    offset?: number
  }): Promise<import('../types/forum').AnimePage>
  getAnime(animeId: number): Promise<import('../types/forum').Anime>
  listAnimeThreads(input: {
    animeId: number
    sort?: 'latest-reply' | 'latest'
    limit?: number
    offset?: number
  }): Promise<import('../types/posts').ListPostsResult>
}

const impl: ForumApi = isMock('forum') ? mock : real

export const listAnimes = impl.listAnimes
export const getAnime = impl.getAnime
export const listAnimeThreads = impl.listAnimeThreads

/** 目录单页条数（与服务端 `animePageDefault` 同值）。 */
export const FORUM_ANIME_PAGE_SIZE = 20
/** 板块主题串单页条数。 */
export const FORUM_THREADS_PAGE_SIZE = 20

/** 三种边界各有自己的文案（验收第 2 条）：空目录 / 搜不到 / 空板块。 */
export const FORUM_EMPTY_TEXT = '目录里还没有番剧'
export const FORUM_NO_MATCH_TEXT = '没有匹配的番剧'
export const FORUM_THREADS_EMPTY_TEXT = '这个板块还没有主题串'

/** 类型再导出：页面从门面拿类型，不必知道 `types/forum.ts` 的位置。 */
export type { Anime, AnimePage } from '../types/forum'
export type { Post } from '../types/posts'
