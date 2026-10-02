// File System Access API 的最小封装。
//
// Chromium 系有 window.showDirectoryPicker；Firefox / Safari 还没有。
// TypeScript 的 lib.dom 目前不带 showDirectoryPicker 的声明（只有 FileSystemDirectoryHandle
// 及其方法），所以这里补一个全局声明，避免在组件里到处写 any。

declare global {
  interface Window {
    /** 弹出目录选择器并拿到可写句柄；用户取消时抛出 name === 'AbortError' 的 DOMException。 */
    showDirectoryPicker?: (options?: {
      id?: string
      mode?: 'read' | 'readwrite'
      startIn?: string
    }) => Promise<FileSystemDirectoryHandle>
  }
}

/** 当前浏览器能不能"直接把产物写进一个本地目录"。 */
export function supportsFileSystemAccess(): boolean {
  return typeof window !== 'undefined' && typeof window.showDirectoryPicker === 'function'
}

/**
 * 让用户挑一个目录（可写）。
 * 用户取消时抛出 DOMException(AbortError)，调用方按正常取消处理即可，不要当错误展示。
 */
export async function pickDirectory(): Promise<FileSystemDirectoryHandle> {
  const picker = window.showDirectoryPicker
  if (!picker) {
    throw new Error('当前浏览器不支持 File System Access API（showDirectoryPicker）')
  }
  return picker({ id: 'projection-room-media', mode: 'readwrite' })
}
