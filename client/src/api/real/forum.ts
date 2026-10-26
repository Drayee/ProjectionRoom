/**
 * 番剧论坛域 · 真实 HTTP 实现（wayfinder #36；服务端由 #37 / #38 冻结）。
 *
 * 三条端点，前两条属于目录（#37），第三条属于帖子域（#38，但语义是"番剧的板块"）：
 *
 *   GET /api/animes?q&limit&offset                  → {items, total, limit, offset}
 *   GET /api/animes/:animeId                        → Anime
 *   GET /api/animes/:animeId/posts?sort&limit&offset → {items, hasMore}（postView 行）
 *
 * 主题串**详情与回复不在这里**：它们就是 `/api/posts/:id` 与评论端点，由
 * `api/posts.ts` 门面提供（票面明写"复用 PostDetailPage，不造第二套"）。
 * 这里只把行数据复用同一条归一化（`toPost`），否则 `animeId` / `lastReplyAt`
 * 会在列表与详情之间一处有一处漏。
 *
 * ⚠ 404 的两种含义必须分开（与 #32 的 users 域同一条判据）：
 *   - **JSON 404 + `NOT_FOUND`** = 这个番剧/板块不存在（页面的"板块不存在"空态）；
 *   - **纯文本 404（本地兜底码 `HTTP_404`）** = 这一组路由没注册（能力未上线）。
 * 目录列表是域探针，它自己的 404 就是能力未上线，因此直接用 `toCapabilityUnavailable`。
 */
import { HttpError, query, requestJSON } from '../http'
import { toCapabilityUnavailable, CapabilityUnavailable } from '../mode'
import { useAuthStore } from '../../stores/auth'
import { toPost } from './posts'
import type { AuthedFetch } from '../http'
import type { Anime, AnimePage } from '../../types/forum'
import type { ListPostsResult } from '../../types/posts'

/** 与服务端默认页大小同值。 */
const PAGE_SIZE = 20

function fetcher(): AuthedFetch {
  return useAuthStore().authedFetch
}

/** 服务端 `animeView`（#37）。 */
interface ServerAnime {
  id: number
  title: string
  aliases: string[]
  coverUrl: string
  summary: string
  createdAt: string
}

/** 服务端 `postView`（#38 的板块列表用它）。只声明本文件用到的键，形状由 `toPost` 收口。 */
interface ServerThreadRow {
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
  animeId?: number
  lastReplyAt?: string
}

function toAnime(raw: ServerAnime): Anime {
  return {
    id: raw.id,
    title: raw.title,
    // 服务端 jsonb 理论上不会给 null，但"缺字段"与"空数组"在界面上一样，这里归一成数组。
    aliases: Array.isArray(raw.aliases) ? raw.aliases.filter((x): x is string => typeof x === 'string') : [],
    coverUrl: typeof raw.coverUrl === 'string' ? raw.coverUrl : '',
    summary: typeof raw.summary === 'string' ? raw.summary : '',
    createdAt: typeof raw.createdAt === 'string' ? raw.createdAt : '',
  }
}

/**
 * 404 的两种含义：路由没注册 → 能力未上线；番剧不存在 → 原样上抛（页面说"板块不存在"）。
 *
 * 只看 404 且错误码是本地兜底的 `HTTP_404`（响应体不是 JSON，正是 gin NoRoute 的纯文本形状）。
 */
function toForumError(err: unknown): Error {
  if (err instanceof HttpError && err.status === 404 && err.code === 'HTTP_404') {
    return new CapabilityUnavailable('forum', '番剧论坛后端尚未上线（/api/animes/* 未注册）')
  }
  return err instanceof Error ? err : new Error(String(err))
}

/** 目录列表 = 域探针：它自己 404 就是整域没上线。 */
export async function listAnimes(
  input: { q?: string; limit?: number; offset?: number } = {},
): Promise<AnimePage> {
  const limit = input.limit ?? PAGE_SIZE
  const offset = input.offset ?? 0
  try {
    const body = await requestJSON<{
      items: ServerAnime[]
      total: number
      limit: number
      offset: number
    }>(fetcher(), `/api/animes${query({ q: input.q, limit, offset })}`)
    const items = Array.isArray(body?.items) ? body.items.map(toAnime) : []
    return {
      items,
      total: typeof body?.total === 'number' ? body.total : items.length,
      limit: typeof body?.limit === 'number' ? body.limit : limit,
      offset: typeof body?.offset === 'number' ? body.offset : offset,
    }
  } catch (err) {
    throw toCapabilityUnavailable('forum', err)
  }
}

/** 单个番剧（板块头部用）。 */
export async function getAnime(animeId: number): Promise<Anime> {
  try {
    const body = await requestJSON<ServerAnime>(fetcher(), `/api/animes/${animeId}`)
    if (!body) {
      throw new HttpError({ status: 500, code: 'HTTP_BAD_RESPONSE', message: '服务端返回了空的番剧条目' })
    }
    return toAnime(body)
  } catch (err) {
    throw toForumError(err)
  }
}

/** 某个板块下的主题串（服务端已按最新回复排好，前端不再排一遍）。 */
export async function listAnimeThreads(input: {
  animeId: number
  sort?: 'latest-reply' | 'latest'
  limit?: number
  offset?: number
}): Promise<ListPostsResult> {
  const limit = input.limit ?? PAGE_SIZE
  const offset = input.offset ?? 0
  try {
    const body = await requestJSON<{ items: ServerThreadRow[]; hasMore: boolean }>(
      fetcher(),
      `/api/animes/${input.animeId}/posts${query({
        sort: input.sort ?? 'latest-reply',
        limit,
        offset,
      })}`,
    )
    return {
      items: Array.isArray(body?.items) ? body.items.map((raw) => toPost(raw)) : [],
      hasMore: body?.hasMore === true,
    }
  } catch (err) {
    throw toForumError(err)
  }
}
