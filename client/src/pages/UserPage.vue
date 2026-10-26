<script setup lang="ts">
/**
 * 用户档案页（`/user/:id`）。wayfinder #32：从「只支持本人」扩到公开档案。
 *
 * 三种视角，读不同的数据源：
 *
 *   1. **他人**（含未登录）→ `GET /api/users/:id`（免登录）。只渲染公开白名单：
 *      用户名 / 昵称 / 头像字符 / 加入时间 / 角色标签。email、status、任何 token 信息
 *      在 `PublicProfile` 类型里**根本不存在**，模板里也没有它们的插值位置。
 *   2. **本人** → 不请求公开档案，直接读 `stores/auth.ts` 的档案（那是本人的权威
 *      显示数据，还带着 email 与验证状态）；提供昵称编辑，写 `PATCH /api/me`。
 *   3. **不存在的 id / 被隐藏的账号** → 「用户不存在」空态（见下）。
 *
 * 三条关键判断：
 *
 *  - **404 与 403 是两个故事**（#33 定稿）：`GET /api/users/:id` 对「不存在」与
 *    「被封禁」都回 **404**（区分它们等于开一条存在性探测通道），所以本页只能说
 *    「不存在或不可见」；而本人改自己被封禁的账号，`PATCH /api/me` 回 **403**
 *    （对他隐瞒"你被封了"没有意义），这里单独说明并指向管理员。
 *  - **本人视角不读公开档案**：读它只会拿到少几个字段的同一份数据，而且 mock 态的
 *    人员目录里不一定有当前真实账号的 id —— 那会让"打开自己的主页"显示「用户不存在」。
 *    本人的权威数据在 auth store 里，页面直接渲染它。
 *  - **成功提示写在所有刷新之后**（#28 的教训）：昵称保存后先落 store、再改 draft、
 *    最后写提示；`watch` 只盯 id 的**字符串**，避免"store 一更新就重跑 loader 把提示擦掉"。
 */
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import * as usersApi from '../api/users'
import type { PublicProfile } from '../api/users'
import { NETWORK_MESSAGE, toHttpError, type HttpError } from '../api/http'
import { isCapabilityUnavailable } from '../api/mode'
import { useAuthStore } from '../stores/auth'
import { avatarOf } from '../utils/avatar'

const props = defineProps<{ id: string }>()

const route = useRoute()
const auth = useAuthStore()

/**
 * 路由参数永远是字符串：`/user/12` → `'12'`。
 *
 * 只有正整数才是合法的用户 id（与服务端 `adminPathID` 同一口径）。本地先拦一次：
 * 「id 写错了」是**地址**的问题，不该跳过一次注定 400 的往返，更不该被说成
 * 「用户不存在」（那会让人以为某个人被删了）。
 */
const parsedId = computed(() => {
  const raw = String(props.id ?? '').trim()
  return /^[1-9]\d*$/.test(raw) ? Number(raw) : 0
})

/** 本人判断：只看 id。`auth.profile` 为 null（游客）时恒为 false。 */
const isSelf = computed(() => parsedId.value !== 0 && auth.profile !== null && auth.profile.id === parsedId.value)

/** 他人视角的公开档案（本人视角恒为 null —— 见文件头的第 2 条）。 */
const publicProfile = ref<PublicProfile | null>(null)

const loading = ref(false)
/** 整域未上线（真实实现撞上 NoRoute 的纯文本 404）。 */
const capabilityMissing = ref(false)
/** 用户不存在或不可见（服务端 404 `NOT_FOUND`）。 */
const notFound = ref(false)
const loadError = ref('')

// ---------- 本人：昵称编辑 ----------
const draft = ref('')
const saving = ref(false)
const saveNotice = ref('')
const saveError = ref('')
const saveFieldError = ref('')

/** 失败文案：网络不可达用本地兜底，其余原样（服务端文案就是给用户看的最终表述）。 */
function messageOf(err: HttpError): string {
  return err.offline ? NETWORK_MESSAGE : err.message
}

/** 本人视角的档案从 store 拼出来（形状与公开档案一致，供同一个头部模板用）。 */
const view = computed<(PublicProfile & { avatarChar: string; avatarColor: string }) | null>(() => {
  const base: PublicProfile | null = isSelf.value
    ? auth.profile
      ? {
          id: auth.profile.id,
          username: auth.profile.username,
          displayName: auth.profile.displayName,
          role: auth.profile.role,
          createdAt: auth.profile.createdAt,
        }
      : null
    : publicProfile.value
  if (!base) return null
  return { ...base, ...avatarOf(base.id, base.displayName) }
})

