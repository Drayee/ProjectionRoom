/**
 * 番剧论坛域 · 本地 mock（wayfinder #36）。
 *
 * 页面不直接 import 本文件：统一走门面 `api/forum.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_FORUM` 决定用这一份还是真实实现。
 *
 * 它只负责**前两层**（目录 / 板块主题串列表）；主题串详情与回复复用帖子域
 * （`api/posts.ts`）。为了让这两半在 mock 态对得上，板块里的 id 必须也能在帖子 mock
 * 里找到 —— 两边共用 `animeSeed.ts` 的同一份数据（见那个文件的说明）。
 *
 * ⚠ 跨域注意：mock 的论坛 + 真实的帖子（`VITE_MOCK_FORUM=1`、`VITE_MOCK_POSTS=0`）
 * 是一组**不自洽**的开关组合 —— 板块里列的是本地假 id，而详情页会去真后端查。
 * 默认值两者都是 mock，所以正常开发不会撞上；要单独翻真时记得两个域一起翻。
 */
import { HttpError } from '../http'
import { MOCK_ANIMES, MOCK_ANIME_THREADS, type MockAnimeThread } from './animeSeed'
import type { Anime, AnimePage } from '../../types/forum'
import type { ListPostsResult, Post } from '../../types/posts'

/** 与服务端共用的单页条数（目录与板块都用 20）。 */
const PAGE_SIZE = 20

function notFound(): HttpError {
  return new HttpError({ status: 404, code: 'NOT_FOUND', message: '番剧不存在' })
}

function toThreadPost(thread: MockAnimeThread): Post {
  return {
    id: thread.id,
    title: thread.title,
    content: thread.content,
    authorId: thread.author.id,
    author: thread.author,
    images: [],
    likeCount: thread.likeCount,
    commentCount: thread.commentCount,
    liked: false,
    createdAt: thread.createdAt,
    animeId: thread.animeId,
    lastReplyAt: thread.lastReplyAt,
  }
}

/** `GET /api/animes?q&limit&offset` 的 mock：标题与别名都参与匹配（与服务端同口径）。 */
export async function listAnimes(
  input: { q?: string; limit?: number; offset?: number } = {},
): Promise<AnimePage> {
  const q = (input.q ?? '').trim().toLowerCase()
  const limit = Math.max(1, input.limit ?? PAGE_SIZE)
  const offset = Math.max(0, input.offset ?? 0)

  const matched =
    q === ''
      ? MOCK_ANIMES
      : MOCK_ANIMES.filter(
          (a) => a.title.toLowerCase().includes(q) || a.aliases.some((alias) => alias.toLowerCase().includes(q)),
        )

  return {
    items: matched.slice(offset, offset + limit).map((a) => ({ ...a, aliases: [...a.aliases] })),
    total: matched.length,
    limit,
    offset,
  }
}

/** `GET /api/animes/:animeId` 的 mock。 */
export async function getAnime(animeId: number): Promise<Anime> {
  const anime = MOCK_ANIMES.find((a) => a.id === animeId)
  if (!anime) throw notFound()
  return { ...anime, aliases: [...anime.aliases] }
}

/**
 * `GET /api/animes/:animeId/posts?sort=&limit=&offset` 的 mock。
 *
 * 排序键与服务端一致：`latest-reply` 按 `lastReplyAt` 降序（没有回复的用创建时间兜底），
 * `latest` 按创建时间降序。番剧不存在 → 404（"板块不存在"与"板块是空的"必须能分开）。
 */
export async function listAnimeThreads(input: {
  animeId: number
  sort?: 'latest-reply' | 'latest'
  limit?: number
  offset?: number
}): Promise<ListPostsResult> {
  if (!MOCK_ANIMES.some((a) => a.id === input.animeId)) throw notFound()

  const limit = Math.max(1, input.limit ?? PAGE_SIZE)
  const offset = Math.max(0, input.offset ?? 0)
  const sort = input.sort ?? 'latest-reply'

  const rows = MOCK_ANIME_THREADS.filter((t) => t.animeId === input.animeId).sort((a, b) => {
    if (sort === 'latest') return new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()
    return new Date(b.lastReplyAt).getTime() - new Date(a.lastReplyAt).getTime()
  })

  return {
    items: rows.slice(offset, offset + limit).map(toThreadPost),
    hasMore: offset + limit < rows.length,
  }
}
