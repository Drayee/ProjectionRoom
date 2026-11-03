<script setup lang="ts">
/**
 * 公开档案页（`/user/:id`）。wayfinder #32 起步，#68 扩成完整公开视角，
 * #69 把管理动作整体搬去 `/me`。
 *
 * ## 这一页现在只有**一种**视角
 *
 * 决议 #47 定的双路由：`/user/:id` 是公开橱窗，`/me` 是本人的管理台。
 * 所以**本人访问自己的 id 时也是公开视角**（不再按 `isSelf` 切管理视图），
 * 只在底部多一条提示 + 去管理台的链接。旧实现里那套「同一地址、登录前后两个页面」
 * 的分支已拆除：它的症状是分享出去一个档案链接，对方看到的东西和你看到的不一样。
 *
 * 本人视角仍然**不读公开档案**（`GET /api/users/:id`）：mock 的人员目录里不一定有
 * 当前真实账号的 id，读它会显示「用户不存在」；本人的权威显示数据在 auth store 里。
 *
 * ## 三条关键判断
 *
 *  - **404 与 403 是两个故事**（#33 定稿）：`GET /api/users/:id` 对「不存在」与
 *    「被封禁」都回 **404**（区分它们等于开一条存在性探测通道），所以本页只能说
 *    「不存在或不可见」。管理动作的 403（被封禁的账号不能改昵称）发生在 `/me`。
 *  - **关系栏的准入与档案不同**：`GET /api/follows/:userId` 需要登录，
 *    而公开档案不需要 —— 两者分开读，关系读失败不拖垮整页。
 *  - **`watch` 只盯 id 的字符串**：数组每次求值都是新引用，store 被一次写操作
 *    替换时会白白重跑 loader，把刚写好的提示擦掉（症状是"点了没反应"）。
 */
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import AvatarImage from '../components/ui/AvatarImage.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import RelativeTime from '../components/ui/RelativeTime.vue'
import AuthorPostsBlock from '../components/features/profile/AuthorPostsBlock.vue'
import AuthorRoomsBlock from '../components/features/profile/AuthorRoomsBlock.vue'
import * as usersApi from '../api/users'
import type { PublicProfile } from '../api/users'
import * as followsApi from '../api/follows'
import type { FollowRelation } from '../api/follows'
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
          // 像素头像（票 #74）：本人视角也要带上，否则自己的档案页
          // 与别人看到的你长得不一样 —— 那种"只有我看不到"的缺陷最难自查。
          avatarUrl: auth.profile.avatarUrl,
        }
      : null
    : publicProfile.value
  if (!base) return null
  return { ...base, ...avatarOf(base.id, base.displayName) }
})

const joinedAt = computed(() => {
  const raw = view.value?.createdAt ?? ''
  return { raw, missing: raw === '' }
})

// ---------- 关系动作区（wayfinder #56） ----------

/**
 * 我与 TA 的关系（服务端算好的派生视图）。
 *
 * null = 还没查到 / 查不到（游客看不到"我有没有关注 TA" —— 那需要登录）。
 * 它**不是**本地推出来的：互关与 canDm 都是派生事实（两个方向都有边 + 未被拉黑），
 * 前端自己算会与服务端漂移（铁律 I3：授权事实只在服务端）。
 */
const relation = ref<FollowRelation | null>(null)

/** 关注动作在途（单条锁：只锁这一个按钮，不锁整页）。 */
const followBusy = ref(false)
/** 关注动作的失败文案（服务端原样）。 */
const followError = ref('')
/** 关注成功的提示（一次性）。 */
const followNotice = ref('')

/**
 * 关系动作该显示什么。
 *
 * 六种（GLOSSARY「Profile」· 关系动作）：
 *   - self       本人：没有关注自己的动作；
 *   - none       还没关注 → 「关注」；
 *   - following  我关注了 TA → 「已关注」（可取消）；
 *   - follow-back TA 关注了我 → 「回关」（点完即互关 = 好友）；
 *   - mutual    互关 → 「互关·发消息」；
 *   - blocked   拉黑存续期 → 关注按钮整个消失（GLOSSARY：拉黑存续期不给关注按钮）。
 *
 * ⚠ **最终判据在服务端**：前端显示"关注"而服务端说 403（拉黑），
 * 页面会照服务端文案说明。反过来前端显示"已关注"而服务端没有这条边，
 * 那才是真 bug —— 所以关系态一律取服务端回的那份。
 */
const followAction = computed<'self' | 'none' | 'following' | 'follow-back' | 'mutual' | 'blocked'>(() => {
  if (isSelf.value) return 'self'
  const r = relation.value
  if (!r) return 'none'
  if (r.mutual) return 'mutual'
  if (r.following) return 'following'
  if (r.followedBy) return 'follow-back'
  return 'none'
})

