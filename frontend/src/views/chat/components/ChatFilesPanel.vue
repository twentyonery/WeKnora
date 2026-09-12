<template>
  <div class="chat-files-panel">
    <!-- Hidden file input: the single entry point for browser uploads. -->
    <input
      ref="uploadInput"
      type="file"
      class="files-upload-input"
      :aria-label="$t('chat.sandbox.filesUpload')"
      @change="onUploadPicked"
    />

    <!-- Breadcrumb navigation; every crumb jumps the listing back to that dir. -->
    <nav class="files-breadcrumb" :aria-label="$t('chat.sandbox.filesBreadcrumb')">
      <template v-for="(crumb, i) in crumbs" :key="crumb.path">
        <span v-if="i > 0" class="files-crumb-sep">/</span>
        <button
          type="button"
          class="files-crumb"
          :class="{ 'is-current': i === crumbs.length - 1 }"
          @click="openDir(crumb.path)"
        >
          {{ crumb.name }}
        </button>
      </template>
      <span class="files-toolbar">
        <t-button
          variant="text"
          shape="square"
          size="small"
          :title="$t('chat.sandbox.filesRefresh')"
          :aria-label="$t('chat.sandbox.filesRefresh')"
          @click="refresh"
        >
          <template #icon><t-icon name="refresh" size="16px" /></template>
        </t-button>
        <t-button
          variant="text"
          shape="square"
          size="small"
          :title="$t('chat.sandbox.filesNewDir')"
          :aria-label="$t('chat.sandbox.filesNewDir')"
          @click="beginMkdir"
        >
          <template #icon><t-icon name="folder-add" size="16px" /></template>
        </t-button>
        <t-button
          variant="text"
          shape="square"
          size="small"
          :title="$t('chat.sandbox.filesUpload')"
          :aria-label="$t('chat.sandbox.filesUpload')"
          @click="pickUpload"
        >
          <template #icon><t-icon name="upload" size="16px" /></template>
        </t-button>
      </span>
    </nav>

    <!-- Inline rename row replaces the renamed entry in place. -->
    <div v-if="renamingPath" class="files-inline">
      <t-input
        v-model="renameTarget"
        size="small"
        :label="t('chat.sandbox.filesRename')"
        @enter="confirmRename"
      />
      <t-button size="small" theme="primary" @click="confirmRename">{{ $t('chat.sandbox.filesSave') }}</t-button>
      <t-button size="small" variant="outline" @click="renamingPath = ''">{{ $t('chat.sandbox.filesCancel') }}</t-button>
    </div>
    <!-- Inline mkdir row. -->
    <div v-else-if="mkdirActive" class="files-inline">
      <t-input
        v-model="mkdirName"
        size="small"
        :placeholder="t('chat.sandbox.filesNewDirName')"
        @enter="confirmMkdir"
      />
      <t-button size="small" theme="primary" @click="confirmMkdir">{{ $t('chat.sandbox.filesSave') }}</t-button>
      <t-button size="small" variant="outline" @click="mkdirActive = false">{{ $t('chat.sandbox.filesCancel') }}</t-button>
    </div>

    <div v-if="loadError" class="files-state">
      <t-icon name="error-circle" size="28px" />
      <span>{{ loadError }}</span>
      <t-button v-if="loadErrorRetryable" size="small" variant="outline" @click="refresh">
        {{ $t('chat.sandbox.filesRetry') }}
      </t-button>
    </div>
    <div v-else-if="loading" class="files-state"><t-loading size="small" /></div>
    <div v-else-if="!entries.length" class="files-state">
      <t-icon name="folder-open" size="32px" />
      <span>{{ $t('chat.sandbox.filesEmpty') }}</span>
    </div>
    <ul v-else class="files-list">
      <li
        v-for="entry in sortedEntries"
        :key="entry.path"
        class="file-row"
        :class="{ 'is-dir': isDir(entry) }"
        @click="onRowClick(entry)"
      >
        <button type="button" class="file-open">
          <t-icon :name="isDir(entry) ? 'folder' : fileIcon(entry.name)" size="20px" />
          <span class="file-body">
            <span class="file-name" :title="entry.name">{{ entry.name }}</span>
            <span v-if="!isDir(entry)" class="file-meta">
              <span>{{ formatSize(entry.size) }}</span>
              <span class="file-meta-sep">·</span>
              <span>{{ formatTime(entry.mod_time_unix) }}</span>
            </span>
          </span>
        </button>
        <span class="file-actions" @click.stop>
          <t-button
            v-if="!isDir(entry)"
            variant="text"
            shape="square"
            size="small"
            :title="$t('agent.artifactDrawer.download')"
            :aria-label="$t('agent.artifactDrawer.download')"
            :loading="busyKey === `dl:${entry.path}`"
            @click="handleDownload(entry)"
          >
            <template #icon><t-icon name="download" size="16px" /></template>
          </t-button>
          <t-button
            variant="text"
            shape="square"
            size="small"
            :title="$t('chat.sandbox.filesRename')"
            :aria-label="$t('chat.sandbox.filesRename')"
            @click="beginRename(entry)"
          >
            <template #icon><t-icon name="edit" size="16px" /></template>
          </t-button>
          <t-popconfirm
            :content="t('chat.sandbox.filesDeleteConfirm', { name: entry.name })"
            @confirm="handleDelete(entry)"
          >
            <t-button
              variant="text"
              shape="square"
              size="small"
              :title="$t('chat.sandbox.filesDelete')"
              :aria-label="$t('chat.sandbox.filesDelete')"
              :loading="busyKey === `rm:${entry.path}`"
            >
              <template #icon><t-icon name="delete" size="16px" /></template>
            </t-button>
          </t-popconfirm>
        </span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
