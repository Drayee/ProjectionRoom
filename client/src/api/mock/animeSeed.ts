/**
 * 番剧论坛的共享 mock 数据（wayfinder #36）。
 *
 * 为什么单独一个文件、而不是让论坛 mock 自己造一份：**主题串就是带 `animeId` 的帖子**，
 * 而主题串详情与回复复用 `PostDetailPage`（走 `api/posts` 门面）。mock 态下要让
 * 「目录 → 板块 → 主题串 → 回复」四层真的走得通，板块列表里的 id 必须在**帖子 mock**
 * 里也存在 —— 两份各自随机的数据必然对不上，症状是"点进主题串显示不存在"。
 *
 * 所以这里只放**纯数据**（无行为、无依赖），由两个 mock 共同消费：
 *   - `api/mock/posts.ts` 把它铺成帖子与评论（详情/回复四层的后面两层）；
 *   - `api/mock/forum.ts` 用它做目录、板块与主题串列表（前面两层）。
 *
 * 头像字段直接写死（而不是调用 `avatarOf`）：这里的数据同时给两个域用，写死能保证
 * 同一个作者在板块列表与详情页里颜色/首字母一致；`avatarOf` 的派生仍留给真实实现。
 */

export interface MockAnime {
  id: number
  title: string
  aliases: string[]
  /** 外链封面；空串 = 没有封面（页面降级成首字色块，不请求网络图片）。 */
  coverUrl: string
  summary: string
  createdAt: string
}

export interface MockAuthor {
  id: number
  displayName: string
  avatarChar: string
  avatarColor: string
}

export interface MockThreadComment {
  id: number
  author: MockAuthor
  content: string
  createdAt: string
}

export interface MockAnimeThread {
  id: number
  animeId: number
  title: string
  content: string
  author: MockAuthor
  likeCount: number
  /** 与 `comments.length` 保持一致（mock 里不做"计数与列表不同源"的实验）。 */
  commentCount: number
  createdAt: string
  lastReplyAt: string
  comments: MockThreadComment[]
}

const minutesAgo = (n: number): string => new Date(Date.now() - n * 60 * 1000).toISOString()

const AUTHOR_MOONCAT: MockAuthor = { id: 1, displayName: '月夜猫', avatarChar: '月', avatarColor: '#00a1d6' }
const AUTHOR_PROJECTIONIST: MockAuthor = { id: 2, displayName: '放映员', avatarChar: '放', avatarColor: '#fb7299' }
const AUTHOR_AZI: MockAuthor = { id: 3, displayName: '阿梓', avatarChar: '阿', avatarColor: '#8b7bf0' }
const AUTHOR_NIGHTOWL: MockAuthor = { id: 4, displayName: '夜猫子', avatarChar: '夜', avatarColor: '#3ddc97' }
const AUTHOR_MIAO: MockAuthor = { id: 5, displayName: '喵喵', avatarChar: '喵', avatarColor: '#f5c76a' }

/**
 * 番剧目录：五部，其中 4 / 5 号**故意没有主题串** —— 空板块是三种边界之一，
 * mock 态必须能走到它，否则那条文案只能靠读代码相信。
 */
export const MOCK_ANIMES: MockAnime[] = [
  {
    id: 1,
    title: '葬送的芙莉莲',
    aliases: ['Sousou no Frieren', '芙莉莲'],
    coverUrl: '',
    summary: '魔王讨伐之后，精灵魔法使芙莉莲重新踏上旅途，学习「人类的时间」。',
    createdAt: minutesAgo(60 * 24 * 40),
  },
  {
    id: 2,
    title: '吉卜力剧场版',
    aliases: ['Ghibli', '吉卜力'],
    coverUrl: '',
    summary: '从《千与千寻》到《起风了》：一起重温吉卜力的夏天与风。',
    createdAt: minutesAgo(60 * 24 * 35),
  },
  {
    id: 3,
    title: '新世纪福音战士',
    aliases: ['EVA', 'Evangelion', '新剧场版'],
    coverUrl: '',
    summary: 'TV 版与新剧场版的顺序、意识流结局，以及那些年我们没看懂的 AT 力场。',
    createdAt: minutesAgo(60 * 24 * 30),
  },
  {
    id: 4,
    title: '孤独摇滚！',
    aliases: ['Bocchi the Rock', '波奇'],
    coverUrl: '',
    summary: '社恐吉他手后藤一里的乐队日常。本板块还没有主题串。',
    createdAt: minutesAgo(60 * 24 * 12),
  },
  {
    id: 5,
    title: '星际牛仔',
    aliases: ['Cowboy Bebop', '赏金猎人'],
    coverUrl: '',
    summary: '爵士、赏金与太空。See you space cowboy… 本板块还没有主题串。',
    createdAt: minutesAgo(60 * 24 * 5),
  },
]

