#!/usr/bin/env node
/**
 * 诊断脚本：量化"把切片写进本地文件夹"各阶段耗时，找出慢在哪。
 *
 * 做法：真实 Chrome 里动态 import 前端的解压模块，
 *   A) 只解析 zip（中央目录 + 必要时 inflate）——纯 CPU；
 *   B) 完整写进一个真实 FileSystemDirectoryHandle（用 OPFS，免去原生目录选择框）。
 * 二者之差 ≈ 每个文件的写入开销（`createWritable()` 的固定成本）。
 *
 * 用法（仓库根目录）：
 *   node test/script/diag-write.mjs [--source <源视频>] [--client http://127.0.0.1:5173]
 */
import { existsSync } from 'node:fs'
import { argOf, findChrome, openTarget, sleep, startChrome } from './lib/browser.mjs'

const argv = process.argv.slice(2)
const CLIENT_URL = argOf(argv, 'client', 'http://127.0.0.1:5173')
const SERVER_URL = argOf(argv, 'server', 'http://127.0.0.1:8080')
const SOURCE = argOf(argv, 'source', 'test/resource/short_video/short_video.mp4')
const BASE_PORT = Number(argOf(argv, 'port', '9700'))

async function submitJob() {
  const buf = await (await import('node:fs/promises')).readFile(SOURCE)
  const form = new FormData()
  form.append('file', new Blob([buf], { type: 'video/mp4' }), 'source.mp4')
  const resp = await fetch(`${SERVER_URL}/api/v1/segment/jobs`, { method: 'POST', body: form })
  const body = await resp.json()
  if (!resp.ok) throw new Error(`提交失败：HTTP ${resp.status} ${JSON.stringify(body)}`)
  return body.jobId
}

async function waitDone(jobId) {
  for (let i = 0; i < 120; i += 1) {
    await sleep(1000)
    const state = await (await fetch(`${SERVER_URL}/api/v1/segment/jobs/${jobId}`)).json()
    if (state.state === 'done') return state
    if (state.state === 'failed') throw new Error(`作业失败：${state.error}`)
  }
  throw new Error('作业超时')
}

async function main() {
  if (!existsSync(SOURCE)) throw new Error(`源视频不存在：${SOURCE}`)
  const jobId = await submitJob()
  console.log(`作业 ${jobId}（源：${SOURCE}）`)
  const done = await waitDone(jobId)
  console.log(`完成：${done.result.segments} 片 / ${done.result.bytes} 字节 / 单次返回=${done.result.singleResponse}`)

  const chrome = await startChrome(findChrome(), BASE_PORT, 'write')
  const cdp = await openTarget(BASE_PORT, 'write')
  await cdp.navigate(CLIENT_URL)
  await sleep(1500)

  const script = `(async () => {
    const mod = await import('/src/api/segmentZip.ts');
    const t0 = performance.now();
    const resp = await fetch('/api/v1/segment/jobs/${jobId}/result');
    const blob = await resp.blob();
    const tFetch = performance.now();

    const entries = await mod.readZipEntries(blob);
    const tParse = performance.now();

    const root = await navigator.storage.getDirectory();
    const dir = await root.getDirectoryHandle('probe-${jobId}', { create: true });
    let lastTick = performance.now();
    const gaps = [];
    await mod.extractZipToDirectory(blob, dir, (p) => {
      const now = performance.now();
      gaps.push(Math.round(now - lastTick));
      lastTick = now;
    });
    const tWrite = performance.now();

    // 第二轮：目标文件已存在且大小一致，应当走"跳过重复写"的快路径。
    const tSecond = performance.now();
    await mod.extractZipToDirectory(blob, dir);
    const tSecondEnd = performance.now();

    const totalBytes = entries.reduce((sum, e) => sum + e.blob.size, 0);
    return JSON.stringify({
      zipBytes: blob.size,
      entries: entries.length,
      totalBytes,
      ms: {
        fetch: Math.round(tFetch - t0),
        parse: Math.round(tParse - tFetch),
        write: Math.round(tWrite - tParse),
        writeAgain: Math.round(tSecondEnd - tSecond),
      },
      perFileMs: entries.length ? Number(((tWrite - tParse) / entries.length).toFixed(1)) : 0,
      slowestGaps: gaps.sort((a, b) => b - a).slice(0, 5),
      medianGap: gaps.length ? gaps.sort((a, b) => a - b)[Math.floor(gaps.length / 2)] : 0,
    });
  })()`

  const raw = await cdp.evaluate(script, 300000)
  console.log(JSON.stringify(JSON.parse(raw), null, 2))

  const cleanup = `(async () => {
    const root = await navigator.storage.getDirectory();
    await root.removeEntry('probe-${jobId}', { recursive: true });
    return 'ok';
  })()`
  await cdp.evaluate(cleanup, 60000).catch(() => 'cleanup-failed')
}

try {
  await main()
} catch (err) {
  console.error(`诊断失败：${err.message}`)
  process.exitCode = 1
} finally {
  await sleep(300)
  process.exit(process.exitCode ?? 0)
}
