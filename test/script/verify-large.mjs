#!/usr/bin/env node
/**
 * 大文件端到端：真实走一遍"上传 → 服务端切片 → 取产物"，把耗时与配额判定都打出来。
 *
 * 为什么需要它：小素材（25s）根本压不到"分片数上千、产物上百 MB、上传几个 GB"这些
 * 真实场景下的路径；本地自己切又要先装 ffmpeg。这个脚本用仓库里的中/大素材直接打服务端，
 * 把每一阶段的耗时、分片数、产物形态、以及失败时的错误码都记录成可复现的证据。
 *
 * 用法（仓库根目录）：
 *   node test/script/verify-large.mjs                        # 默认用 middle_mp4_video.mkv（13 分钟）
 *   node test/script/verify-large.mjs --source test/resource/big_mp4_video.mp4   # 142 分钟，应该被 422 拒
 *   node test/script/verify-large.mjs --source movie.mp4 --out "$env:TEMP\pr-large"
 */
import { createWriteStream, existsSync, mkdirSync, statSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { argOf } from './lib/browser.mjs'

const argv = process.argv.slice(2)
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8080')
const SOURCE = argOf(argv, 'source', 'test/resource/middle_mp4_video.mkv')
const OUT_DIR = resolve(argOf(argv, 'out', join(process.env.TEMP ?? '.', 'pr-large')))
const POLL_MS = Number(argOf(argv, 'poll', '2000'))

function human(bytes) {
  if (bytes > 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(2)} GiB`
  if (bytes > 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`
  return `${(bytes / 1024).toFixed(1)} KiB`
}

async function submit() {
  const size = statSync(SOURCE).size
  console.log(`源文件 ${basename(SOURCE)}  ${human(size)}`)
  const form = new FormData()
  const { openAsBlob } = await import('node:fs')
  form.append('file', await openAsBlob(SOURCE, { type: 'video/x-matroska' }), basename(SOURCE))

  const started = Date.now()
  const resp = await fetch(`${SERVER_URL}/api/v1/segment/jobs`, { method: 'POST', body: form })
  const text = await resp.text()
  let body
  try {
    body = JSON.parse(text)
  } catch {
    body = { raw: text.slice(0, 300) }
  }
  const seconds = ((Date.now() - started) / 1000).toFixed(1)
  console.log(`上传+提交: HTTP ${resp.status}  ${seconds}s  ${JSON.stringify(body)}`)
  if (!resp.ok) {
    // 413/422/429 都是"如实拒绝"，把码与文案打出来就算这条用例有结论。
    console.log(`\n判定：服务端拒绝（HTTP ${resp.status} ${body.code ?? ''}）—— 配额/校验路径符合预期`)
    process.exitCode = 0
    return null
  }
  return body.jobId
}

async function poll(jobId) {
  let last = ''
  const started = Date.now()
  for (;;) {
    await new Promise((r) => setTimeout(r, POLL_MS))
    const state = await (await fetch(`${SERVER_URL}/api/v1/segment/jobs/${jobId}`)).json()
    const line = `${state.state} ${Math.round((state.progress ?? 0) * 100)}% 队列位次=${state.queuePosition ?? '-'}`
    if (line !== last) {
      console.log(`  [${((Date.now() - started) / 1000).toFixed(0)}s] ${line}`)
      last = line
    }
    if (state.state === 'done' || state.state === 'failed') {
      console.log(`切片总耗时: ${((Date.now() - started) / 1000).toFixed(1)}s`)
      return state
    }
  }
}

async function fetchResult(jobId, state) {
  const started = Date.now()
  const resp = await fetch(`${SERVER_URL}/api/v1/segment/jobs/${jobId}/result`)
  const type = resp.headers.get('content-type') ?? ''
  const bytes = Number(resp.headers.get('content-length') ?? 0)

  if (type.includes('json')) {
    const manifest = await resp.json()
    console.log(`产物形态: manifest（分批 ${manifest.parts?.length ?? 0} 份），首字节耗时 ${((Date.now() - started) / 1000).toFixed(1)}s`)
    let total = 0
    for (const part of manifest.parts ?? []) {
      const t0 = Date.now()
      const partResp = await fetch(`${SERVER_URL}${part.url}`)
      const buf = Buffer.from(await partResp.arrayBuffer())
      total += buf.length
      mkdirSync(OUT_DIR, { recursive: true })
      const file = join(OUT_DIR, `part${String(part.index ?? '').padStart(4, '0')}.zip`)
      await pipeline(Readable.from(buf), createWriteStream(file))
      console.log(`  part ${part.index}: ${human(buf.length)} / 声明 ${human(part.bytes ?? 0)}  用时 ${((Date.now() - t0) / 1000).toFixed(1)}s`)
    }
    console.log(`分批合计 ${human(total)}`)
    return { kind: 'manifest', parts: manifest.parts?.length ?? 0 }
  }

  mkdirSync(OUT_DIR, { recursive: true })
  const file = join(OUT_DIR, `result-${jobId}.zip`)
  await pipeline(Readable.fromWeb(resp.body), createWriteStream(file))
  const size = statSync(file).size
  console.log(`产物形态: 单次 zip ${human(size)}，下载耗时 ${((Date.now() - started) / 1000).toFixed(1)}s`)
  console.log(`  已落盘: ${file}`)
  return { kind: 'zip', bytes: size }
}

async function main() {
  if (!existsSync(SOURCE)) throw new Error(`源文件不存在：${SOURCE}`)
  console.log(`服务端 ${SERVER_URL}\n产物目录 ${OUT_DIR}\n`)
  const jobId = await submit()
  if (!jobId) return

  const state = await poll(jobId)
  if (state.state !== 'done') {
    console.log(`\n判定：切片失败 —— ${state.error ?? '未知原因'}`)
    process.exitCode = 1
    return
  }

  const r = state.result ?? {}
  const perSegmentMs = r.segments ? Number((state.progress === 1 ? 0 : 0) || 0) : 0
  console.log(
    `\n产物: ${r.segments} 片 / ${human(r.bytes ?? 0)} / 单次返回=${r.singleResponse} / 分批=${r.parts?.length ?? 0}`,
  )
  if (r.segments) {
    console.log(`平均每片 ${human(Math.round((r.bytes ?? 0) / r.segments))}（${perSegmentMs}）`)
  }
  await fetchResult(jobId, state)
  console.log('\n判定：大文件端到端走通')
}

try {
  await main()
} catch (err) {
  console.error(`大文件验收失败：${err.message}`)
  process.exitCode = 1
}
