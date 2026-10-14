#!/usr/bin/env node
/**
 * 生成「月夜故障」视觉方向原型的单文件 HTML（PROTOTYPE — 抛弃物，勿进生产）。
 *
 * 用法：node docs/prototypes/_build-glitch-proto.mjs
 * 产出：docs/prototypes/glitch-direction.html（双击即开，无外链资源）
 *
 * 三个变体沿「故障语言」轴分（结构不同，不只是换色）：
 *   A · CRT 扫描线  —— 荧光屏/行扫描/磷光余晖；构图是"屏幕里的房间"
 *   B · 数字切片    —— datamosh/块状撕裂/RGB 分离；构图是"被撕开的画布"
 *   C · 数据损坏    —— 字符乱码/像素化/静态噪点；构图是"终端与损毁的档案"
 *
 * 切换：?variant=A|B|C，底部悬浮条可点箭头 / 按 ←→ 循环。
 * 状态：全在内存，无持久化。
 *
 * 动效规格（沿用 wayfinder #6 已闭合的决定）：
 *   统一缓动 cubic-bezier(.22,.61,.36,1)；时长三档
 *   微交互 140–180ms / 组件 200–280ms / 大幅 morph 400–460ms。
 *   只动 transform / opacity / clip-path（合成器友好，不触发布局）。
 *   prefers-reduced-motion 下全部判死（CSS 与 WAAPI 双侧都要守）。
 */
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'

const HERE = dirname(fileURLToPath(import.meta.url))
const TMP = join(tmpdir(), 'pr-research')

// ---------- 字体：复用调研代理已实测的子集产物 ----------
const FONTS = [
  {
    key: 'ui',
    src: join(TMP, 'final', 'ChillDuanSans_Black__B_src+gb1.woff2'),
    family: 'ChillDuanSans',
    expect: 651124,
  },
  {
    key: 'glitch',
    src: join(TMP, 'final', 'MaokenGlitchSans__A_src_only.woff2'),
    family: 'MaokenGlitchSans',
    expect: 820092,
  },
]

function embedFont(f) {
  if (!existsSync(f.src)) {
    throw new Error(
      `缺少字体子集：${f.src}\n` +
        `请先按 docs/reports/2026-10-14-resource-fonts-icons.md §6.1 生成，` +
        `或从 %TEMP%\\pr-research\\final\\ 取现成产物。`,
    )
  }
  const buf = readFileSync(f.src)
  console.log(`  字体 ${f.family}: ${buf.length} B（报告期望 ${f.expect} B）`)
  return { family: f.family, data: buf.toString('base64'), bytes: buf.length }
}

// ---------- 图标：从 E:\Resource 内联为 data URI（保持单文件可双击） ----------
const ICON_ROOT = 'E:\\Resource\\icon'
const ICON_DIRS = [join(ICON_ROOT, 'icons'), join(ICON_ROOT, '570+图标-v1.0.3')]

// 原型用到的图标：ASCII 名 → 相对路径（570+ 的中文名在此映射为 ASCII）
const ICONS = {
  menu: 'icons/menu.svg',
  home: 'icons/home.svg',
  video: 'icons/video.svg',
  upload: 'icons/upload.svg',
  download: 'icons/download.svg',
  login: 'icons/login.svg',
  logout: 'icons/logout.svg',
  copy: 'icons/copy.svg',
  share: 'icons/share.svg',
  settings: 'icons/settings.svg',
  'alert-triangle': 'icons/alert-triangle.svg',
  'users-group': 'icons/users-group.svg',
  chat: 'icons/chat.svg',
  star: 'icons/star.svg',
  ticket: 'icons/ticket.svg',
  close: 'icons/close.svg',
  refresh: 'icons/refresh.svg',
  tips: 'icons/tips.svg',
  play: '570+/2.媒体与科技/播放_svg.svg',
  pause: '570+/2.媒体与科技/暂停_svg.svg',
  live: '570+/2.媒体与科技/直播_svg.svg',
  msg: '570+/2.媒体与科技/消息_svg.svg',
  night: '570+/7.自然与饮食/夜晚_svg.svg',
  meteor: '570+/7.自然与饮食/流星_svg.svg',
  'star-fill': '570+/7.自然与饮食/星星_svg.svg',
  user: '570+/1.用户界面/用户_svg.svg',
  cross: '570+/1.用户界面/叉号_svg.svg',
  house: '570+/5.游戏/房子_svg.svg',
}

function loadIcons() {
  const out = {}
  for (const [name, rel] of Object.entries(ICONS)) {
    const abs = rel.startsWith('570+/')
      ? join(ICON_DIRS[1], rel.slice('570+/'.length))
      : join(ICON_DIRS[0], rel.slice('icons/'.length))
    if (!existsSync(abs)) throw new Error(`缺少图标：${name} -> ${abs}`)
    out[name] = readFileSync(abs, 'utf8')
  }
  console.log(`  图标：${Object.keys(out).length} 个已内联`)
  return out
}

const fonts = FONTS.map(embedFont)
const icons = loadIcons()

// 图标以 CSS mask 渲染（与生产 BrandIcon.vue 同机制：读 alpha 通道，currentColor 上色）
const iconCss = Object.entries(icons)
  .map(([name, svg]) => {
    const uri = `url("data:image/svg+xml;base64,${Buffer.from(svg, 'utf8').toString('base64')}")`
    return `.i-${name}{--i:${uri}}`
  })
  .join('\n')

const fontCss = fonts
  .map(
    (f) => `@font-face{
  font-family:'${f.family}';
  src:url("data:font/woff2;base64,${f.data}") format('woff2');
  font-display:block;
  font-weight:400 900;
}`,
  )
  .join('\n')

const html = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>月喵 · 月夜故障 视觉方向原型（PROTOTYPE — 抛弃物）</title>
<style>
/* ============================================================
   月喵「月夜故障」视觉方向原型 —— throwaway，不进 client/src
   切换：?variant=A|B|C（底部悬浮条 / ←→ 键）
   三个变体沿「故障语言」轴分：A CRT 扫描线 / B 数字切片 / C 数据损坏
   ============================================================ */
${fontCss}

${iconCss}

:root{
  --radius:4px; --gap:14px;
  --bg:#07090f; --surface:#0d1220; --line:rgba(120,200,255,.18);
  --text:#d8f2ff; --dim:#6f8ba3; --accent:#39d0ff; --glitch:#ff3d6e;
  color-scheme:dark;

  /* 动效规格（三档时长 + 统一缓动） */
  --ease:cubic-bezier(.22,.61,.36,1);
  --dur-micro:160ms;
  --dur-comp:240ms;
  --dur-morph:440ms;
}
*{box-sizing:border-box}
html,body{margin:0;height:100%}
body{
  background:var(--bg); color:var(--text);
  font-family:'ChillDuanSans',-apple-system,'Segoe UI','Microsoft YaHei',sans-serif;
  font-size:14px; line-height:1.6;
  overflow-x:hidden;
}
button{font:inherit;color:inherit;background:none;border:0;cursor:pointer}