// File manager over the session's sandbox workspace. The server owns path
// safety (lexical jail + read-only input tree); this component only renders
// what the listing returns and sends sandbox-absolute paths back verbatim.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  deleteSandboxPath,
  downloadSandboxFile,
  listSandboxFiles,
  makeSandboxDir,
  renameSandboxPath,
  uploadSandboxFile,
  type SandboxFileEntry,
} from '@/api/chat'

const props = defineProps<{
  sessionId: string
  active?: boolean
}>()

const { t } = useI18n()

const WORKSPACE_ROOT = '/workspace'
const UNBOUND_MESSAGE_KEY = 'chat.sandbox.filesNotStarted'

const entries = ref<SandboxFileEntry[]>([])
const cwd = ref(WORKSPACE_ROOT)
const loading = ref(false)
const loadError = ref('')
const loadErrorRetryable = ref(false)
const busyKey = ref('')
const renamingPath = ref('')
const renameTarget = ref('')
const mkdirActive = ref(false)
const mkdirName = ref('')
const uploadInput = ref<HTMLInputElement | null>(null)

const isDir = (entry: SandboxFileEntry) => entry.type === 'dir'

const sortedEntries = computed(() => {
  const list = [...entries.value]
  list.sort((a, b) => {
    const dirDelta = (isDir(b) ? 1 : 0) - (isDir(a) ? 1 : 0)
    if (dirDelta !== 0) return dirDelta
    return a.name.localeCompare(b.name)
  })
  return list
})

