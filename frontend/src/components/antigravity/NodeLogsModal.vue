<script setup lang="ts">
import { ref, watch } from 'vue'

import { toMessage } from '@/api/client'
import { fetchAntigravityNodeLogs } from '@/api/endpoints'
import type { ContainerNode } from '@/api/types'
import Button from '@/components/ui/Button.vue'
import CodeBlock from '@/components/ui/CodeBlock.vue'
import Icon from '@/components/ui/Icon.vue'
import Modal from '@/components/ui/Modal.vue'
import SegmentedTabs from '@/components/ui/SegmentedTabs.vue'

const props = defineProps<{
  open: boolean
  node: ContainerNode | null
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
}>()

const tail = ref('200')
const tailTabs = [
  { label: '最近 100 行', value: '100' },
  { label: '最近 200 行', value: '200' },
  { label: '最近 500 行', value: '500' },
]

const logs = ref('')
const loading = ref(false)
const error = ref('')

async function loadLogs(): Promise<void> {
  if (!props.node) return
  loading.value = true
  error.value = ''
  try {
    const res = await fetchAntigravityNodeLogs(props.node.id, Number(tail.value) || 200)
    logs.value = res.logs || ''
  } catch (err) {
    error.value = toMessage(err, '读取容器日志失败')
    logs.value = ''
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.open, props.node?.id, tail.value] as const,
  ([isOpen]) => {
    if (isOpen && props.node) {
      void loadLogs()
    }
  },
  { immediate: true },
)
</script>

<template>
  <Modal
    :open="open"
    :title="`容器实时日志 · ${node?.name ?? ''}`"
    :subtitle="node?.container_name ? `容器名：${node.container_name}` : '宿主机 Docker 标准输出与错误流'"
    size="xl"
    @update:open="emit('update:open', $event)"
  >
    <div class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <SegmentedTabs v-model="tail" :items="tailTabs" size="sm" />
        <Button variant="secondary" size="sm" :loading="loading" @click="loadLogs">
          <template #leading><Icon name="refresh" :size="13" /></template>
          刷新日志
        </Button>
      </div>

      <p
        v-if="error"
        class="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-2xs text-danger"
      >
        <Icon name="alertCircle" :size="14" class="mt-px shrink-0" />
        <span>{{ error }}</span>
      </p>

      <CodeBlock
        :code="logs"
        :title="node?.container_name || 'docker logs'"
        language="log"
        max-height="26rem"
        :empty-text="loading ? '正在拉取容器日志…' : '当前容器暂无日志输出'"
      />
    </div>

    <template #footer>
      <Button variant="ghost" size="md" @click="emit('update:open', false)">关闭</Button>
    </template>
  </Modal>
</template>
