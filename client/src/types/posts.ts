/** 帖子数据模型（纯文本插值，不做 Markdown/富文本）。 */
export interface Post {
  id: number
  title: string
  /** 纯文本正文，不包含任何 HTML（前端用 `{{ }}` + `white-space: pre-wrap` 渲染）。 */
  content: string
  authorId: number
  author: { id: number; displayName: string; avatarChar: string; avatarColor: string }
  /** 结构化图片列表（前端各自 `<img>` 渲染，绝不拼 HTML）。 */
  images: string[]
  likeCount: number
  commentCount: number
  liked: boolean
  createdAt: string
}

/** 评论（扁平，无楼中楼/引用）。 */
export interface Comment {
  id: number
  postId: number
  authorId: number
  author: { id: number; displayName: string; avatarChar: string; avatarColor: string }
  content: string
  createdAt: string
}