const joinedAt = computed(() => {
  const raw = view.value?.createdAt ?? ''
  // 服务端没返回（老缓存 / 装配缺失）时如实说，不拿空串冒充一个日期。
  return raw === '' ? '服务端未返回' : raw.slice(0, 10)
})

const selfEmail = computed(() => auth.profile?.email || '未绑定邮箱')

const selfEmailState = computed(() => {
  const p = auth.profile
  if (!p) return '未登录'
  if (!p.email) return '未绑定邮箱'
  // 三态：已验证 / 未验证 / 服务端未返回。把 undefined 当成"未验证"是在编造结论。
  if (p.emailVerified === true) return '已验证'
  if (p.emailVerified === false) return '未验证'
  return '服务端未返回验证状态'
})

function roleLabel(role: string): string {
  return role === 'admin' ? '管理员' : '普通用户'
}

/** 清掉"换了一个 id / 换了登录态"之后不该留着的界面状态。 */
function resetView(): void {
  publicProfile.value = null
  loading.value = false
  capabilityMissing.value = false
  notFound.value = false
  loadError.value = ''
  saveNotice.value = ''
  saveError.value = ''
  saveFieldError.value = ''
  saving.value = false
  draft.value = auth.profile?.displayName ?? ''
}

/** 按当前 id 决定读什么：本人零请求，他人读公开档案。 */
async function refresh(): Promise<void> {
  resetView()
  if (parsedId.value === 0 || isSelf.value) return

  loading.value = true
  try {
    publicProfile.value = await usersApi.getPublicProfile(parsedId.value)
  } catch (err) {
    if (isCapabilityUnavailable(err)) {
      capabilityMissing.value = true
      return
    }
    const e = toHttpError(err)
    if (e.status === 404) {
      notFound.value = true
      return
    }
    loadError.value = messageOf(e)
  } finally {
    loading.value = false
  }
}

/**
 * 只有 `props.id` 与"登录者 id"的**值**变化才重跑 loader。
 *
 * 刻意拼成字符串而不是 watch 数组：数组每次求值都是新引用，Vue 会当成变化；
 * 而 store 的档案被一次写操作替换时（id 没变）会白白重跑 loader，把刚写好的
 * 成功提示擦掉 —— 这类"提示被后续刷新静默清空"的 bug 在界面上表现为"点了没反应"。
 */
watch(
  () => `${props.id}:${auth.profile?.id ?? ''}`,
  () => {
    void refresh()
  },
  { immediate: true },
)

/**
 * store 里的昵称被**别处**改掉时（启动引导的 `/me` 回填、另一个标签页保存），
 * 输入框要跟着走 —— 否则会出现"标题是新名字、输入框还是旧名字"的错觉。
 *
 * 只在"用户没动过输入框"时同步（draft 仍等于 store 的旧值）；否则会覆盖正在输入的内容。
 */
watch(
  () => auth.profile?.displayName ?? '',
  (next, prev) => {
    if (!isSelf.value) return
    if (draft.value === (prev ?? '')) draft.value = next
  },
)

/** 保存昵称：服务端清洗后的权威档案直接落地 store（不再补一发 `/me`）。 */
async function saveDisplayName(): Promise<void> {
  if (saving.value) return
  saveNotice.value = ''
  saveError.value = ''
  saveFieldError.value = ''
  saving.value = true
  try {
    const updated = await usersApi.updateDisplayName(draft.value)
    auth.applyProfileUpdate(updated)
    draft.value = updated.displayName
    // 提示放在最后写（见文件头第 3 条）。
    saveNotice.value = `昵称已保存为「${updated.displayName}」`
  } catch (err) {
    if (isCapabilityUnavailable(err)) {
      capabilityMissing.value = true
      return
    }
    const e = toHttpError(err)
    if (e.status === 401) {
      // authedFetch 已经把人送去登录页（带 redirect）；这里只留一句可见说明。
      saveError.value = '登录状态已失效，请重新登录后再试'
      return
    }
    if (e.status === 403) {
      // 与「用户不存在」分开（#33 定稿）：这是**本人**的写操作被拒 —— 账号被封禁。
      saveError.value = `${e.message}（被封禁的账号不能改昵称；如有疑问请联系管理员）`
      return
    }
    if (e.status === 400) {
      // 400 只可能是昵称不合规：把错误定位到输入框，而不是整页一条报错。
      saveFieldError.value = e.message
      return
    }
    saveError.value = messageOf(e)
  } finally {
    saving.value = false
  }
}

