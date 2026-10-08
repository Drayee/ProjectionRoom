<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import SegmentUpload from './SegmentUpload.vue'
import { useAuthStore } from '../stores/auth'
import { useRoomStore } from '../stores/room'
import { useRoomMetaStore } from '../stores/roomMeta'

const store = useRoomStore()
const auth = useAuthStore()
const meta = useRoomMetaStore()

/** 房间信息（标题 / 公开到列表）的编辑态：只有房主看得到这些控件。 */
const metaBusy = computed(() => meta.saving)
/** 非房主（服务端 404 的裁决）或本机不是主播时整块不渲染。 */
const showMeta = computed(() => store.isHost && auth.isLoggedIn && meta.roomId !== '' && !meta.notOwner)
/**
 * 标题输入框的本地值。
 *
 * 为什么不直接 v-model 到 store 里的 title：那会把"服务端回显的终态"与"用户正在敲的字"
 * 混成一个字段 —— 用户在输入途中每敲一个字都会写进那个字段，读回/保存失败后也没法回退到
 * 服务端的原值。输入是**局部**状态，读回与保存成功才由 store 同步回显（服务端会清洗标题，
 * 乐观写下去的值与真正生效的值可能不同）。
 */
const titleDraft = ref('')
/** 超过服务端上限时的前端提示（**以服务端为准**：它仍是唯一会拒绝的一方）。 */
const tooLong = computed(() => [...titleDraft.value].length > meta.titleLimit)

/**
 * "用户正在编辑标题"的标记。
 *
 * 读回与保存的回显都是异步的：如果用户手快先在空框里敲了字，迟到的回读不能把他敲进去的
 * 内容冲掉。所以只在"用户没动过输入框"时才把服务端值同步进草稿。
 */
let titleDirty = false

// 房间码到位后绑定一次，并**读回**服务端已经保存的标题与公开性（`GET /api/rooms/:id/meta`，
// 仅房主）。不读的话"刷新后输入框是空的、开关是关的"，而下一次保存会把空标题写回去。
watch(
  () => store.roomId,
  (roomId) => {
    meta.bind(roomId)
    titleDraft.value = ''
    titleDirty = false
    if (roomId !== '' && store.isHost && auth.isLoggedIn) {
      void meta.load(auth.authedFetch)
    }
  },
  { immediate: true },
)

// 服务端回显终态后同步到输入框（刚读回、保存成功、或读回后又保存）。
watch(
  () => meta.title,
  (value) => {
    if (!titleDirty) titleDraft.value = value
  },
  { immediate: true },
)

function onTitleInput() {
  titleDirty = true
}

/**
 * 保存标题。
 *
 * **允许空串**（服务端 schema 里 title 的默认值就是空串）：读回闭环之后，空输入框不再有
 * "没读到"的歧义 —— 界面知道服务端现值是什么，所以清空标题是一个明确动作，
 * 结果是列表里显示「未命名房间」。
 */
function saveTitle() {
  void meta.save(auth.authedFetch, { title: titleDraft.value }).then(() => {
    // 保存之后 store 里的 title 是服务端清洗过的终态：交还控制权让它同步进输入框。
    titleDirty = false
  })
}

/** 开关切换前的上一个值：保存失败时用它把开关拨回去。 */
let publicBefore = false

function onPublicToggle(event: Event) {
  const next = (event.target as HTMLInputElement).checked
  // 开关本身立刻动（用户的这一下必须有反馈），失败再回滚 —— 这是可逆的局部状态，
  // 与"乐观地当成保存成功"是两件事：按钮的 aria-busy 与错误文案都不会骗人。
  publicBefore = meta.isPublic
  meta.isPublic = next
  void meta.save(auth.authedFetch, { isPublic: next }).then((ok) => {
    if (!ok) meta.isPublic = publicBefore
  })
}

const seekTarget = ref('')
const dirInput = ref<HTMLInputElement | null>(null)
/** 服务端切片面板默认收起：不选它就不占地方，也不影响原有的"选择分片目录"流程。 */
const segmentOpen = ref(false)
/** 进行中态：按钮必须自己说清楚"正在干什么"，别让用户以为点不动。 */
const seeking = ref(false)
const starting = ref(false)

const canControl = computed(() => store.isHost && store.joined)

