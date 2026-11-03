<script setup lang="ts">
/**
 * 头像设置块（`/me?tab=settings`，票 #74 占位 → 票 #75 接线）。
 *
 * ## 完整链路（决议 #54 §8 的「零新端点」）
 *
 *   点「更换头像」→ `AvatarCropDialog`（选图 → 拖/缩 → 导出 512×512 blob）
 *   → `CaptchaDialog`（人机验证，页内子窗体）
 *   → `POST /api/uploads`（现成的通用上传）→ `{url}`
 *   → `PATCH /api/me {avatarUrl}`（服务端校验是本桶的合法对象地址）
 *   → `auth.applyProfileUpdate(...)`（权威档案落地）
 *
 * 两个新端点都没有：裁剪在前端，上传走通用接口，落库走既有的 `PATCH /api/me`。
 *
 * ## 为什么人机验证在"裁剪之后、上传之前"
 *
 * 验证的意义是**挡脚本批量写桶**，所以它必须在上传前；而放在裁剪前的话，用户
 * 解完题才发现自己选错了图，得重新解一次。顺序按"代价小的先做"：
 * 裁剪是本地操作（错了重选即可），验证与上传都是要过服务端的。
 *
 * ## 清除头像也走同一条验证
 *
 * 清除在服务层**不占每日额度**（降风险方向，与"注销冷静期里撤销不要密码"同一条
 * 判断），但它仍然是一次"改变别人看到的头像"的写入，并且和设置共用同一条
 * `PATCH /api/me`。让两条路走同一套编排，界面上就只有一个答案：
 * 「点这个按钮会发生什么」不需要分类讨论。
 *
 * ## 半状态怎么办（这是本组件最需要想清楚的地方）
 *
 * 两次往返分别是"传图"与"落库"，中间失败时状态是**不一致的**：
 *
 *   | 传图 | 落库 | 用户看到 | 界面说什么 |
 *   | :--- | :--- | :--- | :--- |
 *   | 失败 | 未执行 | 旧头像 | 上传失败的原文案（服务端给的最准） |
 *   | 成功 | 失败 | 旧头像 | **图已传上去但没生效**，并给出"再试一次" |
 *   | 成功 | 成功 | 新头像 | 确认提示 |
 *
 * 第二行不能笼统说"失败了"：那张图**确实**已经在桶里（URL 是有效的），
 * 重试只需要再打一次 PATCH。所以 `pendingUrl` 会被记住，重试时跳过上传 ——
 * 否则用户重试一次就多往桶里塞一张一模一样的图。
 *
 * ## 三态回落
 *
 * 预览用的就是 `UserAvatar`：`avatarUrl` 为空 / 图片挂了 / 挂了之后不再重试，
 * 三种情况它自己消化，本组件不判断"有没有头像"。
 */
import { computed, onMounted, ref } from 'vue'
import Button from '../../ui/Button.vue'
import UserAvatar from '../../ui/UserAvatar.vue'
import AvatarCropDialog from './AvatarCropDialog.vue'
import CaptchaDialog from '../auth/CaptchaDialog.vue'
import { probeAvatarUpload, uploadAvatarBlob } from '../../../api/avatar'
import { updateAvatarUrl } from '../../../api/users'
import { useAuthStore } from '../../../stores/auth'
import { toHttpError, isOffline } from '../../../api/http'

const auth = useAuthStore()

/** 这个部署有没有上传能力：`null` = 还在探。 */
const uploadAvailable = ref<boolean | null>(null)

/** 裁剪子窗体是否打开。 */
const cropOpen = ref(false)
/** 人机验证子窗体是否打开。 */
const captchaOpen = ref(false)

/** 待提交的动作：`set` 要带 blob / `clear` 直接清。 */
type PendingAction = { kind: 'set'; blob: Blob } | { kind: 'clear' } | null
const pending = ref<PendingAction>(null)
/** 已经传上去但还没落库的 URL（重试时跳过上传，避免桶里多一张一样的图）。 */
const pendingUrl = ref('')

const busy = ref(false)
const notice = ref('')
const failure = ref('')

/** 预览用的人（`UserAvatar` 只认这三个字段）。 */
const previewUser = computed(() => {
  const p = auth.profile
  return { id: p?.id ?? 0, displayName: p?.displayName || p?.username || '?', avatarUrl: p?.avatarUrl ?? '' }
})

const hasAvatar = computed(() => (auth.profile?.avatarUrl ?? '') !== '')

onMounted(async () => {
  // 探针失败一律当作"不可用"，且不弹错误：用户还没点任何东西，
  // 一条红字只会让人以为自己做错了什么。
  uploadAvailable.value = await probeAvatarUpload()
})

function resetFeedback(): void {
  notice.value = ''
  failure.value = ''
}

function askChange(): void {
  if (busy.value || uploadAvailable.value !== true) return
  resetFeedback()
  cropOpen.value = true
}

/** 裁剪器给了 blob：接着做人机验证（此时还没碰网络）。 */
function onCropConfirm(blob: Blob): void {
  cropOpen.value = false
  pending.value = { kind: 'set', blob }
  pendingUrl.value = ''
  captchaOpen.value = true
}

function onCropCancel(): void {
  cropOpen.value = false
}

/** 点「移除头像」：同样要过验证，只是待提交的动作不同。 */
function askClear(): void {
  if (busy.value || uploadAvailable.value !== true) return
  resetFeedback()
  pending.value = { kind: 'clear' }
  pendingUrl.value = ''
  captchaOpen.value = true
}

function onCaptchaPass(): void {
  captchaOpen.value = false
  void submit()
}

