<script setup lang="ts">
import ToastHost from '@/components/ui/ToastHost.vue'
</script>

<template>
  <!-- 根组件承载当前路由视图与全局最高层级通知宿主 -->
  <div class="min-h-screen bg-canvas text-fg antialiased">
    <RouterView />
    <ToastHost />
  </div>
</template>
