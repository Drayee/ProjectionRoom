<script setup lang="ts">
/**
 * AvatarImage —— 像素头像的覆盖层（票 #74 / 决议 #54 §4）。
 *
 * ## 它解决的问题
 *
 * 站内有三处"大头像"：`UserAvatar`（四档小圆）、`/me` 概览的 56px 圆、
 * `/user/:id` 的 76px 圆（窄屏 58px）。后两处各自带响应式尺寸，套不进
 * `UserAvatar` 的四档，但它们**需要完全一样的覆盖层行为**：
 *
 *   1. 有 URL 才渲染 `<img>`；
 *   2. `object-fit: cover` 兜住"服务端只验不重编码"留下的非方图；
 *   3. `onerror` 时记住这个 URL，本次会话内不再重试（见 `utils/avatarImage.ts`）；
 *   4. 图片是**装饰**（`alt=""` + `aria-hidden`），名字由外层给。
 *
 * 抄三份的话，"失败后不再重试"这条纪律迟早会有一处漏掉 —— 症状是列表里
 * 那一格反复闪一下破图，而它不会报错、也不会被任何断言抓到。
 *
 * ## 用法
 *
 * 父元素必须是 `position: relative` 且 `overflow: hidden` 的圆形块，
 * 并且**先渲染字母头**：本组件是绝对定位盖上去的，顺序反过来会在解码前闪一下。
 *
 * 归 `ui/`：纯展示，不读 store、不发请求。
 */
import { computed } from 'vue'
import { avatarImageSrc, markAvatarImageFailed } from '../../utils/avatarImage'

const props = withDefaults(
  defineProps<{
    /** 像素头像地址；空 / 空串 / 之前失败过 → 什么都不渲染（回落字母头）。 */
    url?: string | null
  }>(),
  { url: '' },
)

/** 该显示什么：三种"没有"（没给 / 空串 / 失败过）在这里归一成一件事。 */
const src = computed(() => avatarImageSrc(props.url))

function onError(): void {
  markAvatarImageFailed(props.url)
}
</script>

<template>
  <img
    v-if="src"
    class="avatar-pic"
    :src="src"
    alt=""
    aria-hidden="true"
    decoding="async"
    @error="onError"
  />
</template>

<style scoped>
/**
 * `object-fit: cover` 是决议 #54 §1 的**执行点**：服务端只验不重编码
 * （go.mod 零图像依赖），所以它不保证上传的真是正方形。裁剪器会尽力，
 * 而显示层这一条兜住"万一不是"—— 不 cover 就会把 4:3 的照片压成扁的。
 *
 * 宽高用 100% 而不是固定像素：尺寸由父级的档位/媒体查询决定，这里跟着走。
 * 圆角也不在这里写 —— 父级 `overflow: hidden` 已经有，重复会出现 1px 锯齿。
 */
.avatar-pic {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
</style>
