<script setup lang="ts">
/**
 * 帮助与说明（`/help`）。
 *
 * 存在的理由：核心页面（首页 / 房间页 / 登录注册）此前堆着成段的解释文字，
 * 把"要做的事"挤到了折叠线以下。现在所有长解释集中在这里，核心页只留一句话 + 图标入口。
 *
 * 约定：
 *   - 目录是可点击锚点，锚点 id 被其它页面深链引用（`/help#source`、`/help#why-login`…），
 *     改名要同时改引用方；
 *   - 文案里的默认值都对应服务端配置项（并给出环境变量名），说"默认"就一定是代码里的默认值，
 *     不确定的地方写明"以服务端配置为准"，不编数字。
 */
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'

/** 开源仓库地址（作者 drayee）。 */
const REPO_URL = 'https://github.com/Drayee/ProjectionRoom'

/** 目录：`id` 同时是锚点与深链目标。 */
const toc = [
  { id: 'why-login', text: '为什么建房要登录、看片不用登录' },
  { id: 'source', text: '怎么准备片源（切片器 / 服务端切片 / 下载 exe）' },
  { id: 'room-code', text: '房间码、房间密码与主播复位令牌' },
  { id: 'playback', text: '播放、画质与延迟的取舍' },
  { id: 'diagnostics', text: '诊断抽屉里的指标是什么意思' },
  { id: 'privacy', text: '隐私与安全' },
  { id: 'faq', text: '常见问题' },
]
</script>

<template>
  <div class="help">
    <AppHeader variant="page" />

    <header class="help-head">
      <img class="logo" src="/icons/夜晚.svg" alt="" width="34" height="34" aria-hidden="true" />
      <div class="head-text">
        <h1>帮助与说明</h1>
        <p class="muted brand-line">
          <span>月喵 · 作者 drayee · 开源</span>
          <a
            class="icon-btn repo"
            :href="REPO_URL"
            target="_blank"
            rel="noopener noreferrer"
            aria-label="开源仓库（作者 drayee）"
            v-tip="'开源仓库（作者 drayee）'"
            data-testid="help-github"
          >
            <BrandIcon name="github" decorative :size="18" />
          </a>
        </p>
      </div>
    </header>

    <nav class="toc card" aria-label="目录">
      <h2>
        <BrandIcon name="tips" decorative :size="16" />
        目录
      </h2>
      <ol>
        <li v-for="(item, index) in toc" :key="item.id">
          <a :href="`#${item.id}`">{{ index + 1 }}. {{ item.text }}</a>
        </li>
      </ol>
    </nav>

    <!-- 1 -->
    <section class="card" id="why-login">
      <h2>1. 为什么建房要登录、看片不用登录</h2>
      <p>
        授权 <b>100% 在服务端</b>，前端不做守卫。两类人因此走两条不同的路：
      </p>
      <ul>
        <li>
          <b>主播（建房）需要账号</b>：<code class="mono">POST /api/rooms</code> 在服务端挂了鉴权，
          匿名请求会被 401 拒绝，房间不会因此被创建。房间里"谁是房主"这件事要有可追溯的主体，
          也是管理端（封禁、归属核查）能对上的前提。
        </li>
        <li>
          <b>观众（按房间码进房）不需要账号</b>：这是产品决定。拿着房间码点「进入房间」，
          服务端只校验房间码与（可选的）房间密码。未登录也能进房看片。
        </li>
      </ul>
      <p class="muted small">
        所以前端没有"未登录一律跳登录页"的全局守卫：那样会顺手把观众的这扇门关上。
        需要登录的是<b>具体动作</b>（建房），由动作入口就地拦截并带上回跳路径，登录成功后回到发起处。
      </p>
    </section>

    <!-- 2 -->
    <section class="card" id="source">
      <h2>2. 怎么准备片源（切片器 / 服务端切片 / 下载 exe）</h2>
      <p>
        月喵播放的是<b>已经切好片的分片目录</b>（fMP4：<code class="mono">init.mp4</code> +
        <code class="mono">c00001.m4s…</code> + <code class="mono">index.json</code>）。
        切完之后，主播在房间页点「选择分片目录」选中那个目录即可开播。三条路任选一条：
      </p>

      <h3>2.1 本机有 ffmpeg：自己切（推荐，最快）</h3>
      <pre class="mono">REM 普通 MP4：先无损重新封装成 fragmented MP4，再按 moof 切分
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -fragment

