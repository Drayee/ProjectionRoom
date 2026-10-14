<script setup lang="ts">
/**
 * 唯一 Markdown 消毒出口（票 #15 渲染门禁重谈）。
 *
 * 全项目 HTML 注入只放行本组件这一处（验收 grep 命中 ≤1 且处在本文件）；
 * DOM 属性直写为 0。流程：`marked` 解析 Markdown → 字符串 →
 * `DOMPurify.sanitize()` 消毒 → 交给 Vue 渲染。
 *
 * 放 `ui/` 的理由：不 import `api/`、不 import `composables/`、不发请求；
 * 只做「文本 → 安全 HTML」的纯展示转换。
 *
 * `breaks: true`：mock 正文用单 `\n` 分段（api/posts.ts 是逻辑层不可改），
 * 开启 GFM 换行让单换行渲染为 <br>，与旧 `white-space: pre-wrap` 观感一致。
 */
import { computed } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

const props = withDefaults(
  defineProps<{
    /** Markdown 源文（纯文本字符串，绝不直接包含 HTML）。 */
    source: string
    /** 语义标题（可访问性：正文区标题，供屏幕阅读器识别结构）。 */
    label?: string
  }>(),
  { source: '', label: '' },
)

const html = computed(() => {
  if (!props.source) return ''
  const raw = marked.parse(props.source, { gfm: true, breaks: true }) as string
  return DOMPurify.sanitize(raw)
})
</script>

<template>
  <div
    class="markdown"
    :role="label ? 'region' : undefined"
    :aria-label="label || undefined"
    v-html="html"
  />
</template>

<style scoped>
/* 正文排版（B 视觉）：直角、深底、强调紫。注入节点无 scoped 属性，需 :deep()。 */
.markdown {
  color: var(--text);
  font-size: 15px;
  line-height: 1.9;
  word-break: break-word;
}

.markdown :deep(p) {
  margin: 0 0 12px;
}

.markdown :deep(h1),
.markdown :deep(h2),
.markdown :deep(h3),
.markdown :deep(h4) {
  margin: 22px 0 8px;
  font-family: var(--font-glitch);
  line-height: 1.3;
  letter-spacing: 0.04em;
  color: var(--text);
}

.markdown :deep(h1) {
  font-size: 24px;
}

.markdown :deep(h2) {
  font-size: 20px;
}

.markdown :deep(h3) {
  font-size: 17px;
}

.markdown :deep(h4) {
  font-size: 15px;
}

.markdown :deep(a) {
  color: var(--accent);
  text-decoration: underline;
  text-underline-offset: 3px;
}

.markdown :deep(strong) {
  font-weight: 700;
  color: var(--text);
}

.markdown :deep(em) {
  font-style: italic;
}

.markdown :deep(code) {
  font-family: var(--font-mono);
  font-size: 0.9em;
  padding: 1px 5px;
  background: rgba(0, 0, 0, 0.4);
  border: 1px solid var(--line);
  border-radius: var(--radius);
}

.markdown :deep(pre) {
  margin: 0 0 12px;
  padding: 12px 14px;
  background: rgba(0, 0, 0, 0.45);
  border: 1px solid var(--line);
  overflow-x: auto;
}

.markdown :deep(pre code) {
  padding: 0;
  background: none;
  border: 0;
}

.markdown :deep(blockquote) {
  margin: 0 0 12px;
  padding: 4px 0 4px 14px;
  border-left: 3px solid var(--accent);
  color: var(--dim);
}

.markdown :deep(blockquote p) {
  margin: 0;
}

.markdown :deep(ul),
.markdown :deep(ol) {
  margin: 0 0 12px;
  padding-left: 24px;
}

.markdown :deep(li) {
  margin: 2px 0;
}

.markdown :deep(hr) {
  height: 1px;
  border: 0;
  margin: 18px 0;
  background: var(--line);
}

.markdown :deep(img) {
  max-width: 100%;
  display: block;
  margin: 12px 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
}

.markdown :deep(table) {
  border-collapse: collapse;
  margin: 0 0 12px;
  width: 100%;
}

.markdown :deep(th),
.markdown :deep(td) {
  border: 1px solid var(--line);
  padding: 6px 10px;
  text-align: left;
}

.markdown :deep(th) {
  background: rgba(180, 108, 255, 0.12);
}
</style>
