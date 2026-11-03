/**
 * 帖子域 · 本地 mock（wayfinder #22 从 `api/posts.ts` 整体迁出，数据与函数逐字不变）。
 *
 * 这里只管"没有后端时怎么跑"：数据在内存里，写操作改内存并立刻反映到后续读。
 * 页面**不直接** import 本文件——统一走门面 `api/posts.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_POSTS` 开关决定是否用这一份。
 */
import { HttpError } from '../http'
import type { Comment, ListPostsInput, ListPostsResult, Post } from '../../types/posts'
import { MOCK_ANIME_THREADS } from './animeSeed'
import { isFavorited } from './favoriteStore'
import { mockAuthorVisibleInFlow, mockFolloweeIds } from './follows'

// ---------- Mock 数据 ----------

const MOCK_USERS = [
  { id: 1, displayName: '月夜猫', avatarChar: '月', avatarColor: '#00a1d6' },
  { id: 2, displayName: '放映员', avatarChar: '放', avatarColor: '#fb7299' },
  { id: 3, displayName: '阿梓', avatarChar: '阿', avatarColor: '#8b7bf0' },
  { id: 4, displayName: '夜猫子', avatarChar: '夜', avatarColor: '#3ddc97' },
  { id: 5, displayName: '喵喵', avatarChar: '喵', avatarColor: '#f5c76a' },
]

/**
 * 帖子的示例封面（票 #78）。
 *
 * 同源 SVG，不是远程图床：生产 CSP 是 `img-src 'self' data:`，远程图会被拦。
 * 有了它，`images` 非空的渲染路径才在 mock 域里跑得到 —— 帖封面→详情的
 * 共享元素（`data-shared="post-cover:<id>"`）也就有了可演示、可验收的样本。
 * 只给前两条，其余保持空数组：列表里"有封面"与"没封面"两种卡片要同时在。
 */
const COVER = '/demo/cover.svg'

const MOCK_POSTS: Post[] = [
  {
    id: 1,
    title: '今晚一起补《葬送的芙莉莲》',
    content: '今晚 8 点一起补《葬送的芙莉莲》第 4 集，房间码 MOON04，来的人发个爪。\n同步看番真的快乐，弹幕节奏拉满。',
    authorId: 1,
    author: MOCK_USERS[0],
    images: [COVER],
    likeCount: 23,
    commentCount: 7,
    liked: false,
    favorited: false,
    createdAt: new Date(Date.now() - 2 * 60 * 1000).toISOString(),
  },
  {
    id: 2,
    title: '卡顿换父逻辑又调了一版',
    content: '把「一起看」的卡顿换父逻辑又调了一版，现在切主播放器基本无感了。\n来来回回测了好多轮，终于找到稳定的阈值。',
    authorId: 2,
    author: MOCK_USERS[1],
    images: [COVER],
    likeCount: 41,
    commentCount: 12,
    liked: false,
    favorited: false,
    createdAt: new Date(Date.now() - 18 * 60 * 1000).toISOString(),
  },
  {
    id: 3,
    title: '求一个能一起看老番的房',
    content: '求一个能一起看老番的房，最好带弹幕节奏……\n刚入坑，有没有大佬带带。',
    authorId: 3,
    author: MOCK_USERS[2],
    images: [],
    likeCount: 9,
    commentCount: 3,
    liked: false,
    favorited: false,
    createdAt: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
  },
  {
    id: 4,
    title: '今晚片单：吉卜力连播',
    content: '今晚的片单：吉卜力连播。房间码 GHIBLI，静音党勿入。\n从《千与千寻》开始到《起风了》，一起来沉浸式重温。',
    authorId: 4,
    author: MOCK_USERS[3],
    images: [],
    likeCount: 57,
    commentCount: 15,
    liked: false,
    favorited: false,
    createdAt: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
  },
  {
    id: 5,
    title: '月喵的动效真顺滑',
    content: '月喵的动效真顺滑，morph 变形那一下每次看都觉得治愈。\nicon-first 的标题栏也很清爽，继续加油！',
    authorId: 5,
    author: MOCK_USERS[4],
    images: [],
    likeCount: 18,
    commentCount: 5,
    liked: false,
    favorited: false,
    createdAt: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
  },
  {
    id: 6,
    title: '如何选择适合的影片格式？',
    content: '想问问大家一般用什么格式分享影片？\nMP4 最通用，但 MKV 保留多音轨，WebM 适合网络串流。\n欢迎讨论～',
    authorId: 1,
    author: MOCK_USERS[0],
    images: [],
    likeCount: 15,
    commentCount: 8,
    liked: false,
    favorited: false,
    createdAt: new Date(Date.now() - 36 * 60 * 60 * 1000).toISOString(),
  },
  {
    id: 7,
    title: '推荐一部冷门佳作',
    content: '推荐一部冷门佳作《横道世之介》\n看了三遍，每一次都有新感受。有谁看过吗？',
    authorId: 3,
    author: MOCK_USERS[2],
    images: [],
    likeCount: 32,
    commentCount: 11,
    liked: false,
    favorited: false,
    createdAt: new Date(Date.now() - 48 * 60 * 60 * 1000).toISOString(),
  },
]

