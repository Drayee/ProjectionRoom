<script setup lang="ts">
/**
 * 全局提示图层挂载点 + 路由出口。
 * 图层必须挂在路由之外：否则换页会重建图层，正在显示的提示被卸载。
 */
import TooltipLayer from './components/ui/TooltipLayer.vue'
</script>

<template>
  <router-view />
  <TooltipLayer />
</template>