async function doLogout(): Promise<void> {
  saving.value = true
  try {
    await auth.logout()
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="page">
    <main class="body">
      <!-- 地址里的 id 不合法：不请求，直接说清是「地址」的问题（与「用户不存在」区分）。 -->
      <section v-if="parsedId === 0" class="state" role="status">
        <h1 class="title">用户档案</h1>
        <p class="state-text">地址里的用户 id 不合法：它必须是正整数，例如 <code class="mono">/user/2</code>。</p>
        <div class="state-acts">
          <RouterLink class="btn" :to="{ name: 'friends' }">去找人</RouterLink>
          <RouterLink class="btn" to="/">回到首页</RouterLink>
        </div>
      </section>

      <p v-else-if="loading" class="dim" role="status">加载中…</p>

      <CapabilityNotice
        v-else-if="capabilityMissing"
        domain="users"
        title="用户档案后端尚未上线"
        hint="上线后这里是任何人的公开档案（用户名 / 昵称 / 加入时间 / 角色标签），以及你自己的昵称编辑。"
        retryable
        @retry="refresh"
      />

      <!-- 服务端对「不存在」与「被封禁」都回 404（#33）：界面不能替它区分，只能说明这一点。 -->
      <section v-else-if="notFound" class="state" role="status">
        <h1 class="title">用户不存在</h1>
        <p class="state-text">没有找到这个用户，或该账号当前不可见。</p>
        <p class="state-note">
          服务端对「不存在」与「账号被封禁」返回同一个结果 —— 否则任何人都能靠试探 id
          判断某个账号是否存在。这不是权限问题，也不是网络故障。
        </p>
        <div class="state-acts">
          <RouterLink class="btn" :to="{ name: 'friends' }">去找人</RouterLink>
          <RouterLink class="btn" to="/">回到首页</RouterLink>
        </div>
      </section>

      <p v-else-if="loadError" class="alert" role="alert">{{ loadError }}</p>

      <template v-else-if="view">
        <header class="hero">
          <span class="avatar-lg" :style="{ background: view.avatarColor }" aria-hidden="true">{{ view.avatarChar }}</span>
          <div class="who">
            <div class="name-row">
              <h1 class="title">{{ view.displayName }}</h1>
              <!-- 角色只作**标签**：管理员的真实边界是服务端 RequireAdmin（铁律 I3）。 -->
              <span v-if="view.role === 'admin'" class="badge">
                <BrandIcon name="shield-done" decorative :size="12" />
                管理员
              </span>
            </div>
            <p class="mono dim handle">@{{ view.username }}</p>
          </div>
        </header>

        <!-- 未登录访问他人档案：公开信息不需要登录，这里把这一点说明白，并给登录入口。 -->
        <p v-if="!auth.isLoggedIn && !isSelf" class="guest-note" role="status">
          你正在以游客身份查看公开档案（这些信息本来就不需要登录）。<RouterLink
            :to="{ name: 'login', query: { redirect: route.fullPath } }"
            >登录</RouterLink
          >后可以编辑你自己的资料。
        </p>

        <!-- 公开字段：只有这四项 + 角色标签，没有 email / status / 任何 token 信息。 -->
        <dl class="kv">
          <dt>用户名</dt>
          <dd class="mono">{{ view.username }}</dd>
          <dt>昵称</dt>
          <dd>{{ view.displayName }}</dd>
          <dt>加入时间</dt>
          <dd class="mono">{{ joinedAt }}</dd>
          <template v-if="isSelf">
            <!-- 以下三项只出现在**本人**视角：它们是账号自己的信息，不是公开档案的一部分。 -->
            <dt>邮箱</dt>
            <dd>{{ selfEmail }}</dd>
            <dt>邮箱状态</dt>
            <dd>{{ selfEmailState }}</dd>
            <dt>角色</dt>
            <dd>{{ roleLabel(view.role) }}</dd>
          </template>
        </dl>

        <!-- 本人：昵称编辑 -->
        <section v-if="isSelf" class="edit" aria-labelledby="edit-head">
          <h2 id="edit-head" class="sect-head">修改昵称</h2>
          <p class="dim small">
            最多 32 个字符；零宽与方向控制字符会被服务端剔除；清空会回落成用户名。
          </p>
          <form class="edit-form" @submit.prevent="saveDisplayName">
            <label class="sr-only" for="display-name">昵称</label>
            <input
              id="display-name"
              v-model="draft"
              type="text"
              autocomplete="nickname"
              maxlength="128"
              :disabled="saving"
            />
            <button class="primary" type="submit" :disabled="saving || draft === view.displayName">
              {{ saving ? '保存中…' : '保存' }}
            </button>
          </form>
          <p v-if="saveFieldError" class="alert" role="alert">{{ saveFieldError }}</p>
          <p v-if="saveError" class="alert" role="alert">{{ saveError }}</p>
          <p v-if="saveNotice" class="ok" role="status">{{ saveNotice }}</p>

          <div class="edit-foot">
            <span class="dim small">改完昵称后，作品署名与好友列表里显示的就是新名字。</span>
            <button class="ghost" type="button" :disabled="saving" @click="doLogout">退出登录</button>
          </div>
        </section>
      </template>
    </main>
  </div>
</template>

<style scoped>
.page {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}
.body {
  flex: 1;
  width: min(720px, 100%);
  margin: 0 auto;
  padding: 32px 20px 64px;
  display: flex;
  flex-direction: column;
  gap: 18px;
  align-items: flex-start;
}
.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(30px, 6vw, 44px);
  overflow-wrap: anywhere;
}

/* 头部：大头像 + 名字 + @用户名。375 档缩头像、换行。 */
.hero {
  display: flex;
  align-items: center;
  gap: 18px;
  width: 100%;
}
.avatar-lg {
  flex: none;
  width: 76px;
  height: 76px;
  border-radius: 50%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-weight: 700;
  font-size: 30px;
}
.who {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.name-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 9px;
  border: 1px solid var(--accent);
  color: var(--accent);
  font-size: 12px;
  line-height: 18px;
  white-space: nowrap;
}
.handle {
  margin: 0;
  font-size: 13px;
}

/* 公开字段表：窄屏时仍是"标签 / 值"两列，长值可换行。 */
.kv {
  margin: 0;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 8px 18px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 16px;
  width: 100%;
  box-sizing: border-box;
}
.kv dt {
  color: var(--dim);
  font-size: 13px;
}
.kv dd {
  margin: 0;
  font-size: 13px;
  overflow-wrap: anywhere;
}

.guest-note {
  margin: 0;
  padding: 10px 12px;
  border: 1px dashed var(--line);
  font-size: 13px;
  line-height: 1.7;
  color: var(--dim);
  width: 100%;
  box-sizing: border-box;
}

.edit {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 100%;
  border-top: 1px solid var(--line);
  padding-top: 16px;
}
.sect-head {
  margin: 0;
  font-size: 15px;
}
.edit-form {
  display: flex;
  gap: 8px;
  width: 100%;
}
.edit-form input {
  flex: 1;
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--line);
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
}
.edit-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  width: 100%;
}