/** 主题串：4 条挂在番剧 1，1 条挂在番剧 3；番剧 2 一条，用来验证"按最新回复排序"。 */
export const MOCK_ANIME_THREADS: MockAnimeThread[] = [
  {
    id: 9101,
    animeId: 1,
    title: '第 4 集同步看安排（今晚 8 点）',
    content:
      '今晚 8 点一起补《葬送的芙莉莲》第 4 集，房间码 MOON04。\n这一集的战斗分镜很关键，建议戴耳机。',
    author: AUTHOR_MOONCAT,
    likeCount: 18,
    commentCount: 2,
    createdAt: minutesAgo(60 * 5),
    lastReplyAt: minutesAgo(8),
    comments: [
      { id: 91001, author: AUTHOR_PROJECTIONIST, content: '准时到，耳机已就位。', createdAt: minutesAgo(9) },
      { id: 91002, author: AUTHOR_AZI, content: '带我一个！', createdAt: minutesAgo(8) },
    ],
  },
  {
    id: 9102,
    animeId: 1,
    title: '芙莉莲的叙事节奏值得细看',
    content: '一集一个小故事，时间跨度却拉得很长。\n这种"把寿命差写进日常"的做法，比直接讲生死更狠。',
    author: AUTHOR_AZI,
    likeCount: 26,
    commentCount: 1,
    createdAt: minutesAgo(60 * 26),
    lastReplyAt: minutesAgo(60),
    comments: [
      { id: 91003, author: AUTHOR_MOONCAT, content: '同意，尤其是回忆插叙那几段。', createdAt: minutesAgo(60) },
    ],
  },
  {
    id: 9103,
    animeId: 2,
    title: '吉卜力连播片单征集',
    content: '想排一个周末连播：千与千寻 → 哈尔的移动城堡 → 起风了。\n还有想加进来的吗？',
    author: AUTHOR_NIGHTOWL,
    likeCount: 9,
    commentCount: 0,
    createdAt: minutesAgo(60 * 3),
    lastReplyAt: minutesAgo(60 * 3),
    comments: [],
  },
  {
    id: 9104,
    animeId: 1,
    title: '关于「魔族」设定的一个疑问',
    content: '魔族对"恶意"的感知是本能还是学习来的？\n动画里给的线索有点暧昧，想听听大家的理解。',
    author: AUTHOR_MIAO,
    likeCount: 14,
    commentCount: 0,
    createdAt: minutesAgo(60 * 2),
    lastReplyAt: minutesAgo(60 * 2),
    comments: [],
  },
  {
    id: 9105,
    animeId: 3,
    title: 'TV 版与剧场版的正确观看顺序',
    content: 'TV 1-24 → 真心为你 → 新剧场版四部，这个顺序对吗？\n第一次看的人容易被结局劝退，求个不剧透的引导。',
    author: AUTHOR_PROJECTIONIST,
    likeCount: 31,
    commentCount: 1,
    createdAt: minutesAgo(60 * 30),
    lastReplyAt: minutesAgo(60 * 12),
    comments: [
      {
        id: 91004,
        author: AUTHOR_NIGHTOWL,
        content: '顺序没问题，TV 版先看 1-24 再补剧场版，观感最完整。',
        createdAt: minutesAgo(60 * 12),
      },
    ],
  },
]