const MOCK_COMMENTS: Record<number, Comment[]> = {
  1: [
    { id: 101, postId: 1, authorId: 2, author: MOCK_USERS[1], content: '准时到！', createdAt: new Date(Date.now() - 1 * 60 * 1000).toISOString() },
  ],
  4: [
    { id: 401, postId: 4, authorId: 1, author: MOCK_USERS[0], content: '千寻太经典了，报名！', createdAt: new Date(Date.now() - 60 * 60 * 1000).toISOString() },
    { id: 402, postId: 4, authorId: 5, author: MOCK_USERS[4], content: '起风了看一次哭一次😭', createdAt: new Date(Date.now() - 30 * 60 * 1000).toISOString() },
  ],
}

/** 当前登录用户 id（本地 mock：固定为 1）。 */
const CURRENT_USER_ID = 1
const CURRENT_USER = MOCK_USERS[0]

// ---------- 主题串（wayfinder #36） ----------
//
// 主题串就是**带 `animeId` 的帖子**：板块列表（forum 域）只提供"有哪些 id"，
// 详情与回复完全复用本域的 getPost / listComments / addComment。因此这里必须把
// 同一批数据也铺进帖子 mock —— 否则 mock 态点进主题串会显示"不存在"，
// 而那条路径正是验收第 1 条要走的四层之一。
//
// 数据来自 `animeSeed.ts`（forum mock 与这里共用同一份纯数据），id / 作者 / 时间
// 因此天然对齐，不需要两处手抄。

for (const thread of MOCK_ANIME_THREADS) {
  MOCK_POSTS.push({
    id: thread.id,
    title: thread.title,
    content: thread.content,
    authorId: thread.author.id,
    author: thread.author,
    images: [],
    likeCount: thread.likeCount,
    commentCount: thread.commentCount,
    liked: false,
    favorited: false,
    createdAt: thread.createdAt,
    animeId: thread.animeId,
    lastReplyAt: thread.lastReplyAt,
  })
  if (thread.comments.length > 0) {
    MOCK_COMMENTS[thread.id] = thread.comments.map((c) => ({
      id: c.id,
      postId: thread.id,
      authorId: c.author.id,
      author: c.author,
      content: c.content,
      createdAt: c.createdAt,
    }))
  }
}

// ---------- 排序（按最新 / 按热度 / 按关注） ----------

/**
 * 排序（最新 / 热门 / 关注）。
 *
 * 关注流（票 #66）在 mock 里同样要过两条：缩到"我关注的人"、再滤掉不可见的作者。
 * 与服务端 `applyFollowingScope` + `applyAuthorVisibility` 一一对应 ——
 * 只是那边是 SQL 谓词，这里是内存过滤。
 */
