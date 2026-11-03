/**
 * 像素头像的"失败记忆"（票 #74 / 决议 #54 §4）。
 *
 * ## 为什么需要它
 *
 * 像素头像是**可选覆盖层**：`users.avatar_url` 有值就盖在字母头上，没有就回落字母头。
 * 三种"没有"必须归一成同一件事：
 *
 *   1. `avatarUrl` 为空 —— 从未设置、被管理员重置、或账号已注销（服务端置空）；
 *   2. 图片加载失败（对象被删、网络抖动、CSP 拦截）；
 *   3. 加载失败**之后**，本次会话内不再重试这个 URL。
 *
 * 第 3 条是这里存在的理由：列表页有几十个头像，其中一个 404 时浏览器会
 * **每次重渲染都重试** —— 症状是列表里那一格反复闪一下破图。
 *
 * ## 为什么是模块级 + reactive
 *
 * - 模块级：同一个用户的头像同时出现在顶栏、列表、详情里，失败一次就该全局记住；
 *   组件级的话同一张坏图会被每个实例各试一次。
 * - `reactive`：`has()` 在 computed 里被读取，普通 `Set` 不会触发重渲染，
 *   结果是"失败后永远空白"而不是回落字母头。
 *
 * 刷新页面会清空（新的会话，值得再试一次 —— 对象可能已经被补传回来了）。
 */
import { reactive } from 'vue'

/** 本次会话内已经加载失败的头像 URL。 */
const failedAvatarUrls = reactive(new Set<string>())

/**
 * 归一出一个"该显示什么"：空串 = 不显示像素头像（回落字母头）。
 *
 * 调用方**不需要**自己判断有没有头像、是不是失败过 —— 那是本文件的事。
 */
export function avatarImageSrc(url: string | null | undefined): string {
  const value = (url ?? '').trim()
  if (value === '') return ''
  if (failedAvatarUrls.has(value)) return ''
  return value
}

/**
 * 记下这个 URL 加载失败，本次会话内不再重试。
 *
 * 这里**不**做任何提示：头像是装饰，加载失败的正确表达就是"没有像素头像"
 * —— 弹一句"头像加载失败"对用户没有任何可操作性。
 */
export function markAvatarImageFailed(url: string | null | undefined): void {
  const value = (url ?? '').trim()
  if (value !== '') failedAvatarUrls.add(value)
}

/** 这个 URL 是否已经失败过（测试与调试用）。 */
export function isAvatarImageFailed(url: string | null | undefined): boolean {
  const value = (url ?? '').trim()
  return value !== '' && failedAvatarUrls.has(value)
}