.state {
  display: flex;
  flex-direction: column;
  gap: 12px;
  align-items: flex-start;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 18px 20px;
  width: 100%;
  box-sizing: border-box;
}
.state-text {
  margin: 0;
  font-size: 14px;
  line-height: 1.7;
}
.state-note {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: var(--dim);
}
.state-acts {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px 14px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}
.btn:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}
.primary {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 8px 18px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
  font-size: 13px;
  white-space: nowrap;
}
.primary:hover {
  text-decoration: none;
  filter: brightness(1.08);
}
.primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.ghost {
  padding: 8px 16px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}
.ghost:hover {
  color: var(--text);
  border-color: var(--accent);
}

.alert {
  margin: 0;
  padding: 8px 10px;
  border: 1px solid var(--danger);
  background: rgba(255, 92, 92, 0.12);
  font-size: 12px;
  color: #ffdada;
  width: 100%;
  box-sizing: border-box;
}
.ok {
  margin: 0;
  padding: 8px 10px;
  border: 1px solid var(--accent);
  background: rgba(180, 108, 255, 0.1);
  font-size: 12px;
  width: 100%;
  box-sizing: border-box;
}
.mono {
  font-family: var(--font-mono);
}
.dim {
  color: var(--dim);
}
.small {
  font-size: 12px;
}

/* 读屏可用、视觉隐藏（输入框已经用 placeholder/label 语义表达）。 */
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

@media (max-width: 560px) {
  .body {
    padding: 20px 14px 48px;
  }
  .hero {
    gap: 14px;
  }
  .avatar-lg {
    width: 58px;
    height: 58px;
    font-size: 24px;
  }
  .edit-form {
    flex-wrap: wrap;
  }
  .edit-form .primary {
    width: 100%;
  }
}
</style>
