// 文件预览能力判定与分类(供列表"预览"按钮与预览对话框共用)
// 按 MIME 优先;无 MIME 或 MIME 不可用时按扩展名回退(代码/文本类)。

/** 文本/代码类扩展名(无 MIME 或 MIME 为 application/octet-stream 时的回退判定) */
export const TEXT_EXTS = [
  'txt', 'md', 'markdown', 'log', 'csv', 'tsv', 'ini', 'conf', 'cfg', 'env',
  'json', 'xml', 'yaml', 'yml', 'toml',
  'js', 'mjs', 'cjs', 'ts', 'jsx', 'tsx', 'vue', 'html', 'htm', 'css', 'scss', 'less',
  'go', 'rs', 'py', 'java', 'c', 'h', 'cpp', 'hpp', 'cs', 'rb', 'php', 'sh', 'bash',
  'zsh', 'ps1', 'bat', 'cmd', 'sql', 'graphql', 'proto', 'dockerfile', 'makefile',
  'gitignore', 'editorconfig', 'properties',
]

/** office/压缩等不支持在线预览的类型(用于明确提示) */
export const UNSUPPORTED_EXTS = [
  'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'zip', 'rar', '7z', 'tar', 'gz', 'exe',
]

/** 预览类别:image 图片 / media 音视频 / pdf / text 文本与代码 */
export type PreviewKind = 'image' | 'media' | 'pdf' | 'text'

function extOf(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot < 0 || dot === name.length - 1) return ''
  return name.slice(dot + 1).toLowerCase()
}

/** 是否支持在线预览 */
export function isPreviewable(fileType: string, name: string): boolean {
  const t = (fileType || '').toLowerCase()
  if (
    t.startsWith('image/') ||
    t.startsWith('video/') ||
    t.startsWith('audio/') ||
    t === 'application/pdf' ||
    t.startsWith('text/')
  ) {
    return true
  }
  const ext = extOf(name)
  if (UNSUPPORTED_EXTS.includes(ext)) return false
  return TEXT_EXTS.includes(ext)
}

/** 预览类别(配合 isPreviewable 使用;不支持的调用方应先行拦截) */
export function previewKind(fileType: string): PreviewKind {
  const t = (fileType || '').toLowerCase()
  if (t.startsWith('image/')) return 'image'
  if (t.startsWith('video/') || t.startsWith('audio/')) return 'media'
  if (t === 'application/pdf') return 'pdf'
  return 'text'
}