/** 关系动作按钮的文案（随动作态变）。 */
const followActionLabel = computed(() => {
  switch (followAction.value) {
    case 'following':
      return '已关注'
    case 'follow-back':
      return '回关'
    case 'mutual':
      return '互关 · 发消息'
    default:
      return '关注'
  }
})

/** 「已关注」的次级动作：取消关注（取关是**幂等**的，重复点不报错）。 */
const canUnfollow = computed(() => followAction.value === 'following')

/** 清掉"换了一个 id / 换了登录态"之后不该留着的界面状态。 */
function resetView(): void {
  publicProfile.value = null
  loading.value = false
  capabilityMissing.value = false
  notFound.value = false
  loadError.value = ''
  relation.value = null
  followBusy.value = false
  followError.value = ''
  followNotice.value = ''
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
 * 读我与 TA 的关系。
 *
 * **只在登录态下读**：关系是私有事实（"我有没有关注 TA"不是公开信息），
 * 游客访问公开档案时这一栏不出现（服务端也 401）。
 *
 * 失败**不**让整页失败：关系动作只是档案的一栏，
 * 而公开档案（名字、加入时间）在关系读不到时仍然可用。
 * 失败只落到那一栏的提示上 —— 整页变成"用户不存在"是过度的反应。
 */
async function loadRelation(): Promise<void> {
  if (parsedId.value === 0 || isSelf.value || !auth.isLoggedIn) {
    relation.value = null
    return
  }
  try {
    relation.value = await followsApi.getRelation(parsedId.value)
  } catch (err) {
    if (isCapabilityUnavailable(err)) {
      // 整域未上线：那一栏不渲染，其余照常（与页面其它部分的降级口径一致）。
      relation.value = null
      return
    }
    const e = toHttpError(err)
    // 401：authedFetch 已经把人送去登录页（带 redirect），这里不重复提示。
    if (e.status === 401) {
      relation.value = null
      return
    }
    // 404（对方刚被注销 / 不可见）：关系栏空着，档案其余部分照常。
    relation.value = null
  }
}

/**
 * 关注 / 回关。
 *
 * 用服务端回**权威关系**替换本地状态（不回 void）：互关是派生的，
 * 本地自己算会与服务端漂移，而"按钮显示已关注但服务端没有这条边"是要避免的症状。
 */
async function doFollow(): Promise<void> {
  if (followBusy.value) return
  const target = parsedId.value
  if (target === 0) return
  followError.value = ''
  followNotice.value = ''
  followBusy.value = true
  try {
    const next = await followsApi.follow(target)
    relation.value = next
    followNotice.value = next.mutual ? '互关成功：你们现在是好友' : '已关注'
  } catch (err) {
    const e = toHttpError(err)
    if (e.status === 401) {
      followError.value = '登录状态已失效，请重新登录后再试'
      return
    }
    followError.value = messageOf(e)
  } finally {
    followBusy.value = false
  }
}

/** 取关（幂等：重复点不会报错，服务端也只删一条边）。 */
async function doUnfollow(): Promise<void> {
  if (followBusy.value) return
  const target = parsedId.value
  if (target === 0) return
  followError.value = ''
  followNotice.value = ''
  followBusy.value = true
  try {
    await followsApi.unfollow(target)
    // 本地状态：把"我关注了 TA"这一位翻掉，其余（TA 关注我 / 计数）由服务端说了算 ——
    // 但这里不必再打一次 GET：取关只影响**我这一条边**，其余位不变。
    if (relation.value) {
      relation.value = { ...relation.value, following: false, mutual: false, canDm: false }
    }
    followNotice.value = '已取消关注'
  } catch (err) {
    const e = toHttpError(err)
    if (e.status === 401) {
      followError.value = '登录状态已失效，请重新登录后再试'
      return
    }
    followError.value = messageOf(e)
  } finally {
    followBusy.value = false
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
    // 关系单独读：它需要登录态，而公开档案不需要 ——
    // 两者的准入不同，所以**不**并进 refresh()（那会让登录态变化拖垮档案读取）。
    void loadRelation()
  },
  { immediate: true },
)

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
        hint="上线后这里是任何人的公开档案（用户名 / 昵称 / 加入时间 / 角色标签）与 TA 的帖子、放映室。"
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
          <span
            class="avatar-lg"
            :style="{ background: view.avatarColor }"
            aria-hidden="true"
            :data-shared="`avatar:${view.id}`"
          >
            {{ view.avatarChar }}
            <AvatarImage :url="view.avatarUrl" />
          </span>
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

        <!-- 关系动作区（wayfinder #56）：关注 / 回关 / 已关注 / 互关·发消息。 -->
        <section v-if="!isSelf && auth.isLoggedIn" class="relation" aria-labelledby="relation-head">
          <h2 id="relation-head" class="sr-only">关系动作</h2>

          <div class="relation-acts">
            <!--
              互关 → 显示「互关 · 发消息」，但**发消息只在互关时可用**（can_dm）。
              这里把两个动作分开：一个按钮是关系动作（关注/回关/已关注），
              另一个是消息入口（仅互关且可私聊时出现）。
              为什么分开：关系动作是幂等的（再点一次不报错），
              而"发消息"是**跳转** —— 两者混成一个按钮会让"点一下没反应"变成可能。
            -->
            <button
              v-if="followAction !== 'mutual'"
              type="button"
              class="primary"
              :disabled="followBusy"
              data-testid="follow-button"
              @click="doFollow"
            >
              {{ followBusy ? '处理中…' : followActionLabel }}
            </button>

            <RouterLink
              v-else
              class="primary as-link"
              :to="{ name: 'messages', query: { peer: parsedId } }"
              data-testid="follow-message-link"
            >
              互关 · 发消息
            </RouterLink>

            <!-- 取关：只在"我关注了 TA 且未互关"时出现（互关时用「发消息」那一侧）。 -->
            <button
              v-if="canUnfollow"
              type="button"
              class="ghost"
              :disabled="followBusy"
              data-testid="unfollow-button"
              @click="doUnfollow"
            >
              取消关注
            </button>
          </div>

          <p v-if="followError" class="alert" role="alert">{{ followError }}</p>
          <p v-if="followNotice" class="ok" role="status">{{ followNotice }}</p>
        </section>

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
          <dd class="mono">
            <span v-if="joinedAt.missing">服务端未返回</span>
            <RelativeTime v-else :value="joinedAt.raw" />
          </dd>
        </dl>

        <!--
          内容块（票 #68）：TA 的帖子 → TA 的放映室。
          两块各自管自己的加载/空/错与无限滚动，页面只负责按块序摆位置。
          **收藏块刻意不渲染**：收藏是私人书签（票 #64 / 决议 #48），
          除本人外全世界看不到 —— 他人档案页上连"这里本来有个块"的痕迹都不该有。
        -->
        <AuthorPostsBlock v-if="parsedId !== 0" :user-id="parsedId" />
        <AuthorRoomsBlock v-if="parsedId !== 0" :user-id="parsedId" />

        <!--
          关注数 / 粉丝数：**纯数字，不可点**（决议 #47 块序第 ⑤ 项）。
          名单只在 /me 看自己的（GLOSSARY「Profile」）—— 把它做成链接等于
          给别人开一条"谁关注了谁"的探查通道，而那不在本票范围内。
        -->
        <section v-if="relation" class="counts-block" aria-labelledby="counts-head">
          <h2 id="counts-head" class="sr-only">关注与粉丝</h2>
          <p class="counts dim" data-testid="profile-counts">
            <span><strong>{{ relation.followingCount }}</strong> 关注</span>
            <span aria-hidden="true">·</span>
            <span><strong>{{ relation.followerCount }}</strong> 粉丝</span>
          </p>
        </section>

        <!--
          本人访问自己的 /user/:id：这里是**纯公开视角**（决议 #47）。
          所有管理动作（改昵称、我的帖子/放映室、关系名单、账号设置）在 `/me`，
          所以这里给一个真的跳转 —— 而不是只留一句"功能搬走了"。
        -->
        <p v-if="isSelf" class="guest-note" role="status">
          这是别人看到的你的公开页。改昵称、管帖子与放映室、看关系名单，去
          <RouterLink :to="{ name: 'me' }">我的管理台</RouterLink>。
        </p>
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
  /* position + overflow 是像素头像覆盖层（`ui/AvatarImage.vue`）的前提：
     它绝对定位盖在字母上，父级不裁剪就会溢出圆外。 */
  position: relative;
  overflow: hidden;
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

/* 关系动作区：名片与公开字段之间的一栏。 */
.relation {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 100%;
}
.counts {
  display: flex;
  gap: 8px;
  align-items: baseline;
  margin: 0;
  font-size: 13px;
}
.counts strong {
  color: var(--text);
  font-weight: 700;
}
.relation-acts {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  align-items: center;
}
/* 主行动做成链接时的外观：与 .primary 同一套视觉，但保留链接语义。 */
.as-link {
  text-decoration: none;
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
