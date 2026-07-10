<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useData } from 'vitepress'

const { page, frontmatter } = useData()
const open = ref(false)
const copied = ref('')
const root = ref<HTMLElement>()
const markdownFiles = import.meta.glob('../../../**/*.md', {
  query: '?raw',
  import: 'default',
  eager: true
}) as Record<string, string>

const relativePath = computed(() => page.value.relativePath)
const markdown = computed(() => markdownFiles[`../../../${relativePath.value}`] ?? '')
const sourceUrl = computed(
  () => `https://raw.githubusercontent.com/dirien/doplane/main/website/${relativePath.value}`
)
const fileName = computed(() => relativePath.value.split('/').at(-1) ?? 'doplane.md')

async function copy(value: string, label: string) {
  await navigator.clipboard.writeText(value)
  copied.value = label
  open.value = false
  window.setTimeout(() => { copied.value = '' }, 1800)
}

function download() {
  const blob = new Blob([markdown.value], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = fileName.value
  anchor.click()
  URL.revokeObjectURL(url)
  open.value = false
}

function closeOnOutsideClick(event: MouseEvent) {
  if (root.value && !root.value.contains(event.target as Node)) open.value = false
}

onMounted(() => document.addEventListener('click', closeOnOutsideClick))
onBeforeUnmount(() => document.removeEventListener('click', closeOnOutsideClick))
</script>

<template>
  <div v-if="frontmatter.layout !== 'home'" ref="root" class="agent-actions">
    <p class="agent-actions__context">Agent-ready source</p>
    <div class="agent-actions__menu">
      <button
        class="agent-actions__trigger"
        type="button"
        :aria-expanded="open"
        aria-haspopup="menu"
        @click="open = !open"
      >
        <span class="agent-actions__markdown">M↓</span>
        {{ copied || 'Markdown for agents' }}
        <span aria-hidden="true" class="agent-actions__chevron">⌄</span>
      </button>
      <div v-if="open" class="agent-actions__dropdown" role="menu">
        <button role="menuitem" @click="copy(markdown, 'Markdown copied')">
          <strong>Copy Markdown</strong>
          <span>Paste the page into an agent context.</span>
        </button>
        <a :href="sourceUrl" target="_blank" rel="noreferrer" role="menuitem">
          <strong>View raw source</strong>
          <span>Open the canonical Markdown on GitHub.</span>
        </a>
        <button role="menuitem" @click="download">
          <strong>Download .md</strong>
          <span>Save this page as a local context file.</span>
        </button>
        <button
          role="menuitem"
          @click="copy(`Read ${sourceUrl}\nFollow its invariants and verify each stated success condition.`, 'Prompt copied')"
        >
          <strong>Copy agent prompt</strong>
          <span>Copy a short prompt with the source URL.</span>
        </button>
      </div>
    </div>
  </div>
</template>
