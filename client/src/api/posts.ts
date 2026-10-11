/**
 * 帖子占位 API（PROTOTYPE — 本地 mock 实现，后端上线后替换实现、形状不变）。
 *
 * 接口形状约定（见 wayfinder #5）：
 *   listPosts({sort, page}) → {items, hasMore}
 *   getPost(id) → Post
 *   createPost({title, content, images}) → Post
 *   toggleLike(postId) → {liked, likeCount}
 *   listComments(postId) → Comment[]
 *   addComment({postId, content}) → Comment
 *   deleteComment(id) → void
 */
import type { Post, Comment } from '../types/posts'

// ---------- Mock 数据 ----------

const MOCK_USERS = [
  { id: 1, displayName: '月夜猫', avatarChar: '月', avatarColor: '#00a1d6' },
  { id: 2, displayName: '放映员', avatarChar: '放', avatarColor: '#fb7299' },
  { id: 3, displayName: '阿梓', avatarChar: '阿', avatarColor: '#8b7bf0' },
  { id: 4, displayName: '夜猫子', avatarChar: '夜', avatarColor: '#3ddc97' },
  { id: 5, displayName: '喵喵', avatarChar: '喵', avatarColor: '#f5c76a' },
]

const MOCK_POSTS: Post[] = [
  {
    id: 1,
    title: '今晚一起补《葬送的芙莉莲》',
    content: '今晚 8 点一起补《葬送的芙莉莲》第 4 集，房间码 MOON04，来的人发个爪。\n同步看番真的快乐，弹幕节奏拉满。',
    authorId: 1,
    author: MOCK_USERS[0],
    images: [],
    likeCount: 23,
    commentCount: 7,
    liked: false,
    createdAt: new Date(Date.now() - 2 * 60 * 1000).toISOString(),
  },
  {
    id: 2,
    title: '卡顿换父逻辑又调了一版',
    content: '把「一起看」的卡顿换父逻辑又调了一版，现在切主播放器基本无感了。\n来来回回测了好多轮，终于找到稳定的阈值。',
    authorId: 2,
    author: MOCK_USERS[1],
    images: [],
    likeCount: 41,
    commentCount: 12,
    liked: false,
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

// ---------- 排序（按最新 / 按热度） ----------

function sortPosts(posts: Post[], sort: 'latest' | 'hot'): Post[] {
  const list = [...posts]
  if (sort === 'hot') {
    list.sort((a, b) => (b.likeCount + b.commentCount) - (a.likeCount + a.commentCount))
  } else {
    list.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
  }
  return list
}

// ---------- 导出的 API ----------

const PAGE_SIZE = 20
const allPosts = [...MOCK_POSTS]

export interface ListPostsInput {
  sort?: 'latest' | 'hot'
  page?: number
}

export interface ListPostsResult {
  items: Post[]
  hasMore: boolean
}

/** 帖子列表（排序 + 分页）。 */
export async function listPosts(input: ListPostsInput = {}): Promise<ListPostsResult> {
  const { sort = 'latest', page = 1 } = input
  const sorted = sortPosts(allPosts, sort)
  const start = (page - 1) * PAGE_SIZE
  const items = sorted.slice(start, start + PAGE_SIZE)
  return { items, hasMore: start + PAGE_SIZE < sorted.length }
}

/** 单篇帖子。 */
export async function getPost(id: number): Promise<Post> {
  const post = allPosts.find((p) => p.id === id)
  if (!post) throw new Error('帖子不存在')
  return { ...post }
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