REM 已经是 fMP4：直接切（这一步不需要 ffmpeg）
go run ./cmd/segmenter -in out_frag.mp4 -out ./room-media

REM 低上行／浏览器不支持的编码：转码降码率后再切
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -transcode 1200k -uplink-mbps 3</pre>
      <p class="muted small">
        常用参数：<code class="mono">-frag-sec</code> 分片秒数（默认 2）、
        <code class="mono">-ffmpeg</code> 指定 ffmpeg 路径（目录或可执行文件）、
        <code class="mono">-pack N</code> 控制每个 <code class="mono">.bin</code> 装几片（默认 100）。
      </p>

      <h3>2.2 不想敲命令：用下载好的切片器</h3>
      <p>
        主播页展开「本机没有 ffmpeg？交给服务器切片 / 下载切片工具」，里面按平台列出可下载的
        <code class="mono">segmenter</code> 二进制（服务端发布在 <code class="mono">/downloads/segmenter-*</code>，
        清单接口是 <code class="mono">GET /api/downloads/segmenter</code>）。两条最省事的用法：
      </p>
      <ul>
        <li>把视频文件<b>直接拖到下载好的 exe 上</b>；</li>
        <li>或者<b>双击 exe</b> 后按提示输入文件路径（两者与 2.1 的命令行完全等价）。</li>
      </ul>
      <p class="muted small">
        本机没有 ffmpeg 也没关系：exe 会提示 5 秒后自动下载一份放到它旁边（这 5 秒里按
        <code class="mono">Ctrl+C</code> 可取消）；已经有 ffmpeg 时用
        <code class="mono">-ffmpeg-dir &lt;目录或可执行文件&gt;</code> 指定即可。
        下载完请照面板里的 sha256 校验一次（面板同时给出 Windows / macOS / Linux 的校验命令）。
      </p>

      <h3>2.3 完全不想装东西：让服务器切片</h3>
      <p>
        主播页的「服务端切片」面板把源视频上传给服务器切，切完可以「下载 zip」或「写入本地目录并开播」。
        默认配额与限制（都是服务端配置项，可按部署调整）：
      </p>
      <ul>
        <li>单文件上限 <b>16 GiB / 60 分钟</b>（时长在前端先用 &lt;video&gt; 读 metadata 预判，超限不白传）；</li>
        <li>同时切 <b>2 个</b>作业、最多排 <b>8 个</b>、人均 <b>3 个作业/分钟</b>；</li>
        <li>产物与作业记录保留 <b>30 分钟</b>，过期连临时目录一起清理（要留着就先下载到本地）。</li>
      </ul>
      <p class="muted small">
        只支持 <code class="mono">.mp4</code> / <code class="mono">.mkv</code> 这类常见容器；
        上传链路只在"开播前的媒体准备"阶段存在，直播期服务器不接触任何视频字节。
      </p>

      <h3>2.4 片源从哪来</h3>
      <p>
        本项目<b>不提供也不分发任何内容</b>，服务端上传接口也没有任何内置素材。
        仓库里 <code class="mono">test/resource/</code> 下的视频只用于自动化验收
        （例如 <code class="mono">test/resource/short_video/cut</code> 是切好的分片目录，
        <code class="mono">test/resource/big_mp4_video.mp4</code> 用于验证超时长会被拒绝），
        它们不是给你看的片源。
      </p>
      <p class="muted small">
        还没有片源的话，可以到第三方资源站自行寻找，例如
        <a
          href="https://www.comicat.org/"
          target="_blank"
          rel="noopener noreferrer"
          data-testid="anime-source-link"
          >comicat.org（番剧资源，新窗口打开）</a
        >。请自行确认内容的来源与合法性。
      </p>
    </section>

    <!-- 3 -->
    <section class="card" id="room-code">
      <h2>3. 房间码、房间密码与主播复位令牌</h2>
      <ul>
        <li>
          <b>房间码</b>：建房成功后拿到的 6 位码，是房间的唯一入口（房间页左上角可点击复制）。
          观众拿它就能进房，不需要账号。
        </li>
        <li>
          <b>房间密码</b>：建房时可选。留空 = 谁都能进；设了就要求观众填同一个密码才会被服务端放行。
          密码只进本机 sessionStorage 作为进房凭据，刷新页面仍在，关掉标签页即失效。
        </li>
        <li>
          <b>主播复位令牌</b>：建房响应里一次性下发的随机串（服务端只存哈希，之后再也拿不到）。
          主播断线后在服务端宽限期内重连时，要靠它接回<b>同一房间码的主播位</b>；
          丢了它就只能重开一个房间。它和进房密码存在一起，同样跟着标签页走。
        </li>
      </ul>
      <p class="muted small">
        房间的回收与默认时长（都以服务端配置为准）：创建后从未有人进房
        <b>10 分钟</b>（<code class="mono">PR_ROOM_UNCLAIMED_TTL</code>）；
        主播断线宽限期 <b>60 秒</b>（<code class="mono">PR_ROOM_HOST_GRACE</code>，上限 10 分钟）；
        房间里没人且不在宽限期 <b>30 分钟</b>（<code class="mono">PR_ROOM_EMPTY_TTL</code>）。
      </p>
    </section>

    <!-- 4 -->
    <section class="card" id="playback">
      <h2>4. 播放、画质与延迟的取舍</h2>
      <ul>
        <li>
          <b>多缓冲一点更稳，但延迟更大</b>：观众从主播时间戳所在分片起，攒够连续 n 片才起播
          （启动门控<b>不设超时上限</b>，只在画面里显示"缓冲中 x/y 片、已等待多少秒"）。
          上行带宽充裕时把门控调小更跟手，带宽紧张时调大能避免反复卡顿。
        </li>
        <li>
          <b>画质由片源决定</b>：客户端不做重编码，看到的画质就是主播切出来的那份。
          上行不够、或编码浏览器解不了时，主播用 <code class="mono">-transcode 1200k</code> 之类重新切一遍。
        </li>
        <li>
          <b>为什么会自动换父</b>：观众的分片来自 P2P 上游（父节点），不是服务器。
          父节点的速率、RTT、稳定性会被持续评估；某条边超时（阈值取 3×RTT 与 2×预计传输耗时里的较大者）
          就换到备用父，并给刚才那条边"降权"一段时间。换父瞬间会重新缓冲，
          这是"用一次短缓冲换掉持续卡顿"。
        </li>
        <li>
          <b>卡顿怎么办</b>：① 主播端看诊断抽屉的「每边速率 / 调度」，上行估计偏低就降码率重切；
          ② 观众端看「与主播的差」，落后过多会自动跳转到主播当前进度（会明确提示）；
          ③ 一直"加载中"且「上游链路」显示"未安置"，说明房间已满或暂时没有可用父节点 —— 多等一会儿或让主播重开房间。
        </li>
      </ul>
    </section>

    <!-- 5 -->
    <section class="card" id="diagnostics">
      <h2>5. 诊断抽屉里的指标是什么意思</h2>
      <p class="muted small">
        房间页底部的「诊断 / 排障日志」默认收起（日志每 3 秒可能新增，默认展开会刷屏）。
        它是只读的环形缓冲，保留最近若干条；要重新取证就刷新页面。
      </p>
      <div class="kv">
        <span class="k">上游链路</span>
        <span class="v">
          主播侧是「P2P N 条（已开通道 …）· 上行估计」；观众侧是「主父 …（备用 …）· 已开通道 x/y」。
          显示「未安置」= 服务端确实没有分配父节点（房间满 / 无可用父），与"还没下发"是两件事。
        </span>

        <span class="k">在途</span>
        <span class="v">
          「调度」里的<b>在途上限</b>：由边速率、平均分片大小与期望延迟推出来的一次可并行请求数
          （同时列出此刻实际在途数）。发送队列按"离播放头距离"升序，已播过的排最后。
        </span>

        <span class="k">换父</span>
        <span class="v">
          「超时换父」累计次数与「降权父」个数：换父是超时触发的自动动作，降权是"短期内不再优先选它"。
        </span>

        <span class="k">延迟 / 与主播的差</span>
        <span class="v">
          本机播放头与主播播放头的差值（正数=落后）。小于 0.5s 视为基本同步；落后过多会触发提示与强制对齐。
        </span>

        <span class="k">门控</span>
        <span class="v">启动门控的实时状态：还需要连续几片、已缓冲几秒、已等多久（不设超时）。</span>

        <span class="k">时钟</span>
        <span class="v">
          同步时钟的样本是否就绪、偏差（drift）与偏移（offset = 本跳 + 父节点），是上面"延迟"的测量基础。
        </span>

        <span class="k">播放健康度</span>
        <span class="v">
          <b>按时率</b>（按时 / 迟到分片）+ <b>卡顿次数</b>（&lt;video&gt; 的 waiting 事件）。
          "优化到底有没有用"看这两项就够了。
        </span>

        <span class="k">每边速率</span>
        <span class="v">
          每个节点一条：速率／峰值、RTT、超时阈值（含预计传输耗时）、交付与超时次数。主父带 <code class="mono">*</code>。
        </span>

        <span class="k">内容校验</span>
        <span class="v">
          收到的分片按 sha256 校验的坏片／通过／跳过计数。坏片会被丢弃、绝不进播放器。
          「跳过」出现在非安全上下文（明文 http 的局域网地址没有 <code class="mono">crypto.subtle</code>）。
        </span>

        <span class="k">拓扑</span>
        <span class="v">
          模式（扇出/单链）、深度、主父与备用父、下游个数、分发节点与最近换防，
          以及服务端给出的<b>分配依据</b>（为什么把你挂在那个父节点下面）。
        </span>

        <span class="k">ICE / IPv6</span>
        <span class="v">
          ICE 载荷 TTL（默认 300s）、刷新次数与"列表是否变化"、重启次数；被白名单过滤掉的 STUN 条目；
          IPv6 直连是否真的拿到全局地址、被挡下的不可跨网候选条数。
        </span>
      </div>
      <p class="muted small">
        需要贴给别人看时点「复制诊断报告」。报告默认把本机与对端地址<b>掩码</b>：
        要带上完整地址得显式勾选「包含完整地址（仅自己看）」，那个开关不落盘、刷新即回到掩码。
      </p>
    </section>

    <!-- 6 -->
    <section class="card" id="privacy">
      <h2>
        <BrandIcon name="shield-done" decorative :size="18" />
        6. 隐私与安全
      </h2>
      <ul>
        <li>
          <b>服务端不传输视频字节</b>：服务器只做信令、房间状态与拓扑分配（HTTP + WebSocket），
          视频与分片经由 WebRTC 在浏览器之间直连（P2P）。唯一的例外是你主动用「服务端切片」：
          那时源视频会上传给服务器切一次，产物 30 分钟后清理。
        </li>
        <li>
          <b>房间密码不是端到端加密</b>：它只是服务端放行的凭据。传输本身用 WebRTC 自带的 DTLS/SRTP 加密，
          但同一房间里的其他成员在协议上仍可拿到分片 —— 房间里的人彼此可见。
        </li>
        <li>
          <b>明文 http 的已知限制</b>：无 TLS 部署（<code class="mono">http://</code>）下，
          剪贴板 API 与 <code class="mono">crypto.subtle</code> 在非 localhost 地址上不可用。
          前者会退回旧式复制并如实报告失败；后者会让分片内容校验退化为「跳过」（诊断里会写明）。
          生产部署建议放在 HTTPS 反向代理后面。
        </li>
        <li>
          <b>日志会被管理员查看</b>：服务端标准日志（含加入/离开房间、被拒原因、管理端操作）
          会留在内存环形缓冲里，管理员可以通过 <code class="mono">GET /api/admin/logs</code> 读取。
          诊断报告里的地址默认掩码，也是因为这个报告是设计成要贴出去给别人看的。
        </li>
        <li>
          <b>账号凭据</b>：access token 只存内存与 sessionStorage（关掉标签页即失效），
          refresh token 放在 HttpOnly Cookie 里；本地缓存的档案（用户名、昵称）只用于显示，
          不是授权依据。
        </li>
      </ul>
    </section>

    <!-- 7 -->
    <section class="card" id="faq">
      <h2>7. 常见问题</h2>
      <dl class="faq">
        <dt>提示「房间 XXXX 不存在」</dt>
        <dd>
          房间码打错了，或者房间已被回收（空置 10 分钟 / 无成员 30 分钟，见第 3 节）。
          让主播重新建房拿一个新房间码。
        </dd>

        <dt>提示「房间 XXXX 还没有主播进房，请稍候」</dt>
        <dd>
          房间建出来了但主播还没进房，此时进房没有可跟随的源。等主播点进来再试。
        </dd>

        <dt>一直「加载中」/「房间已满」</dt>
        <dd>
          诊断抽屉的「上游链路」若显示「未安置」，就是服务端分不出父节点（房间成员已达上限、
          或暂时没有带宽余量足够的节点）。等一会儿或让主播重开房间；主播侧降低码率重切也能腾出余量。
        </dd>

        <dt>信令一直重连</dt>
        <dd>
          页面顶部的连接徽标会说明状态。客户端会自动重连，主播断线还有 60 秒宽限期（期间房间与房间码都不变）。
          若长时间连不上，先确认服务端进程还在、以及浏览器到服务端的网络没有断。
        </dd>

        <dt>复制房间码失败</dt>
        <dd>
          明文 http 下异步剪贴板不可用，会退回旧式复制；两者都失败时按钮上会明写「复制失败，请手动选中」，
          此时直接选中房间码按 Ctrl+C 即可。分享按钮同理。
        </dd>

        <dt>上传/切片报错</dt>
        <dd>
          「服务端切片不可用」说明 <code class="mono">/api</code> 连不上（后端没起或代理没配）；
          「本机没有 ffmpeg」是你的机器缺 ffmpeg，改用第 2.2 节的切片器或第 2.1 节的命令。
          超时长/超大文件会在上传前就被前端拦下。
        </dd>
      </dl>
    </section>

    <footer class="help-foot muted">
      <span>月喵 · 作者 drayee · 开源项目</span>
      <a
        class="icon-btn"
        :href="REPO_URL"
        target="_blank"
        rel="noopener noreferrer"
        aria-label="开源仓库（作者 drayee）"
        v-tip="'开源仓库（作者 drayee）'"
      >
        <BrandIcon name="github" decorative :size="16" />
      </a>
    </footer>
  </div>
