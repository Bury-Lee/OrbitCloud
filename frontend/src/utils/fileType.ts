// 上传文件类型简化映射:按扩展名归类为短 MIME(后端不存 OOXML 等 80+ 字符长串)。
// 与后端 server.shortTypeByExt 口径一致;未知类型 → application/octet-stream。
const FILE_TYPE_MAP: Record<string, string> = {
  // 图片
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  png: 'image/png',
  gif: 'image/gif',
  webp: 'image/webp',
  bmp: 'image/bmp',
  svg: 'image/svg+xml',
  ico: 'image/x-icon',
  // 视频
  mp4: 'video/mp4',
  webm: 'video/webm',
  mov: 'video/quicktime',
  avi: 'video/x-msvideo',
  mkv: 'video/x-matroska',
  // 音频
  mp3: 'audio/mpeg',
  wav: 'audio/wav',
  ogg: 'audio/ogg',
  flac: 'audio/flac',
  m4a: 'audio/mp4',
  // 文档(统一短 MIME,含 OOXML)
  pdf: 'application/msword',
  doc: 'application/msword',
  docx: 'application/msword',
  rtf: 'application/msword',
  odt: 'application/msword',
  xls: 'application/vnd.ms-excel',
  xlsx: 'application/vnd.ms-excel',
  ods: 'application/vnd.ms-excel',
  csv: 'application/vnd.ms-excel',
  ppt: 'application/msword',
  pptx: 'application/msword',
  // 文本/代码
  txt: 'text/plain',
  md: 'text/markdown',
  log: 'text/plain',
  ini: 'text/plain',
  conf: 'text/plain',
  json: 'application/json',
  xml: 'text/xml',
  yaml: 'text/yaml',
  yml: 'text/yaml',
  html: 'text/html',
  css: 'text/css',
  js: 'text/javascript',
  ts: 'text/javascript',
  jsx: 'text/javascript',
  tsx: 'text/javascript',
  go: 'text/plain',
  rs: 'text/plain',
  py: 'text/plain',
  java: 'text/plain',
  c: 'text/plain',
  h: 'text/plain',
  cpp: 'text/plain',
  sh: 'text/plain',
  sql: 'text/plain',
  // 压缩包
  zip: 'application/zip',
  rar: 'application/x-rar-compressed',
  '7z': 'application/x-7z-compressed',
  tar: 'application/x-tar',
  gz: 'application/gzip',
}

/** 按文件名扩展名返回简化 MIME 类型(未知 → application/octet-stream) */
export function simplifyFileType(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot < 0 || dot === name.length - 1) return 'application/octet-stream'
  const ext = name.slice(dot + 1).toLowerCase()
  return FILE_TYPE_MAP[ext] ?? 'application/octet-stream'
}
