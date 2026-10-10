<script setup lang="ts">
/**
 * 应用外壳。
 *
 * 只做两件事：
 *   1. 渲染当前路由；
 *   2. 挂**全局唯一的**提示图层（`v-tip` 的落点）—— 它必须挂在路由之外，
 *      否则每次换页都会重建图层，正在显示的提示会被卸载掉（鼠标还停在按钮上时闪一下）。
 */
import TooltipLayer from './components/TooltipLayer.vue'
</script>

<template>
  <router-view />
  <TooltipLayer />
</template>