const capacityText = computed(() => {
  const cap = store.capacity
  if (!cap) return '容量未知'
  const mbps = (cap.streamBps / 1_000_000).toFixed(2)
  if (cap.mode === 'pending') {
    return `码率 ${mbps} Mbps · 成员上限 ${cap.maxMembers} 人 · 等待实测上行`
  }
  const modeText = cap.mode === 'chain' ? '单链分发' : '扇出'
  return `码率 ${mbps} Mbps · ${modeText} K0=${cap.hostChildSlots} · 上限 ${cap.maxMembers} 人`
})

const mediaText = computed(() => {
  const index = store.mediaIndex
  if (!index) return ''
  return `${index.mimeType} · ${index.segments.length} 段 · ${(index.totalBytes / 1024 / 1024).toFixed(1)} MiB`
})

/** 主播侧的状态一句话：未选片 / 读取中 / 挂载中 / 播放中 / 已暂停。 */
const stateText = computed(() => {
  if (store.mediaError) return '开播失败'
  if (store.mediaLoading) return '正在读取分片目录…'
  if (!store.mediaIndex) return '未选片'
  if (!store.videoReady) return '正在挂载播放器…'
  return store.playback.paused ? '已暂停' : '播放中'
})

const uploadText = computed(() => {
  const bps = store.uploadCapacityBps
  if (!bps) return '未测得'
  return `${(bps / 1_000_000).toFixed(1)} Mbps`
})

function pickDirectory() {
  dirInput.value?.click()
}

async function onDirectoryChosen(event: Event) {
  const input = event.target as HTMLInputElement
  if (input.files && input.files.length > 0) {
    await store.publishMediaDirectory(input.files)
  }
  input.value = ''
}

async function seek() {
  const seconds = Number(seekTarget.value)
  if (!Number.isFinite(seconds) || seconds < 0 || seeking.value) return
  seeking.value = true
  try {
    await store.seekTo(seconds)
    seekTarget.value = ''
  } finally {
    seeking.value = false
  }
}

async function startPlay() {
  if (starting.value) return
  starting.value = true
  try {
    await store.play()
  } finally {
    starting.value = false
  }
}

function changeRate(event: Event) {
  const value = Number((event.target as HTMLSelectElement).value)
  if (Number.isFinite(value) && value > 0) store.setRate(value)
}
</script>

