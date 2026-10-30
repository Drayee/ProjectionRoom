import { ref } from 'vue'

/**
 * 换页播报（票 #79）。
 *
 * 屏幕阅读器用户换页后有两件事要知道：**这是哪一页**、**焦点在哪**。
 * 只做标题（`document.title`）不够 —— 标题不一定会被念；只移焦点也不够 ——
 * 焦点落在一个没名字的容器上，读屏只会说"标题"。
 *
 * 这里提供**一个**全局 `aria-live="polite"` 区域（渲染在 App.vue 里），
 * 换页时把页面名写进去一次。
 *
 * 为什么是 polite 而不是 assertive：换页不是错误，用 assertive 会打断读屏用户
 * 正在读的内容（他们可能正在上一页的正文里）。
 *
 * 为什么"一次"：同一个文本连续写两次不会触发播报（live region 只在**内容变化**时播报），
 * 所以"重复写入相同文本"天然不会念第二遍 —— 但为了防"念了两次"，
 * 这里在两次写入之间插一个空串再写回，这是 live region 的标准做法。
 */
export const announcement = ref('')

/**
 * 播报一句页面名。
 *
 * 参数是**页面名**（不含「· 月喵」后缀）：读屏念「帖子 · 月喵」不如念「帖子」干净。
 */
export function announcePage(pageName: string): void {
  const text = (pageName ?? '').trim()
  if (text === '') return
  // 先清空再写回：保证连续两次相同的内容也会被重新播报。
  announcement.value = ''
  // 下一帧再写：与清空之间留一个微任务，live region 才能观察到"变了"。
  void Promise.resolve().then(() => {
    announcement.value = text
  })
}