function sortPosts(posts: Post[], sort: 'latest' | 'hot' | 'following'): Post[] {
  const list = [...posts]
  if (sort === 'hot') {
    list.sort((a, b) => (b.likeCount + b.commentCount) - (a.likeCount + a.commentCount))
    return list
  }
  if (sort === 'following') {
    const followees = new Set(mockFolloweeIds())
    return list
      .filter((p) => followees.has(p.authorId) && mockAuthorVisibleInFlow(p.authorId))
      .sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
  }
  list.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
  return list
}

// ---------- 导出的 API ----------

const PAGE_SIZE = 20
const allPosts = [...MOCK_POSTS]

/** 帖子列表（排序 + 分页）。 */
export async function listPosts(input: ListPostsInput = {}): Promise<ListPostsResult> {
  const { sort = 'latest', page = 1 } = input
  const sorted = sortPosts(allPosts, sort)
  const start = (page - 1) * PAGE_SIZE
  // 收藏态是**跨对象**的真源（`favoriteStore`），不在 Post 行上存 ——
  // 与真实现同构：服务端每次列表都现查一次，前端这里也一样现问一次。
  const items = sorted.slice(start, start + PAGE_SIZE).map(withFavorite)
  return { items, hasMore: start + PAGE_SIZE < sorted.length }
}

/**
 * 给一行帖子补上 `favorited`（mock 态的"批量回填"）。
 *
 * 真实现里这个字段由服务端在同一个响应里回填（决议 #48：禁 N+1）；
 * mock 这里等价 —— 一次内存查表，没有额外请求。
 */
function withFavorite(post: Post): Post {
  return { ...post, favorited: isFavorited('post', String(post.id)) }
}

/**
 * 某位作者的全部帖（票 #68 的档案页内容块用）。
 *
 * 返回**未分页**的全集，由调用方切页：档案页的无限滚动与帖子列表是同一套
 * limit/offset 语义，把切页放在一处（`api/mock/users.ts`）比两边各切一次更不容易错。
 *
 * 过滤掉不可见作者（被封禁 / 双向拉黑）与服务端 `applyAuthorVisibility` 同口径 ——
 * 但注意档案页**先判账号可见性**（不存在/被封禁直接 404），所以这里实际上只需要
 * 处理"我看 TA 的帖"这一侧：拉黑存续期公开内容仍可看（票 #42 §4），
 * 所以**不**按拉黑过滤。这与 `mockAuthorVisibleInFlow`（关注流要滤）刻意不同。
 */
export function mockPostsByAuthor(authorId: number): Post[] {
  return allPosts
    .filter((p) => p.authorId === authorId)
    .map(withFavorite)
    .sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
}

/** 单篇帖子（主题串也是帖子）。 */
export async function getPost(id: number): Promise<Post> {
  const post = allPosts.find((p) => p.id === id)
  if (!post) {
    // 必须是 **404 HttpError**，不能是裸 Error：页面的分诊会把裸 Error 当成
    // "网络不可达"（`toHttpError` 的 status 0），于是"这条主题串不存在"会被显示成
    // 「无法连接服务器」—— 一个会让人去查网络的假象（wayfinder #36 发现）。
    throw new HttpError({ status: 404, code: 'NOT_FOUND', message: '内容不存在' })
  }
  return withFavorite(post)
}

/** 创建帖子（标题 + 纯文本正文 + 可选图片数组）。 */
export async function createPost(input: { title: string; content: string; images?: string[] }): Promise<Post> {
  const id = allPosts.length > 0 ? Math.max(...allPosts.map((p) => p.id)) + 1 : 1
  const post: Post = {
    id,
    title: input.title,
    content: input.content,
    authorId: CURRENT_USER_ID,
    author: CURRENT_USER,
    images: input.images ?? [],
    likeCount: 0,
    commentCount: 0,
    liked: false,
    favorited: false,
    createdAt: new Date().toISOString(),
  }
  allPosts.unshift(post)
  return post
}