/* ---- 图标（与生产 BrandIcon 同机制：mask + currentColor） ---- */
[class^="i-"]::before{
  content:''; display:inline-block; width:1.15em; height:1.15em;
  vertical-align:-.18em; background:currentColor;
  -webkit-mask:var(--i) center/contain no-repeat;
  mask:var(--i) center/contain no-repeat;
}
.icon-btn{
  display:inline-flex; align-items:center; justify-content:center;
  width:36px; height:36px; border-radius:999px;
  border:1px solid transparent; color:var(--dim);
  transition:color var(--dur-micro) var(--ease),
             border-color var(--dur-micro) var(--ease),
             transform var(--dur-micro) var(--ease),
             background var(--dur-micro) var(--ease);
}
.icon-btn:hover{color:var(--text); border-color:var(--line)}
.icon-btn:active{transform:scale(.9)}
.icon-btn:focus-visible{outline:2px solid var(--accent); outline-offset:2px}

/* ---- 共用：页面骨架 ---- */
.screen{min-height:100vh; display:flex; flex-direction:column}
.wrap{width:min(1080px,100%); margin:0 auto; padding:20px}
.dim{color:var(--dim)}
.mono{font-family:'Cascadia Mono',Consolas,monospace}

/* ============================================================
   变体 A · CRT 扫描线
   构图：整页是一块荧光屏；标题被行扫描切过；按钮是屏幕上的「开机」
   ============================================================ */
[data-v="A"] .screen{position:relative}
[data-v="A"] .crt-scan{
  position:fixed; inset:0; pointer-events:none; z-index:5;
  background:repeating-linear-gradient(
    to bottom,
    rgba(0,0,0,0) 0 2px,
    rgba(0,0,0,.35) 2px 3px
  );
  mix-blend-mode:multiply;
}
[data-v="A"] .crt-sweep{
  position:fixed; left:0; top:0; right:0; height:22vh; pointer-events:none; z-index:6;
  background:linear-gradient(to bottom, transparent, rgba(57,208,255,.10), transparent);
  /* 用 transform 而非 top：不触发布局，走合成器 */
  animation:sweep 6s linear infinite;
}
@keyframes sweep{
  from{transform:translateY(-25vh)}
  to{transform:translateY(110vh)}
}
[data-v="A"] .crt-glow{
  position:fixed; inset:0; pointer-events:none; z-index:4;
  background:radial-gradient(120% 90% at 50% 40%, rgba(57,208,255,.07), transparent 70%);
}
[data-v="A"] .hero{
  flex:1; display:flex; flex-direction:column; align-items:center;
  justify-content:center; gap:28px; text-align:center; padding:40px 20px;
}
/* 字体分工：A 用主 UI 字体（CRT 的"脏"来自屏幕扫描，不是字体本身） */
[data-v="A"] .brand{
  font-family:'ChillDuanSans',sans-serif;
  font-size:clamp(64px,15vw,140px); line-height:1; letter-spacing:.08em;
  position:relative; text-shadow:0 0 24px rgba(57,208,255,.45);
}
/* 行扫描切过标题：三层错位（父级是主色层，两个伪元素是错位色层，各占不同高度区间） */
[data-v="A"] .brand::before,
[data-v="A"] .brand::after{
  content:attr(data-text); position:absolute; inset:0;
  pointer-events:none;
}
[data-v="A"] .brand::before{
  color:var(--glitch); clip-path:inset(0 0 66% 0); opacity:.9;
  animation:crtA 3.4s steps(2,end) infinite;
}
[data-v="A"] .brand::after{
  color:var(--accent); clip-path:inset(64% 0 0 0); opacity:.9;
  animation:crtB 4.1s steps(2,end) infinite;
}
@keyframes crtA{0%,92%{transform:translateX(-3px)}94%{transform:translateX(-11px) translateY(2px)}96%,100%{transform:translateX(-3px)}}
@keyframes crtB{0%,88%{transform:translateX(3px)}91%{transform:translateX(9px) translateY(-2px)}94%,100%{transform:translateX(3px)}}
[data-v="A"] .start{
  padding:16px 54px; border:1px solid var(--accent); border-radius:var(--radius);
  color:var(--accent); font-size:19px; letter-spacing:.22em; background:rgba(57,208,255,.06);
  box-shadow:0 0 0 1px rgba(57,208,255,.15), 0 0 30px rgba(57,208,255,.18) inset;
  transition:box-shadow var(--dur-comp) var(--ease),
             background var(--dur-comp) var(--ease),
             letter-spacing var(--dur-comp) var(--ease),
             transform var(--dur-micro) var(--ease);
}
[data-v="A"] .start:hover{background:rgba(57,208,255,.14); box-shadow:0 0 0 1px var(--accent), 0 0 40px rgba(57,208,255,.3) inset}
[data-v="A"] .start:active{transform:scale(.97)}
[data-v="A"] .foot{display:flex; gap:14px; justify-content:center; align-items:center; font-size:12px}
[data-v="A"] .topbar{display:flex; justify-content:flex-end; gap:6px; padding:14px 18px}

/* ============================================================
   变体 B · 数字切片
   构图：整页被撕成横向条带；标题块状撕裂；按钮是撕裂的斜切
   ============================================================ */