function onCaptchaCancel(): void {
  captchaOpen.value = false
  if (pending.value !== null) {
    // 验证被取消 = 什么都没发生。**不要**在这里报错：用户主动取消，不是失败。
    notice.value = '已取消，头像没有改动。'
  }
  pending.value = null
  pendingUrl.value = ''
}

/** 把待提交的动作送出去。两次往返拆开，便于"传上去了但没生效"的局部重试。 */
async function submit(): Promise<void> {
  const action = pending.value
  if (action === null || busy.value) return
  busy.value = true
  resetFeedback()

  if (action.kind === 'clear') {
    try {
      const profile = await updateAvatarUrl(null)
      auth.applyProfileUpdate(profile)
      notice.value = '已移除头像，现在显示字母头像。'
      pending.value = null
    } catch (err) {
      const http = toHttpError(err)
      failure.value = http.message
    } finally {
      busy.value = false
    }
    return
  }

  // 第一步：上传。已经有 pendingUrl 说明上一步成功过，跳过它。
  if (pendingUrl.value === '') {
    try {
      pendingUrl.value = await uploadAvatarBlob(action.blob)
    } catch (err) {
      // 上传失败 = 什么都没发生（桶里也没有图），文案原样透出即可。
      const http = toHttpError(err)
      failure.value = http.message
      pendingUrl.value = ''
      busy.value = false
      return
    }
  }

  // 第二步：落库。失败时 pendingUrl 保留 —— 重试只需要再打一次 PATCH。
  await savePendingUrl()
  busy.value = false
}

/** 只做第二步（落库）。`submit` 与「重试保存」共用它。 */
async function savePendingUrl(): Promise<void> {
  try {
    const profile = await updateAvatarUrl(pendingUrl.value)
    auth.applyProfileUpdate(profile)
    notice.value = '头像已更新。'
    pending.value = null
    pendingUrl.value = ''
  } catch (err) {
    // 半状态：图在桶里，但档案没改。文案必须说清这件事，并留一个只重试
    // 第二步的入口（`pendingUrl` 已被记住）。
    const http = toHttpError(err)
    failure.value = isOffline(err)
      ? '图片已经传上去了，但没能保存到账号（网络不可达），点「重试保存」再试一次。'
      : `图片已经传上去了，但保存失败：${http.message}。点「重试保存」再试一次，不需要重选图片。`
  }
}

/** 「重试保存」：pendingUrl 还在，直接打第二步。 */
async function retrySave(): Promise<void> {
  if (pendingUrl.value === '' || busy.value) return
  busy.value = true
  resetFeedback()
  try {
    await savePendingUrl()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="card" aria-labelledby="me-avatar-head">
    <h2 id="me-avatar-head" class="sect-title">头像</h2>

    <div class="avatar-row" data-testid="avatar-row">
      <UserAvatar :user="previewUser" size="lg" :interactive="false" />
      <div class="avatar-meta">
        <p class="dim small">
          {{ hasAvatar ? '当前使用你上传的图片。' : '当前是字母头像：由昵称的首字与一套固定色板生成，随时可以换掉。' }}
        </p>
        <p class="dim small">图片加载不出来时会自动退回字母头像，不会出现破图。</p>
      </div>
    </div>

    <!-- 未启用对象存储：这是**能力未上线**，不是故障（与整域 404 同一口径）。 -->
    <p v-if="uploadAvailable === false" class="capability" role="status" data-testid="avatar-unavailable">
      本部署没有启用文件存储，因此暂时不能上传头像。字母头像照常显示，不影响其它功能。
    </p>

    <div v-else class="actions">
      <Button
        type="button"
        variant="primary"
        :disabled="uploadAvailable !== true || busy"
        :loading="busy"
        data-testid="avatar-change"
        @click="askChange"
      >
        更换头像
      </Button>
      <Button
        v-if="hasAvatar"
        type="button"
        variant="ghost"
        :disabled="uploadAvailable !== true || busy"
        data-testid="avatar-remove"
        @click="askClear"
      >
        移除头像
      </Button>
      <span class="dim small">每天最多改 5 次。图片会被裁成正方形后上传，服务端不改尺寸。</span>
    </div>

    <p v-if="notice" class="notice" role="status" data-testid="avatar-notice">{{ notice }}</p>
    <p v-if="failure" class="failure" role="alert" data-testid="avatar-failure">
      {{ failure }}
      <button
        v-if="pendingUrl !== ''"
        class="retry"
        type="button"
        :disabled="busy"
        data-testid="avatar-retry-save"
        @click="retrySave"
      >
        重试保存
      </button>
    </p>

    <AvatarCropDialog v-if="cropOpen" @confirm="onCropConfirm" @cancel="onCropCancel" />
    <CaptchaDialog v-if="captchaOpen" @pass="onCaptchaPass" @cancel="onCaptchaCancel" />
  </section>
</template>

<style scoped>
.avatar-row {
  display: flex;
  align-items: center;
  gap: 14px;
}

.avatar-meta {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 12px;
}

/* 能力说明用中性描边（与 CapabilityNotice 同一套语言）：它不是错误，是"这里还没开"。 */
.capability {
  margin: 12px 0 0;
  padding: 10px 12px;
  border: 1px solid var(--line);
  background: var(--surface);
  color: var(--text-dim);
  font-size: 13px;
  line-height: 1.6;
}

.notice {
  margin: 10px 0 0;
  font-size: 13px;
  color: var(--text-dim);
  line-height: 1.6;
}

.failure {
  margin: 10px 0 0;
  font-size: 13px;
  color: var(--danger, #e46a6a);
  line-height: 1.6;
}

.retry {
  margin-left: 8px;
  border: 0;
  background: none;
  padding: 0;
  color: var(--accent);
  font-size: 13px;
  text-decoration: underline;
  cursor: pointer;
}

.retry:disabled {
  color: var(--dim);
  cursor: default;
}
</style>