<template>
  <div class="host card">
    <header>
      <h2>
        <BrandIcon name="video" decorative :size="15" />
        主播控制台
      </h2>
      <span class="muted">{{ capacityText }}</span>
    </header>

    <div class="media">
      <input
        id="media-dir-input"
        ref="dirInput"
        class="hidden-input"
        type="file"
        webkitdirectory
        multiple
        @change="onDirectoryChosen"
      />      <button :disabled="!store.joined || store.mediaLoading" @click="pickDirectory">
        <BrandIcon name="upload" decorative :size="14" />
        {{ store.mediaLoading ? '正在读取分片…' : '选择分片目录' }}
      </button>
      <span v-if="mediaText" class="muted mono small">{{ mediaText }}</span>
      <span v-else class="muted small">未选片：片源要先切成分片目录（三条路见帮助）。</span>
    </div>

    <!-- 片源从哪来：一句话 + 指向 /help 的入口，不再在核心页铺开解释。 -->
    <p class="muted small hint" v-if="!store.mediaIndex">
      本项目不提供也不分发任何内容，片源要自备。
      <RouterLink :to="{ name: 'help', hash: '#source' }" class="hint-link">
        <BrandIcon name="video" decorative :size="13" />
        片源准备
      </RouterLink>
    </p>

    <p class="error-text" v-if="store.mediaError">
      <BrandIcon name="alert-triangle" label="开播失败" :size="14" />
      {{ store.mediaError }}
    </p>

    <!-- 房间信息（二期 T6）：标题 + 公开到房间列表。
         可见性判定：本机是主播 **且** 已登录（改元数据要 access token），
         并且服务端没有用 403/404 否认房主身份。控件只是"不显示"，授权始终在服务端。 -->
    <div class="room-meta" v-if="showMeta">
      <div class="field">
        <label for="room-title-input">
          <BrandIcon name="video" decorative :size="12" />
          房间标题（会显示在公开房间列表里）
        </label>
        <input
          id="room-title-input"
          v-model="titleDraft"
          :maxlength="meta.titleLimit"
          placeholder="未命名房间"
          :aria-busy="metaBusy || meta.loading"
          :disabled="meta.loading"
          data-testid="room-title-input"
          @input="onTitleInput"
        />
        <p class="meta-line">
          <span class="muted small">{{ [...titleDraft].length }} / {{ meta.titleLimit }}</span>
          <span class="warn small" v-if="tooLong">超过 {{ meta.titleLimit }} 个字符，服务端可能拒绝</span>
          <!-- 服务端确认过"这个房间现在没有标题"：和"还没读到"必须区分开，
               否则用户会以为自己的标题丢了。 -->
          <span class="muted small" v-if="meta.unnamed" data-testid="room-meta-unnamed">当前未命名房间</span>
        </p>
      </div>

      <div class="field toggle-field">
        <label class="toggle-label" for="room-public-toggle">
          <input
            id="room-public-toggle"
            type="checkbox"
            class="checkbox"
            :checked="meta.isPublic"
            :disabled="metaBusy || meta.loading"
            :aria-busy="metaBusy"
            data-testid="room-public-toggle"
            @change="onPublicToggle"
          />
          <span>
            <BrandIcon name="share" decorative :size="12" />
            公开到房间列表（任何人无需账号即可看到并进入）
          </span>
        </label>
        <!-- 开关显示的是**服务端读回来的真实值**（GET /api/rooms/:roomId/meta）。
             读不到时下面用**中性提示**说明（不是错误条）：元数据行是异步落库的，
             刚建完房就读会撞上这个窗口，保存一次即补上。 -->
        <p class="muted small toggle-hint" v-if="meta.loaded">
          这是服务端保存的当前状态；改动会立刻保存。
        </p>
        <p class="muted small toggle-hint" v-else-if="meta.readMissing" data-testid="room-meta-read-missing">
          还没读到服务端已有的设置（新房间的元数据是异步写入的，稍等一下或直接保存一次即可）。
        </p>
        <p class="warn small toggle-hint" v-else-if="meta.error" data-testid="room-meta-read-failed">
          没能读到服务端当前状态，开关显示的可能不是服务端的真实值。
        </p>
      </div>

      <div class="meta-actions">
        <button
          class="primary"
          type="button"
          :disabled="metaBusy || meta.loading"
          :aria-busy="metaBusy"
          data-testid="room-meta-save"
          @click="saveTitle"
        >
          <BrandIcon :name="metaBusy ? 'refresh' : 'check-circle'" decorative :size="14" :class="{ spinning: metaBusy }" />
          {{ metaBusy ? '保存中…' : '保存房间信息' }}
        </button>
        <span class="muted small" v-if="meta.loading" data-testid="room-meta-loading">正在读取当前房间信息…</span>
        <span class="ok small" v-else-if="meta.done" data-testid="room-meta-done">
          <BrandIcon name="check-circle" label="已保存" :size="13" />
          {{ meta.done }}
        </span>
      </div>

      <!-- 失败：**原样显示服务端文案**（400/404/503 各有各的说明，界面不重写）。
           标题留空是**合法**动作（结果是「未命名房间」），所以这里只在真的失败时出现。 -->
      <div class="error-bar meta-error" v-if="meta.error" data-testid="room-meta-error">
        <BrandIcon name="alert-triangle" label="保存失败" :size="14" />
        <span class="msg">{{ meta.error }}</span>
      </div>
    </div>

    <!-- 本机没有 ffmpeg 的兜底入口：默认收起，展开后是上传 + 服务端切片面板 + 切片工具下载。 -->
    <div class="segment-entry">
      <!-- 『交给服务器切片』是既有验收脚本（verify-segment-ui）定位这个入口的锚点，文案不能改。 -->
      <button class="segment-toggle" @click="segmentOpen = !segmentOpen">
        <BrandIcon :name="segmentOpen ? 'close' : 'upload'" decorative :size="13" />
        {{ segmentOpen ? '收起服务端切片' : '本机没有 ffmpeg？交给服务器切片 / 下载切片工具' }}
      </button>
      <SegmentUpload v-if="segmentOpen" />
    </div>

    <div class="controls">
      <button
        v-if="store.playback.paused"
        class="primary"
        :disabled="!canControl || !store.mediaIndex || starting"
        :aria-busy="starting"
        @click="startPlay"
      >
        {{ starting ? '准备中…' : '播放' }}
      </button>
      <button v-else :disabled="!canControl" @click="store.pause()">暂停</button>

      <div class="seek">
        <input
          v-model="seekTarget"
          type="number"
          min="0"
          step="1"
          placeholder="秒"
          :disabled="!canControl || !store.mediaIndex || seeking"
        />
        <button :disabled="!canControl || !store.mediaIndex || seekTarget === '' || seeking" @click="seek">
          {{ seeking ? '跳转中…' : '跳转' }}
        </button>
      </div>

      <label class="rate">
        速率
        <select :value="store.playback.rate" :disabled="!canControl" @change="changeRate">
          <option :value="0.5">0.5x</option>
          <option :value="1">1.0x</option>
          <option :value="1.25">1.25x</option>
          <option :value="1.5">1.5x</option>
          <option :value="2">2.0x</option>
        </select>
      </label>
    </div>

    <p class="muted small hint" v-if="!store.mediaIndex">
      跳转与播放要等分片目录选好之后才能用；跳转会清空缓冲并重建，观众端会跟着一起跳。
      <RouterLink :to="{ name: 'help', hash: '#playback' }" class="hint-link">
        <BrandIcon name="tips" decorative :size="13" />
        画质与延迟
      </RouterLink>
    </p>

    <div class="metrics mono small">
      <span class="badge mono" :class="{ ok: stateText === '播放中' }">{{ stateText }}</span>
      <span class="badge mono">seq {{ store.playback.seq }}</span>
      <span>P2P {{ store.peerCount }} 连接</span>
      <span>上行 {{ uploadText }}</span>
      <span>已交付 {{ store.delivered }}</span>
      <span v-if="store.timedOut">超时 {{ store.timedOut }}</span>
    </div>
  </div>