[data-v="B"] .screen{position:relative; background:
  linear-gradient(180deg, var(--bg), #12091e 60%, var(--bg))}
[data-v="B"] .slice-veil{
  position:fixed; inset:0; pointer-events:none; z-index:5; opacity:.55;
  background:repeating-linear-gradient(
    90deg, transparent 0 60px, rgba(180,108,255,.05) 60px 62px
  );
}
[data-v="B"] .hero{
  flex:1; display:flex; flex-direction:column; align-items:center;
  justify-content:center; gap:32px; text-align:center; padding:40px 20px;
}
/* 字体分工：B 用故障黑体本体（字体本身就是故障美学）+ 三片横向撕裂叠加 */
[data-v="B"] .brand{
  font-family:'MaokenGlitchSans',sans-serif;
  font-size:clamp(60px,14vw,132px); line-height:1; letter-spacing:.04em;
  position:relative; color:var(--text);
}
/* 块状撕裂：三片横向错位 + RGB 分离（父级文字只占位/供可访问名，视觉靠切片） */
[data-v="B"] .brand .sr{visibility:hidden}
[data-v="B"] .brand .sl{
  position:absolute; left:0; top:0; width:100%;
  font-family:inherit; font-size:inherit; letter-spacing:inherit;
  visibility:visible;
}
[data-v="B"] .brand .sl1{clip-path:inset(0 0 66% 0); color:var(--accent);  animation:slB1 2.6s steps(3,end) infinite}
[data-v="B"] .brand .sl2{clip-path:inset(33% 0 33% 0); color:#ff6ec7;      animation:slB2 3.1s steps(2,end) infinite}
[data-v="B"] .brand .sl3{clip-path:inset(66% 0 0 0); color:var(--glitch); animation:slB3 3.7s steps(3,end) infinite}
@keyframes slB1{0%,80%{transform:none;opacity:.9}84%{transform:translateX(-18px);opacity:1}88%,100%{transform:none;opacity:.9}}
@keyframes slB2{0%,86%{transform:none;opacity:.85}90%{transform:translateX(14px)}94%,100%{transform:none;opacity:.85}}
@keyframes slB3{0%,90%{transform:none;opacity:.95}93%{transform:translateX(-9px) skewX(-6deg)}96%,100%{transform:none;opacity:.95}}
/* 标题入场：三片从不同方向"拼合"（只动 transform + opacity） */
@keyframes asmIn1{from{transform:translateX(-70px);opacity:0}to{transform:none;opacity:.9}}
@keyframes asmIn2{from{transform:translateX(70px);opacity:0} to{transform:none;opacity:.85}}
@keyframes asmIn3{from{transform:translateY(30px);opacity:0} to{transform:none;opacity:.95}}
[data-v="B"] .brand.assembling .sl1{animation:asmIn1 460ms var(--ease) both, slB1 2.6s steps(3,end) 460ms infinite}
[data-v="B"] .brand.assembling .sl2{animation:asmIn2 460ms var(--ease) 80ms both, slB2 3.1s steps(2,end) 540ms infinite}
[data-v="B"] .brand.assembling .sl3{animation:asmIn3 460ms var(--ease) 160ms both, slB3 3.7s steps(3,end) 620ms infinite}
/* 矩形按钮：直角实心；故障感来自 RGB 分离 + 掠过的亮带，不靠斜切 */
[data-v="B"] .start{
  position:relative; overflow:hidden;
  padding:15px 52px; font-size:19px; letter-spacing:.16em; font-weight:700;
  color:#12091e; background:var(--accent); border-radius:0;
  transition:transform var(--dur-comp) var(--ease),
             background var(--dur-comp) var(--ease),
             box-shadow var(--dur-comp) var(--ease),
             letter-spacing var(--dur-comp) var(--ease);
}
[data-v="B"] .start:hover{
  background:#c98dff; letter-spacing:.2em;
  box-shadow:5px 0 0 var(--glitch), -5px 0 0 var(--accent),
             0 10px 30px rgba(180,108,255,.35);
}
[data-v="B"] .start:active{transform:scale(.97)}
/* 掠过的亮带：悬停时横向扫过（只动 transform） */
[data-v="B"] .start::before{
  content:''; position:absolute; inset:0; pointer-events:none;
  background:linear-gradient(100deg, transparent 30%, rgba(255,255,255,.55) 50%, transparent 70%);
  transform:translateX(-120%);
}
[data-v="B"] .start:hover::before{animation:sheen 720ms var(--ease)}
@keyframes sheen{from{transform:translateX(-120%)}to{transform:translateX(120%)}}
[data-v="B"] .topbar{display:flex; justify-content:flex-end; gap:6px; padding:14px 18px}
[data-v="B"] .foot{display:flex; gap:14px; justify-content:center; font-size:12px}
/* 图标悬停抖动（故障语言：位置不稳） */
[data-v="B"] .icon-btn:hover{animation:iconJit 260ms steps(2,end)}
@keyframes iconJit{
  0%{transform:translateX(0)}
  33%{transform:translateX(-2px)}
  66%{transform:translateX(2px)}
  100%{transform:translateX(0)}
}

/* ============================================================
   变体 C · 数据损坏
   构图：终端式左对齐、等宽、直角；标题是乱码字符流；按钮是命令行提示
   ============================================================ */
[data-v="C"] .screen{background:#0c0c0c}
[data-v="C"] .noise{
  position:fixed; inset:-8px; pointer-events:none; z-index:5; opacity:.05;
  background-image:
    radial-gradient(rgba(255,255,255,.9) .5px, transparent .5px),
    radial-gradient(rgba(255,255,255,.6) .5px, transparent .5px);
  background-size:3px 3px, 5px 5px;
  background-position:0 0, 1px 2px;
  /* 用 transform 抖动而非 background-position：不触发重绘 */
  animation:noiseShift .28s steps(4,end) infinite;
}
@keyframes noiseShift{
  0%{transform:translate(0,0)}
  25%{transform:translate(-2px,1px)}
  50%{transform:translate(1px,-2px)}
  75%{transform:translate(2px,2px)}
  100%{transform:translate(0,0)}
}
[data-v="C"] .hero{
  flex:1; display:flex; flex-direction:column; justify-content:center;
  gap:26px; padding:40px clamp(20px,8vw,120px); text-align:left;
}
[data-v="C"] .brand{
  font-family:'MaokenGlitchSans','Cascadia Mono',Consolas,monospace;
  font-size:clamp(44px,9vw,96px); line-height:1.1; letter-spacing:.02em;
  position:relative; word-break:break-all;
}
[data-v="C"] .brand .caret{
  display:inline-block; width:.55em; height:1em; background:var(--accent);
  vertical-align:-.12em; animation:blink 1s steps(1,end) infinite;
}
@keyframes blink{0%,49%{opacity:1}50%,100%{opacity:0}}
[data-v="C"] .corrupt{
  color:var(--glitch); animation:corrupt 1.9s steps(1,end) infinite;
}
@keyframes corrupt{
  0%,70%{opacity:1}
  72%{opacity:.2}
  74%{opacity:1}
  86%{transform:translateX(2px)}
  88%,100%{transform:none}
}
[data-v="C"] .start{
  align-self:flex-start; padding:14px 26px; border:1px solid var(--accent);
  color:var(--accent); font-family:'Cascadia Mono',Consolas,monospace;
  font-size:16px; letter-spacing:.06em; background:transparent;
  transition:background var(--dur-comp) var(--ease), color var(--dur-comp) var(--ease),
             transform var(--dur-micro) var(--ease);
}
[data-v="C"] .start::before{content:'> '}
[data-v="C"] .start:hover{background:var(--accent); color:#0c0c0c}
[data-v="C"] .start:active{transform:scale(.98)}
[data-v="C"] .topbar{display:flex; justify-content:flex-start; gap:6px; padding:14px 18px}
[data-v="C"] .foot{display:flex; gap:14px; font-size:12px; font-family:'Cascadia Mono',Consolas,monospace}

/* ---- 登录窗（三变体共用结构，外观各自不同） ---- */
.win{
  width:min(440px,92vw); padding:18px; border:1px solid var(--line);
  border-radius:var(--radius); background:var(--surface);
  will-change:transform, opacity;
}

/* 变体 B 的登录窗：撕裂边框 + 顶部 RGB 分离条 + 故障化的页签与按钮 */
[data-v="B"] .win{
  position:relative; border:0; border-radius:0;
  background:linear-gradient(180deg, rgba(180,108,255,.10), rgba(0,0,0,.45));
  box-shadow:0 0 0 1px var(--line), 0 24px 60px rgba(0,0,0,.6);
  padding:22px 22px 20px;
}
/* 上下两条错位装饰线（RGB 分离感） */
[data-v="B"] .win::before,
[data-v="B"] .win::after{
  content:''; position:absolute; left:0; right:0; height:3px; pointer-events:none;
}
[data-v="B"] .win::before{top:0; background:var(--accent); transform:translateX(-5px)}
[data-v="B"] .win::after{bottom:0; background:var(--glitch); transform:translateX(5px)}
/* 四角的直角标记：把"窗口"框出来 */
[data-v="B"] .win .corner{
  position:absolute; width:14px; height:14px; border:2px solid var(--accent);
}
[data-v="B"] .win .c-tl{top:-1px; left:-1px; border-right:0; border-bottom:0}
[data-v="B"] .win .c-tr{top:-1px; right:-1px; border-left:0; border-bottom:0}
[data-v="B"] .win .c-bl{bottom:-1px; left:-1px; border-right:0; border-top:0}
[data-v="B"] .win .c-br{bottom:-1px; right:-1px; border-left:0; border-top:0}
/* 标题：故障黑体 + 错位重影 */
[data-v="B"] .win h3{
  font-family:'MaokenGlitchSans',sans-serif;
  font-size:22px; letter-spacing:.06em; position:relative;
  margin:0 0 4px;
}
[data-v="B"] .win .sub{
  font-size:12px; color:var(--dim); margin:0 0 16px;
  font-family:'Cascadia Mono',Consolas,monospace;
}
/* 页签：登录 / 注册 / 找回 */
[data-v="B"] .tabs{display:flex; gap:2px; margin-bottom:16px}
[data-v="B"] .tabs button{
  flex:1; padding:8px 4px; font-size:13px; color:var(--dim);
  border-bottom:2px solid transparent;
  transition:color var(--dur-micro) var(--ease), border-color var(--dur-micro) var(--ease);
}
[data-v="B"] .tabs button[aria-selected="true"]{color:var(--text); border-bottom-color:var(--accent)}
[data-v="B"] .tabs button:hover{color:var(--text)}
/* 输入框：直角 + 聚焦时 RGB 分离的描边 */
[data-v="B"] .win input{border-radius:0; background:rgba(0,0,0,.5); border-color:rgba(180,108,255,.25)}
[data-v="B"] .win input:focus{
  border-color:var(--accent);
  box-shadow:2px 0 0 var(--glitch), -2px 0 0 var(--accent);
}
[data-v="B"] .win .primary{border-radius:0; position:relative; overflow:hidden}
[data-v="B"] .win .ghost{border-radius:0}
/* 分割线：一条会呼吸的故障线 */
[data-v="B"] .win .rule{
  height:1px; margin:16px 0 12px; background:var(--line);
  animation:ruleGlitch 3.6s steps(3,end) infinite;
}
@keyframes ruleGlitch{
  0%,88%{transform:none; opacity:1}
  90%{transform:translateX(-4px) scaleY(2); opacity:.6}
  93%,100%{transform:none; opacity:1}
}
.win h3{margin:0 0 14px; font-size:16px; display:flex; align-items:center; gap:8px}
.win label{display:block; font-size:12px; color:var(--dim); margin:10px 0 5px}
.win input{
  width:100%; padding:10px; color:var(--text); background:rgba(0,0,0,.35);
  border:1px solid var(--line); border-radius:var(--radius); font:inherit;
  transition:border-color var(--dur-micro) var(--ease),
             box-shadow var(--dur-micro) var(--ease),
             background var(--dur-micro) var(--ease);
}
.win input:focus{outline:none; border-color:var(--accent); box-shadow:0 0 0 3px rgba(57,208,255,.12)}
.win .row{display:flex; gap:10px; margin-top:16px}
.win .primary{
  flex:1; padding:11px; background:var(--accent); color:#0a0a0a;
  border-radius:var(--radius); font-weight:700;
  transition:transform var(--dur-micro) var(--ease), filter var(--dur-micro) var(--ease);
}
.win .primary:hover{filter:brightness(1.1)}
.win .primary:active{transform:scale(.98)}
.win .ghost{
  padding:11px 16px; border:1px solid var(--line); border-radius:var(--radius); color:var(--dim);
  transition:color var(--dur-micro) var(--ease), border-color var(--dur-micro) var(--ease),
             transform var(--dur-micro) var(--ease);
}
.win .ghost:hover{color:var(--text); border-color:var(--accent)}
.win .ghost:active{transform:scale(.98)}

/* ---- 外壳（登录后） ---- */
.shell{display:flex; flex-direction:column; min-height:100vh}
.shell-bar{
  display:flex; align-items:center; gap:10px; padding:12px 18px;
  border-bottom:1px solid var(--line);
}
.shell-bar .brand-sm{font-size:20px; letter-spacing:.1em; margin-right:auto}
/* 变体 B 的外壳：品牌名用故障黑体 + 顶栏一条会滑过的故障线 */
[data-v="B"] .shell-bar{position:relative; overflow:hidden}
[data-v="B"] .shell-bar .brand-sm{
  font-family:'MaokenGlitchSans',sans-serif; font-size:24px;
}
[data-v="B"] .shell-bar::after{
  content:''; position:absolute; left:0; bottom:0; width:100%; height:2px;
  background:linear-gradient(90deg, transparent, var(--accent), var(--glitch), transparent);
  animation:barSlide 5s linear infinite;
}
@keyframes barSlide{
  0%{transform:translateX(-100%)}
  100%{transform:translateX(100%)}
}
.shell-body{flex:1; display:grid; grid-template-columns:1fr 1fr; gap:var(--gap); padding:20px}
.card{
  border:1px solid var(--line); border-radius:var(--radius);
  background:var(--surface); padding:16px;
  transition:border-color var(--dur-comp) var(--ease), transform var(--dur-comp) var(--ease),
             box-shadow var(--dur-comp) var(--ease);
}
[data-v="B"] .card{border-radius:0; position:relative; overflow:hidden}
/* 卡片悬停：RGB 分离的边框 + 右上角故障角标展开 */
[data-v="B"] .card:hover{
  border-color:var(--accent); transform:translateY(-3px);
  box-shadow:4px 0 0 rgba(61,255,168,.5), -4px 0 0 rgba(180,108,255,.5),
             0 14px 34px rgba(0,0,0,.45);
}
[data-v="B"] .card::after{
  content:''; position:absolute; top:0; right:0; width:0; height:0;
  border-top:22px solid var(--accent); border-left:22px solid transparent;
  opacity:.25; transition:opacity var(--dur-comp) var(--ease);
}
[data-v="B"] .card:hover::after{opacity:.7}
.card h4{margin:0 0 10px; font-size:15px}

/* ---- 首页：等待加入的占位页 ---- */
.waiting{
  flex:1; display:flex; flex-direction:column; align-items:center;
  justify-content:center; gap:18px; text-align:center;
  padding:40px 24px 80px;
}
.wait-badge{
  display:inline-flex; align-items:center; gap:8px;
  padding:8px 18px; border:1px solid var(--line); border-radius:var(--radius);
  font-size:14px; letter-spacing:.14em; color:var(--text);
  background:rgba(255,255,255,.03);
}
[data-v="B"] .wait-badge{border-radius:0; position:relative}
/* 等待态：徽标外一圈缓慢扩散的环 */
[data-v="B"] .wait-badge::after{
  content:''; position:absolute; inset:0; pointer-events:none;
  border:1px solid var(--accent);
  animation:waitRing 2.8s var(--ease) infinite;
}
@keyframes waitRing{
  0%{transform:scale(1); opacity:.5}
  70%{transform:scale(1.35); opacity:0}
  100%{transform:scale(1.35); opacity:0}
}
/* 三点呼吸：依次亮起，表示"等着的" */
.wait-dots{display:flex; gap:8px}
.wait-dots .dot{
  width:6px; height:6px; background:var(--accent); opacity:.25;
  animation:dotPulse 1.4s var(--ease) infinite;
}
[data-v="B"] .wait-dots .dot{border-radius:0}
[data-v="B"] .wait-dots .dot:nth-child(2){animation-delay:.18s}
[data-v="B"] .wait-dots .dot:nth-child(3){animation-delay:.36s}
@keyframes dotPulse{
  0%,100%{opacity:.25; transform:scale(1)}
  50%{opacity:1; transform:scale(1.5)}
}
.wait-msg{margin:0; max-width:34ch; color:var(--dim); font-size:13px; line-height:1.8}
.wait-act{display:flex; gap:12px; margin-top:6px}
.wait-act .start{padding:13px 40px; font-size:17px}
.wait-act .ghost{
  padding:13px 24px; border:1px solid var(--line); color:var(--dim); border-radius:var(--radius);
  transition:color var(--dur-micro) var(--ease), border-color var(--dur-micro) var(--ease),
             transform var(--dur-micro) var(--ease);
}
[data-v="B"] .wait-act .ghost{border-radius:0}
.wait-act .ghost:hover{color:var(--text); border-color:var(--accent)}
.wait-act .ghost:active{transform:scale(.98)}
.fab{
  position:fixed; right:22px; bottom:22px; width:54px; height:54px;
  border-radius:999px; background:var(--accent); color:#0a0a0a;
  display:flex; align-items:center; justify-content:center; font-size:20px;
  box-shadow:0 8px 26px rgba(0,0,0,.5);
  transition:transform var(--dur-comp) var(--ease), filter var(--dur-comp) var(--ease),
             box-shadow var(--dur-comp) var(--ease);
}
.fab:hover{transform:scale(1.08) rotate(6deg); filter:brightness(1.08);
  box-shadow:0 10px 34px rgba(180,108,255,.45)}
.fab:active{transform:scale(.94)}
/* FAB 常驻呼吸：一圈缓慢扩散的故障环 */
[data-v="B"] .fab::before{
  content:''; position:absolute; inset:0; border-radius:999px;
  border:2px solid var(--accent); pointer-events:none;
  animation:fabPulse 2.6s var(--ease) infinite;
}
@keyframes fabPulse{
  0%{transform:scale(1); opacity:.6}
  70%{transform:scale(1.55); opacity:0}
  100%{transform:scale(1.55); opacity:0}
}
[data-v="B"] .fab{position:fixed}
@media (max-width:760px){ .shell-body{grid-template-columns:1fr} }

/* ---- 帖子卡片 ---- */
.posts{display:grid; grid-template-columns:repeat(auto-fill,minmax(240px,1fr)); gap:var(--gap)}
.post{
  border:1px solid var(--line); border-radius:var(--radius); background:var(--surface); overflow:hidden;
  transition:border-color var(--dur-comp) var(--ease), transform var(--dur-comp) var(--ease),
             box-shadow var(--dur-comp) var(--ease);
}
.post:hover{border-color:var(--accent); transform:translateY(-3px)}
[data-v="B"] .post{border-radius:0}
[data-v="B"] .post:hover{
  box-shadow:5px 0 0 rgba(180,108,255,.45), -5px 0 0 rgba(61,255,168,.45),
             0 16px 36px rgba(0,0,0,.45);
}
.post .thumb{height:110px; background:linear-gradient(135deg,rgba(255,255,255,.06),transparent); position:relative}
/* 缩略图上的扫描线：悬停时自上而下扫过 */
[data-v="B"] .post .thumb::after{
  content:''; position:absolute; inset:0;
  background:linear-gradient(180deg, transparent, rgba(180,108,255,.35), transparent);
  transform:translateY(-100%);
}
[data-v="B"] .post:hover .thumb::after{animation:thumbScan 700ms var(--ease)}
@keyframes thumbScan{from{transform:translateY(-100%)}to{transform:translateY(100%)}}
/* 标题悬停时的故障抖字 */
[data-v="B"] .post:hover h5{animation:titleJit 420ms steps(2,end)}
@keyframes titleJit{
  0%{transform:translateX(0)}
  25%{transform:translateX(-2px) skewX(-3deg)}
  50%{transform:translateX(2px)}
  75%{transform:translateX(-1px) skewX(2deg)}
  100%{transform:translateX(0)}
}
.post .body{padding:12px}
.post h5{margin:0 0 6px; font-size:14px}
.post p{margin:0; font-size:12px; color:var(--dim)}

/* ============================================================
   过渡：屏幕进入 / 变体切换
   只动 transform / opacity / clip-path
   ============================================================ */
.screen{will-change:opacity}

/* 变体切换的「故障擦除」覆盖层 */
.wipe{position:fixed; inset:0; z-index:90; pointer-events:none; overflow:hidden}
.wipe-crt{
  position:absolute; inset:0; background:var(--bg);
  box-shadow:0 0 40px rgba(57,208,255,.5) inset;
}
.wipe-bar{
  position:absolute; left:0; width:100%; height:12.5%; background:var(--bg);
}
.wipe-scan{
  position:absolute; left:0; width:100%; height:100%;
  background:linear-gradient(to bottom, transparent, var(--bg) 40%, var(--bg) 60%, transparent);
}
.wipe-noise{
  position:absolute; inset:0; opacity:.35;
  background-image:
    radial-gradient(rgba(255,255,255,.8) .5px, transparent .5px),
    radial-gradient(rgba(255,255,255,.5) .5px, transparent .5px);
  background-size:4px 4px, 7px 7px;
}

/* ============================================================
   底部悬浮切换条 —— 刻意高对比，明显不属于被评审的设计
   ============================================================ */
.proto-bar{
  position:fixed; left:50%; bottom:18px; transform:translateX(-50%);
  display:flex; align-items:center; gap:12px; z-index:100;
  padding:8px 14px; border-radius:999px;
  background:#f4f4f5; color:#111; font-family:-apple-system,'Segoe UI',sans-serif;
  font-size:13px; box-shadow:0 8px 30px rgba(0,0,0,.55);
}
.proto-bar button{
  color:#111; font-size:16px; padding:0 6px; line-height:1;
  transition:transform var(--dur-micro) var(--ease);
}
.proto-bar button:hover{transform:scale(1.25)}
.proto-bar button:active{transform:scale(.85)}
.proto-bar .lab{font-weight:700; white-space:nowrap}
.proto-bar .key{font-family:'Cascadia Mono',Consolas,monospace; background:#111; color:#f4f4f5; padding:1px 6px; border-radius:4px}
.proto-note{
  position:fixed; left:50%; bottom:64px; transform:translateX(-50%);
  z-index:100; font-size:11px; color:#111; background:#f4f4f5cc;
  padding:3px 10px; border-radius:999px; white-space:nowrap;
}

/* 字体分工一览（放在切换条上方，供评审对照） */
.proto-fonts{
  position:fixed; right:18px; bottom:18px; z-index:100;
  font-family:-apple-system,'Segoe UI',sans-serif; font-size:11px;
  background:#f4f4f5; color:#111; padding:8px 11px; border-radius:10px;
  box-shadow:0 8px 30px rgba(0,0,0,.55); line-height:1.7; max-width:230px;
}
.proto-fonts b{font-family:'MaokenGlitchSans',sans-serif; font-size:13px}
.proto-fonts i{font-style:normal; font-family:'ChillDuanSans',sans-serif; font-size:13px}
@media (max-width:820px){ .proto-fonts{display:none} }

/* ---- 降级：prefers-reduced-motion 下故障动画一律判死 ---- */
/* 注意：伪元素必须显式列出 —— 通配选择器选不中 ::before / ::after */
@media (prefers-reduced-motion: reduce){
  [data-v] *,
  [data-v] *::before,
  [data-v] *::after{animation:none !important; transition:none !important}
  [data-v="A"] .crt-sweep,
  [data-v="C"] .noise{display:none !important}
}
/* ?motion=off 可强制降级，便于评审直接对照（无需改系统设置） */
html[data-motion="off"] *,
html[data-motion="off"] *::before,
html[data-motion="off"] *::after{animation:none !important; transition:none !important}
html[data-motion="off"] .crt-sweep,
html[data-motion="off"] .noise{display:none !important}
</style>
</head>
<body>
<div id="app"></div>

<script>
/* ============================================================
   原型逻辑：三变体切换 + 内存态 + 动效。无持久化、无网络。
   动效只动 transform / opacity / clip-path；reduced-motion 双侧判死。
   ============================================================ */
const V = {
  A: { label:'CRT 扫描线', name:'VariantA', blurb:'荧光屏里的房间：行扫描、磷光余晖、开机式入场' },
  B: { label:'数字切片',   name:'VariantB', blurb:'被撕开的画布：块状撕裂、RGB 分离、datamosh' },
  C: { label:'数据损坏',   name:'VariantC', blurb:'终端与损毁的档案：字符乱码、像素化、静态噪点' }
};
const KEYS = ['A','B','C'];

const EASE = 'cubic-bezier(.22,.61,.36,1)';
const DUR = { micro:160, comp:240, morph:440 };
// 降级：系统偏好，或 ?motion=off 强制（便于评审对照）
const MOTION_OFF = /[?&]motion=off/.test(location.search);
const REDUCED = MOTION_OFF || matchMedia('(prefers-reduced-motion: reduce)').matches;
if (MOTION_OFF) document.documentElement.dataset.motion = 'off';

const state = { variant:'B', stage:'landing', memo:'' };
const NOISE_CHARS = '▓▒░█▚▞◤◥⌁⌇⟊⍟⎔◈※§¶';

/* ---------- 动效基元 ---------- */
// WAAPI 不自动尊重 reduced-motion，必须在 JS 侧也判死。
function anim(el, keyframes, opts){
  if (!el) return null;
  if (REDUCED) return null;
  const o = Object.assign({ easing: EASE, fill:'both' }, opts);
  if (o.duration == null) o.duration = DUR.comp;
  return el.animate(keyframes, o);
}
function fadeIn(el, opts){
  return anim(el, [{ opacity:0 }, { opacity:1 }], Object.assign({ duration:DUR.comp }, opts));
}
// 交错入场：只动 opacity + translateY，避免布局抖动
function stagger(nodes, base){
  const d = base == null ? 60 : base;
  [...nodes].forEach((el, i) => {
    anim(el, [
      { opacity:0, transform:'translateY(12px)' },
      { opacity:1, transform:'none' }
    ], { duration:DUR.comp, delay: d + i * 45 });
  });
}

function fromURL(){
  const m = /[?&]variant=([ABC])/.exec(location.search);
  // 默认 B：2026-10-14 已由 YG 选定 B（数字切片）为定稿方向
  return m ? m[1] : 'B';
}
function toURL(v){
  const u = new URL(location.href);
  u.searchParams.set('variant', v);
  history.replaceState(null,'',u);
}
function noise(n){
  let s = '';
  for (let i=0;i<n;i++) s += NOISE_CHARS[(Math.random()*NOISE_CHARS.length)|0];
  return s;
}
function el(tag, cls, html){
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (html != null) e.innerHTML = html;
  return e;
}
function icon(name){ return '<span class="i-'+name+'" aria-hidden="true"></span>' }
function iconBtn(name, label){
  const b = el('button','icon-btn', icon(name));
  b.title = label; b.setAttribute('aria-label', label);
  return b;
}

/* ---------- 落地页（三变体结构不同） ---------- */
function landing(v){
  const s = el('div','screen');

  if (v==='A'){
    s.append(el('div','crt-glow'), el('div','crt-scan'), el('div','crt-sweep'));
    const top = el('div','topbar');
    top.append(iconBtn('github','开源仓库'), iconBtn('star','关于作者'), iconBtn('tips','帮助'));
    s.append(top);
    const hero = el('div','hero');
    const brand = el('h1','brand');
    brand.dataset.text = '月喵';
    brand.textContent = '月喵';
    hero.append(brand);
    const b = el('button','start','开 始');
    b.onclick = ()=>goStage('auth', b);
    hero.append(b);
    hero.append(el('p','foot dim','<span>看片不需要账号 · 有房间码就能进</span>'));
    s.append(hero);
  }

  if (v==='B'){
    s.append(el('div','slice-veil'));
    const top = el('div','topbar');
    top.append(iconBtn('github','开源仓库'), iconBtn('star','关于作者'), iconBtn('tips','帮助'));
    s.append(top);
    const hero = el('div','hero');
    const brand = el('h1','brand assembling');
    brand.innerHTML =
      '<span class="sr">月喵</span>' +
      '<span class="sl sl1">月喵</span>' +
      '<span class="sl sl2">月喵</span>' +
      '<span class="sl sl3">月喵</span>';
    // 入场拼合播完后摘掉 assembling，避免与常驻撕裂动画互相覆盖
    setTimeout(()=>brand.classList.remove('assembling'), 1100);
    hero.append(brand);
    const b = el('button','start','开始');
    b.onclick = ()=>goStage('auth', b);
    hero.append(b);
    hero.append(el('p','foot dim','看片不需要账号 · 有房间码就能进'));
    s.append(hero);
  }

  if (v==='C'){
    s.append(el('div','noise'));
    const top = el('div','topbar');
    top.append(iconBtn('github','开源仓库'), iconBtn('star','关于作者'), iconBtn('tips','帮助'));
    s.append(top);
    const hero = el('div','hero');
    const brand = el('div','brand');
    brand.innerHTML = '月喵<span class="corrupt">'+noise(3)+'</span><span class="caret"></span>';
    hero.append(brand);
    hero.append(el('p','dim mono','// 本地播放 · WebRTC 同步 · 服务端不经手视频字节'));
    const b = el('button','start','开始');
    b.onclick = ()=>goStage('auth', b);
    hero.append(b);
    hero.append(el('p','foot dim','看片不需要账号 · 有房间码就能进'));
    s.append(hero);
  }

  return s;
}

/* ---------- 登录窗 ---------- */
function auth(v){
  const s = el('div','screen');
  const hero = el('div','hero');
  const w = el('div','win');

  if (v === 'B'){
    // 变体 B：撕裂窗口 —— 角标 + RGB 分离标题 + 页签 + 故障分割线
    for (const c of ['c-tl','c-tr','c-bl','c-br']) w.append(el('span','corner '+c));
    const h = el('h3');
    h.innerHTML = '登录月喵';
    w.append(h);
    w.append(el('p','sub','// 账号只用于建房与社交；看片免登录'));
    const tabs = el('div','tabs');
    for (const [t, on] of [['登录',true],['注册',false],['找回密码',false]]){
      const b = el('button',null,t);
      b.setAttribute('role','tab');
      b.setAttribute('aria-selected', on ? 'true' : 'false');
      b.onclick = ()=>{ [...tabs.children].forEach(x=>x.setAttribute('aria-selected','false')); b.setAttribute('aria-selected','true') };
      tabs.append(b);
    }
    w.append(tabs);
    w.append(el('label',null,'账号'));
    const i1 = el('input'); i1.placeholder='用户名或邮箱'; i1.value = state.memo;
    i1.oninput = ()=>{ state.memo = i1.value; };
    w.append(i1);
    w.append(el('label',null,'密码'));
    const i2 = el('input'); i2.type='password'; i2.placeholder='••••••••';
    w.append(i2);
    const row = el('div','row');
    const go = el('button','primary','登录');
    go.onclick = ()=>goStage('shell', go);
    row.append(go);
    w.append(row);
    w.append(el('div','rule'));
    const alt = el('div','row');
    const direct = el('button','ghost','直接进放映室');
    direct.onclick = ()=>goStage('shell', direct);
    const close = el('button','ghost','收起');
    close.onclick = ()=>goStage('landing', close);
    alt.append(direct, close);
    w.append(alt);
    hero.append(w);
    s.append(hero);
    return s;
  }

  const h = el('h3');
  h.innerHTML = icon('login') + ' <span>登录月喵</span>';
  const close = el('button','icon-btn', icon('close'));
  close.style.marginLeft='auto';
  close.setAttribute('aria-label','收起');
  close.onclick = ()=>goStage('landing', close);
  h.append(close);
  w.append(h);
  w.append(el('label',null,'账号'));
  const i1 = el('input'); i1.placeholder='用户名或邮箱'; i1.value = state.memo;
  i1.oninput = ()=>{ state.memo = i1.value; };
  w.append(i1);
  w.append(el('label',null,'密码'));
  const i2 = el('input'); i2.type='password'; i2.placeholder='••••••••';
  w.append(i2);
  const row = el('div','row');
  const go = el('button','primary','登录');
  go.onclick = ()=>goStage('shell', go);
  const direct = el('button','ghost','直接进放映室');
  direct.onclick = ()=>goStage('shell', direct);
  row.append(go, direct);
  w.append(row);
  hero.append(w);
  s.append(hero);
  return s;
}

/* ---------- 外壳 ---------- */
function shell(v){
  // 必须同时带 .screen：goStage 用 .screen 找当前屏并驱动入场动画
  const s = el('div','screen shell');
  const bar = el('div','shell-bar');
  bar.innerHTML = '<span class="brand-sm">月喵</span>';
  for (const [n,l] of [['house','房间室'],['users-group','好友'],['chat','论坛'],['msg','消息'],['night','公开']]){
    const b = iconBtn(n,l);
    // 论坛图标 → 帖子面（票 #10 要求覆盖的第四个面，独立于首页等待占位）
    if (n === 'chat') b.onclick = ()=>goStage('posts', b);
    bar.append(b);
  }
  const av = el('button','icon-btn', icon('user'));
  av.setAttribute('aria-label','账号');
  bar.append(av);
  s.append(bar);

  // 首页 = 等待加入房间的占位页（不放建房/加入卡片、FAB、帖子列表）
  const wait = el('div','waiting');
  const badge = el('div','wait-badge');
  badge.innerHTML = icon('live') + '<span>等待加入房间</span>';
  const dots = el('div','wait-dots');
  for (let i=0;i<3;i++) dots.append(el('span','dot'));
  const msg = el('p','wait-msg','还没有进入任何房间。拿到房间码就能进，看片不需要账号。');
  const act = el('div','wait-act');
  const join = el('button','start','加入房间');
  join.onclick = ()=>{ /* 原型：无副作用 */ };
  const create = el('button','ghost','创建房间');
  create.onclick = ()=>{ /* 原型：无副作用 */ };
  act.append(join, create);
  wait.append(badge, dots, msg, act);
  s.append(wait);
  return s;
}

function card(t, d){
  const c = el('div','card');
  c.append(el('h4',null,t), el('p','dim',d));
  return c;
}

function postsBlock(){
  const w = el('div','wrap');
  w.append(el('h4',null,'帖子'));
  const g = el('div','posts');
  for (const t of ['今晚看什么','老片重看清单','房间码分享']){
    const p = el('div','post');
    p.append(el('div','thumb'));
    const b = el('div','body');
    b.append(el('h5',null,t), el('p',null,'评论 3 · 赞 12'));
    p.append(b);
    g.append(p);
  }
  w.append(g);
  return w;
}

/* ---------- 帖子面（票 #10 要求的第四个面，独立屏） ---------- */
function postsScreen(v){
  const s = el('div','screen posts-screen');
  const bar = el('div','shell-bar');
  bar.innerHTML = '<span class="brand-sm">月喵</span>';
  const back = iconBtn('house','回到首页');
  back.onclick = ()=>goStage('shell', back);
  bar.append(back);
  const av = el('button','icon-btn', icon('user'));
  av.setAttribute('aria-label','账号');
  bar.append(av);
  s.append(bar);
  s.append(postsBlock());
  return s;
}

/* ---------- 渲染 ---------- */
function render(){
  const v = state.variant;
  document.documentElement.dataset.v = v;
  const app = document.getElementById('app');
  app.replaceChildren();

  if (state.stage==='landing') app.append(landing(v));
  else if (state.stage==='auth') app.append(auth(v));
  else if (state.stage==='posts') app.append(postsScreen(v));
  else app.append(shell(v));

  app.append(switcher());
}

/* ---------- 屏幕之间的过渡 ---------- */
// 返回真实 rect，供 morph 用（render 会重建 DOM，必须先量）
function rectOf(node){ return node ? node.getBoundingClientRect() : null }

function goStage(next, srcEl){
  if (state.stage === next) return;
  const srcRect = rectOf(srcEl);
  state.stage = next;
  render();

  const screen = document.querySelector('.screen');
  if (!screen) return;

  // 大幅 morph：按钮 → 登录窗（next==='auth' 且量到了源 rect）
  if (next === 'auth' && srcRect){
    const win = screen.querySelector('.win');
    if (win && morph(win, srcRect)) return;
  }
  enterScreen(screen, next);
}

// FLIP：把目标元素从源元素的矩形位置/尺寸长到自己的自然位置
function morph(target, srcRect){
  if (REDUCED) return false;
  const t = rectOf(target);
  if (!t || !t.width || !t.height) return false;
  const sx = srcRect.width / t.width;
  const sy = srcRect.height / t.height;
  const dx = (srcRect.left + srcRect.width/2) - (t.left + t.width/2);
  const dy = (srcRect.top + srcRect.height/2) - (t.top + t.height/2);
  const rEnd = getComputedStyle(target).borderRadius;

  target.animate([
    { transform:'translate('+dx+'px,'+dy+'px) scale('+sx+','+sy+')',
      borderRadius:'999px', opacity:.7 },
    { transform:'none', borderRadius:rEnd, opacity:1 }
  ], { duration: DUR.morph, easing: EASE, fill:'both' });

  // 窗内内容稍后淡入，避免"文字被拉伸"的观感
  stagger(target.children, 140);
  return true;
}

// 常规进入：整屏淡入 + 子元素交错上浮
function enterScreen(screen, stage){
  fadeIn(screen, { duration: DUR.comp });
  const groups = [];
  if (stage === 'landing'){
    const top = screen.querySelector('.topbar');
    if (top) groups.push(...top.children);
    const hero = screen.querySelector('.hero');
    if (hero) groups.push(...hero.children);
  } else if (stage === 'auth'){
    const win = screen.querySelector('.win');
    if (win) groups.push(win);
  } else if (stage === 'shell'){
    const bar = screen.querySelector('.shell-bar');
    if (bar) groups.push(...bar.children);
    const wait = screen.querySelector('.waiting');
    if (wait) groups.push(...wait.children);
  } else if (stage === 'posts'){
    const bar = screen.querySelector('.shell-bar');
    if (bar) groups.push(...bar.children);
    const posts = screen.querySelectorAll('.post');
    groups.push(...posts);
  }
  if (groups.length) stagger(groups, stage === 'shell' ? 80 : 40);
}

/* ---------- 变体切换的「故障擦除」 ---------- */
function buildWipe(v){
  const w = el('div','wipe');
  if (v === 'A'){
    // CRT 开关机：整屏横向压缩成一条线，再展开
    const line = el('div','wipe-crt');
    w.append(line);
    line.animate([
      { clipPath:'inset(0 0 0 0)', opacity:1 },
      { clipPath:'inset(calc(50% - 2px) 0 calc(50% - 2px) 0)', opacity:1, offset:.5 },
      { clipPath:'inset(0 0 0 0)', opacity:0 }
    ], { duration:520, easing:EASE, fill:'both' });
  } else if (v === 'B'){
    // 数字切片：八条横带依次错位滑过
    for (let i=0;i<8;i++){
      const b = el('div','wipe-bar');
      b.style.top = (i*12.5)+'%';
      w.append(b);
      b.animate([
        { transform:'translateX(-102%)' },
        { transform:'translateX(0)', offset:.5 },
        { transform:'translateX(102%)' }
      ], { duration:460, easing:EASE, delay:i*28, fill:'both' });
    }
  } else {
    // 数据损坏：自上而下的扫描覆盖 + 噪点闪
    const scan = el('div','wipe-scan');
    w.append(scan, el('div','wipe-noise'));
    scan.animate([
      { transform:'translateY(-100%)' },
      { transform:'translateY(0)', offset:.5 },
      { transform:'translateY(100%)' }
    ], { duration:520, easing:EASE, fill:'both' });
    w.querySelector('.wipe-noise').animate(
      [{ opacity:0 }, { opacity:.5, offset:.5 }, { opacity:0 }],
      { duration:520, easing:EASE, fill:'both' });
  }
  return w;
}

// 擦除到中点时换内容，让切换被"盖住"
function runWipe(swap){
  if (REDUCED){ swap(); return; }
  const w = buildWipe(state.variant);
  document.body.append(w);
  setTimeout(swap, 230);
  setTimeout(()=>w.remove(), 620);
}

/* ---------- 切换条 ---------- */
function switcher(){
  const f = document.createDocumentFragment();
  const note = el('div','proto-note', V[state.variant].blurb + ' · PROTOTYPE（抛弃物）');
  const bar = el('div','proto-bar');
  const prev = el('button',null,'‹');
  const next = el('button',null,'›');
  const lab = el('span','lab', state.variant + ' (' + V[state.variant].name + ')');
  const key = el('span','key', '?variant=' + state.variant);
  prev.setAttribute('aria-label','上一个变体');
  next.setAttribute('aria-label','下一个变体');
  prev.onclick = ()=>cycle(-1);
  next.onclick = ()=>cycle(1);
  bar.append(prev, lab, key, next);
  // 字体对照：让评审直接看到两款真字体的差别
  const ff = el('div','proto-fonts');
  ff.innerHTML =
    '<b>猫啃网故障黑</b> · 装饰/标题<br>' +
    '<i>寒蝉端黑体</i> · 正文/UI<br>' +
    '<span style="opacity:.6">本变体标题用：' +
    (state.variant==='A' ? '寒蝉端黑体' : '猫啃网故障黑') + '</span>';
  f.append(note, bar, ff);
  return f;
}

function cycle(d){
  const i = KEYS.indexOf(state.variant);
  state.variant = KEYS[(i + d + KEYS.length) % KEYS.length];
  state.stage = 'landing';
  toURL(state.variant);
  runWipe(()=>{
    render();
    const screen = document.querySelector('.screen');
    if (screen) enterScreen(screen, 'landing');
  });
}

document.addEventListener('keydown', (e)=>{
  const t = e.target;
  if (t && (t.tagName==='INPUT' || t.tagName==='TEXTAREA' || t.isContentEditable)) return;
  if (e.key==='ArrowLeft') cycle(-1);
  if (e.key==='ArrowRight') cycle(1);
});

window.addEventListener('popstate', ()=>{ state.variant = fromURL(); render(); });

state.variant = fromURL();
render();
// 首屏也走一次入场，避免"啪一下全出现"
{
  const screen = document.querySelector('.screen');
  if (screen) enterScreen(screen, state.stage);
}
</script>
</body>
</html>
`

const out = join(HERE, 'glitch-direction.html')
writeFileSync(out, html, 'utf8')
console.log(`\n产出：${out}`)
console.log(`体积：${(Buffer.byteLength(html) / 1024 / 1024).toFixed(2)} MiB`)
console.log('用法：双击打开；?variant=A|B|C 切换，或点底部条 / 按 ←→')