/** 点赞/取消点赞（乐观更新）。 */
export async function toggleLike(postId: number): Promise<{ liked: boolean; likeCount: number }> {
  const post = allPosts.find((p) => p.id === postId)
  if (!post) throw new Error('帖子不存在')
  post.liked = !post.liked
  post.likeCount += post.liked ? 1 : -1
  return { liked: post.liked, likeCount: post.likeCount }
}

/** 帖子评论列表。 */
export async function listComments(postId: number): Promise<Comment[]> {
  return MOCK_COMMENTS[postId] ?? []
}

/** 添加评论（评论扁平，无楼中楼）。 */
export async function addComment(input: { postId: number; content: string }): Promise<Comment> {
  const comments = MOCK_COMMENTS[input.postId] ?? []
  const id = comments.length > 0 ? Math.max(...comments.map((c) => c.id)) + 1 : input.postId * 100 + 1
  const comment: Comment = {
    id,
    postId: input.postId,
    authorId: CURRENT_USER_ID,
    author: CURRENT_USER,
    content: input.content,
    createdAt: new Date().toISOString(),
  }
  comments.unshift(comment)
  MOCK_COMMENTS[input.postId] = comments
  // 更新帖子评论数
  const post = allPosts.find((p) => p.id === input.postId)
  if (post) post.commentCount += 1
  return comment
}

/** 删除评论（仅本人可删）。 */
export async function deleteComment(id: number): Promise<void> {
  for (const postId of Object.keys(MOCK_COMMENTS)) {
    const comments = MOCK_COMMENTS[Number(postId)]
    const idx = comments.findIndex((c) => c.id === id)
    if (idx !== -1) {
      if (comments[idx].authorId !== CURRENT_USER_ID) throw new Error('只能删除自己的评论')
      comments.splice(idx, 1)
      // 更新帖子评论数
      const post = allPosts.find((p) => p.id === Number(postId))
      if (post) post.commentCount -= 1
      return
    }
  }
  throw new Error('评论不存在')
}

/**
 * mock 态的标题查表（收藏列表要用它给每一行配标题）。
 *
 * 找不到返回 `null` 而不是空串：收藏列表必须能区分"对象没了"（→ 失效态）
 * 与"对象还在但没标题"（→ 显示占位文案），空串会把两者糊成一样。
 */
export function mockPostTitle(id: number): string | null {
  return allPosts.find((p) => p.id === id)?.title ?? null
}

/**
 * 删除自己的帖子（票 #70）。
 *
 * 与真实实现的三条对齐：
 *   1. **只有作者能删** —— 不是作者抛 403 形状的 `HttpError`（不是裸 Error，
 *      否则页面会把它显示成"网络不可达"）；
 *   2. 不存在抛 404；
 *   3. 连带的评论一起清掉（服务端靠 `ON DELETE CASCADE`，mock 手动清）。
 *      **收藏条目刻意不动**：那是私人书签，服务端也没有外键，失效条目要留着
 *      让用户在收藏列表里看见「已失效」并自己移除（决议 #48 §5 / 票 #65 G5a）。
 */
export async function deletePost(id: number): Promise<void> {
  const idx = allPosts.findIndex((p) => p.id === id)
  if (idx === -1) {
    throw new HttpError({ status: 404, code: 'NOT_FOUND', message: '帖子不存在' })
  }
  if (allPosts[idx].authorId !== CURRENT_USER_ID) {
    throw new HttpError({ status: 403, code: 'FORBIDDEN', message: '只能删除自己的帖子' })
  }
  allPosts.splice(idx, 1)
  delete MOCK_COMMENTS[id]
}