</template>

<style scoped>
.host {
  padding: 12px;
}

header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}

h2 {
  margin: 0;
  font-size: 14px;
  display: flex;
  align-items: center;
  gap: 6px;
}

button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.hint-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--accent);
  white-space: nowrap;
}

header .muted {
  font-size: 12px;
}

.hidden-input {
  display: none;
}

.media {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}

.small {
  font-size: 12px;
}

.error-text {
  margin: 0 0 10px;
  color: var(--danger);
  font-size: 12px;
  line-height: 1.6;
  display: flex;
  align-items: flex-start;
  gap: 6px;
}

.segment-entry {
  margin-bottom: 10px;
}

button.segment-toggle {
  width: 100%;
  text-align: left;
  font-size: 12px;
  color: var(--text-dim);
  padding: 6px 10px;
}

.controls {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.hint {
  margin: 8px 0 0;
  font-size: 12px;
  line-height: 1.6;
}

.seek {
  display: flex;
  gap: 6px;
  align-items: center;
}

.seek input {
  width: 92px;
}

.rate {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  color: var(--text-dim);
}

select {
  font: inherit;
  color: var(--text);
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px;
}

.metrics {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  align-items: center;
  margin-top: 10px;
  color: var(--text-dim);
}

/* ---------- 房间信息（标题 / 公开开关，二期 T6）---------- */
.room-meta {
  margin-bottom: 10px;
  padding: 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
}

.room-meta .field {
  margin-bottom: 10px;
}

.room-meta label {
  display: flex;
  align-items: center;
  gap: 5px;
}

.room-meta .meta-line {
  margin: 5px 0 0;
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}

.room-meta .warn {
  color: var(--accent-2);
}

.toggle-label {
  align-items: flex-start;
  gap: 7px;
  color: var(--text);
  font-size: 13px;
  line-height: 1.5;
}

.toggle-label > span {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
}

.toggle-hint {
  margin: 6px 0 0 24px;
  line-height: 1.5;
}

/* 复选框不要继承全局 input{width:100%}：否则它会把一行撑成整宽。 */
.checkbox {
  width: auto;
  flex: none;
  margin: 2px 0 0;
  accent-color: var(--accent);
}

.meta-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.meta-actions button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.ok {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--ok);
}

.meta-error {
  margin: 10px 0 0;
}

.spinning {
  animation: host-spin 0.9s linear infinite;
}

@keyframes host-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