const crumbs = computed(() => {
  const list = [{ name: 'workspace', path: WORKSPACE_ROOT }]
  let acc = WORKSPACE_ROOT
  const rest = cwd.value.slice(WORKSPACE_ROOT.length).replace(/^\//, '')
  for (const part of rest.split('/').filter(Boolean)) {
    acc = `${acc}/${part}`
    list.push({ name: part, path: acc })
  }
  return list
})

async function loadDir(dir: string) {
  if (!props.sessionId) return
  loading.value = true
  loadError.value = ''
  loadErrorRetryable.value = false
  try {
    const res = await listSandboxFiles(props.sessionId, dir)
    const data = (res as any)?.data
    entries.value = Array.isArray(data) ? data : []
    cwd.value = dir
  } catch (err: any) {
    entries.value = []
    const status = err?.response?.status ?? err?.$httpStatus
    if (status === 404) {
      loadError.value = t(UNBOUND_MESSAGE_KEY)
      loadErrorRetryable.value = false
    } else if (status === 409) {
      loadError.value = t('chat.sandbox.filesUnsupported')
      loadErrorRetryable.value = false
    } else {
      loadError.value = t('chat.sandbox.filesLoadFailed')
      loadErrorRetryable.value = true
    }
  } finally {
    loading.value = false
  }
}

function refresh() {
  void loadDir(cwd.value)
}

function openDir(dir: string) {
  if (dir === cwd.value) return
  void loadDir(dir)
}

function onRowClick(entry: SandboxFileEntry) {
  if (isDir(entry)) openDir(entry.path)
}

function withBusy(key: string, fn: () => Promise<unknown>) {
  busyKey.value = key
  return fn()
    .then(() => true)
    .catch((err) => {
      console.error('[ChatFilesPanel] operation failed:', err)
      MessagePlugin.error(t('chat.sandbox.filesOpFailed'))
      return false
    })
    .finally(() => {
      busyKey.value = ''
    })
}

// Download needs the blob itself, so it cannot reuse withBusy's boolean.
async function handleDownload(entry: SandboxFileEntry) {
  if (!props.sessionId) return
  busyKey.value = `dl:${entry.path}`
  try {
    const blob = await downloadSandboxFile(props.sessionId, entry.path)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = entry.name
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (err) {
    console.error('[ChatFilesPanel] download failed:', err)
    MessagePlugin.error(t('chat.sandbox.filesOpFailed'))
  } finally {
    busyKey.value = ''
  }
}

async function handleDelete(entry: SandboxFileEntry) {
  const ok = await withBusy(`rm:${entry.path}`, () => deleteSandboxPath(props.sessionId, entry.path))
  if (ok) {
    MessagePlugin.success(t('chat.sandbox.filesDeleted'))
    refresh()
  }
}

function beginRename(entry: SandboxFileEntry) {
  renamingPath.value = entry.path
  renameTarget.value = entry.name
}

async function confirmRename() {
  const from = renamingPath.value
  const name = renameTarget.value.trim()
  if (!from || !name) return
  const to = from.slice(0, from.lastIndexOf('/') + 1) + name
  if (to === from) {
    renamingPath.value = ''
    return
  }
  const ok = await withBusy(`mv:${from}`, () => renameSandboxPath(props.sessionId, from, to))
  renamingPath.value = ''
  if (ok) refresh()
}

function beginMkdir() {
  mkdirActive.value = true
  mkdirName.value = ''
}

async function confirmMkdir() {
  const name = mkdirName.value.trim()
  if (!name) return
  const target = `${cwd.value}/${name}`
  const ok = await withBusy('mkdir', () => makeSandboxDir(props.sessionId, target))
  mkdirActive.value = false
  if (ok) refresh()
}

function pickUpload() {
  uploadInput.value?.click()
}

async function onUploadPicked(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || !props.sessionId) return
  const target = `${cwd.value}/${file.name}`
  const ok = await withBusy(`up:${file.name}`, () => uploadSandboxFile(props.sessionId, target, file))
  if (ok) {
    MessagePlugin.success(t('chat.sandbox.filesUploaded'))
    refresh()
  }
}

function fileIcon(name: string): string {
  const ext = name.split('.').pop()?.toLocaleLowerCase() ?? ''
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp'].includes(ext)) return 'image'
  if (['mp4', 'mov', 'avi', 'mkv', 'webm'].includes(ext)) return 'video'
  if (['mp3', 'wav', 'flac', 'ogg', 'm4a'].includes(ext)) return 'sound'
  if (['zip', 'tar', 'gz', '7z', 'rar'].includes(ext)) return 'root-list'
  if (['json', 'csv', 'xlsx', 'xls'].includes(ext)) return 'file-excel'
  if (['pdf'].includes(ext)) return 'file-pdf'
  if (['ppt', 'pptx'].includes(ext)) return 'file-ppt'
  if (['doc', 'docx'].includes(ext)) return 'file-word'
  return 'file'
}

function formatSize(size: number): string {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`
  return `${(size / 1024 / 1024 / 1024).toFixed(1)} GB`
}

function formatTime(unix: number): string {
  if (!unix) return ''
  return new Date(unix * 1000).toLocaleString()
}

watch(
  () => [props.sessionId, props.active] as const,
  ([, active], prev) => {
    const prevActive = prev?.[1]
    if (props.sessionId && active && (!prevActive || entries.value.length === 0)) {
      // Re-list on first activation or when the session changed underneath.
      if (cwd.value === WORKSPACE_ROOT || !active) cwd.value = WORKSPACE_ROOT
      refresh()
    }
  },
  { immediate: true },
)
</script>

<style scoped lang="less">
.chat-files-panel {
  flex: 1;
  min-height: 0;
  width: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.files-upload-input {
  display: none;
}

.files-breadcrumb {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 2px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--td-component-stroke);
  font-size: 12px;
}

.files-crumb {
  border: 0;
  background: transparent;
  padding: 2px 4px;
  border-radius: 4px;
  color: var(--td-text-color-secondary);
  font: inherit;
  cursor: pointer;
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;

  &.is-current {
    color: var(--td-text-color-primary);
    font-weight: 600;
  }

  &:hover:not(.is-current) {
    background: var(--td-bg-color-container-hover);
  }
}

.files-crumb-sep {
  color: var(--td-text-color-placeholder);
}

.files-toolbar {
  margin-left: auto;
  display: inline-flex;
  gap: 2px;
}

.files-inline {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--td-component-stroke);
}

.files-state {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 32px 16px;
  color: var(--td-text-color-placeholder);
  font-size: 13px;
  text-align: center;
}

.files-list {
  margin: 0;
  padding: 4px 4px 12px;
  list-style: none;
  overflow: auto;
  flex: 1;
  min-height: 0;
}

.file-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px;
  border-radius: 8px;

  &:hover {
    background: var(--td-bg-color-container-hover);
  }
}

.file-open {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;

  &:focus-visible {
    outline: 2px solid var(--td-text-color-secondary);
    outline-offset: 4px;
  }
}

.file-body {
  flex: 1;
  min-width: 0;
}

.file-name {
  display: block;
  font-size: 13px;
  font-weight: 500;
  color: var(--td-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.file-meta {
  margin-top: 2px;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
  display: flex;
  align-items: center;
  gap: 4px;
}

.file-meta-sep {
  opacity: 0.6;
}

.file-actions {
  flex-shrink: 0;
  display: inline-flex;
  gap: 2px;
  opacity: 0;
  transition: opacity 0.15s ease;

  .file-row:hover &,
  .file-row:focus-within & {
    opacity: 1;
  }
}
</style>
