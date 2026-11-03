<script setup lang="ts">
/**
 * 头像设置块（`/me?tab=settings`，票 #74；裁剪器是票 #75）。
 *
 * ## 本票做了什么、没做什么
 *
 * 票 #74 的验收只要求「**入口占位**」：这里已经能显示当前头像、能判断这个部署
 * 有没有上传能力，并把"还没做"的部分**明说**出来。真正的选图 → 框选方形 → 512px
 * 导出 → 人机验证 → 频控那条链在票 #75。
 *
 * 所以按钮现在是 `disabled` 的，旁边写清原因。这不体面，但比"点了没反应"好：
 * 一个能点、点完什么都不发生的按钮，用户会以为是网络问题，然后反复点。
 *
 * ## 为什么单独一个组件而不是塞进 MeSettingsBlock
 *
 * 票 #75 会把这里长成一个 canvas 裁剪子窗体（与 `CaptchaDialog` 同族的窗体语言）
 * 加两次往返编排。塞进 MeSettingsBlock 的话，那个文件已经 700 行了，而裁剪器
 * 自带一整套状态机（原图 / 缩放 / 拖动 / 导出 / 重试）。**先把边界划好**，
 * #75 只需要在这个文件里长大，不用回头拆别人。
 *
 * ## 三态回落在这里的体现
 *
 * 预览用的就是 `UserAvatar`：`avatarUrl` 为空 / 图片挂了 / 挂了之后不再重试，
 * 三种情况它自己消化，本组件不需要判断"有没有头像"。
 */
import { computed, onMounted, ref } from 'vue'
import Button from '../../ui/Button.vue'
import UserAvatar from '../../ui/UserAvatar.vue'
import { probeAvatarUpload } from '../../../api/avatar'
import { useAuthStore } from '../../../stores/auth'

const auth = useAuthStore()

/** 这个部署有没有上传能力：`null` = 还在探。 */
const uploadAvailable = ref<boolean | null>(null)

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
        <p class="dim small">
          图片加载不出来时会自动退回字母头像，不会出现破图。
        </p>
      </div>
    </div>

    <!-- 未启用对象存储：这是**能力未上线**，不是故障（与整域 404 同一口径）。 -->
    <p v-if="uploadAvailable === false" class="capability" role="status" data-testid="avatar-unavailable">
      本部署没有启用文件存储，因此暂时不能上传头像。字母头像照常显示，不影响其它功能。
    </p>

    <div v-else class="actions">
      <Button type="button" :disabled="true" data-testid="avatar-change">
        更换头像
      </Button>
      <span class="dim small" data-testid="avatar-coming">
        裁剪与上传正在开发中（票 #75），这一栏先占位。
      </span>
    </div>
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
</style>