</template>

<style scoped>
.help {
  max-width: 880px;
  margin: 0 auto;
  padding: 32px 20px 48px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.help-head {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.logo {
  flex: none;
}

.head-text {
  min-width: 0;
}

.help-head h1 {
  margin: 0 0 2px;
  font-size: 24px;
}

.brand-line {
  margin: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  font-size: 12px;
}

.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 4px;
  color: var(--text-dim);
  border: 1px solid transparent;
  border-radius: 6px;
  text-decoration: none;
  line-height: 0;
}

.icon-btn:hover {
  color: var(--text);
  border-color: var(--border);
  text-decoration: none;
}

h2 {
  margin: 0 0 10px;
  font-size: 16px;
  display: flex;
  align-items: center;
  gap: 7px;
}

h3 {
  margin: 14px 0 8px;
  font-size: 13px;
  color: var(--text-dim);
}

/* 锚点跳转落点留一点上边距，标题不贴视口顶边。 */
section[id] {
  scroll-margin-top: 16px;
}

.toc h2 {
  font-size: 14px;
}

.toc ol {
  margin: 0;
  padding-left: 20px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 13px;
  line-height: 1.6;
}

p {
  margin: 0 0 10px;
  line-height: 1.75;
}

ul {
  margin: 0 0 10px;
  padding-left: 20px;
  line-height: 1.75;
}

li + li {
  margin-top: 6px;
}

pre {
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 10px;
  margin: 0 0 10px;
  font-size: 12px;
  line-height: 1.55;
  overflow-x: auto;
  white-space: pre;
}

code {
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0 4px;
  font-size: 12px;
}

.small {
  font-size: 12px;
}

.kv {
  display: grid;
  grid-template-columns: 104px minmax(0, 1fr);
  gap: 6px 12px;
  font-size: 13px;
  line-height: 1.7;
  margin-bottom: 10px;
}

.kv .k {
  color: var(--text-dim);
}

.kv .v {
  min-width: 0;
  word-break: break-word;
}

.faq {
  margin: 0;
  line-height: 1.75;
}

.faq dt {
  font-weight: 600;
  margin-top: 10px;
}

.faq dt:first-child {
  margin-top: 0;
}

.faq dd {
  margin: 4px 0 0;
  padding-left: 16px;
  color: var(--text-dim);
}

.help-foot {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
</style>
