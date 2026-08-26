// LivePrecheck.tsx —— LSP 式实时预检条(修改即预检,防抖合并)。
import { useEffect, useRef, useState } from 'react'
import { runPrecheck } from '../api/remote'
import { emptyConfig, type CheckItem, type ToolConfig } from '../types'

export interface LivePrecheckProps {
  // draft 当前草稿配置(未保存的表单值);null = 使用已保存配置
  draft: ToolConfig | null
  // deps 变更依赖键(表单序列化串);变化即触发防抖预检
  deps: string
  // initial 是否挂载时立即预检一次
  initial?: boolean
}

// 预检状态条组件。
// 参数: 见 LivePrecheckProps
// 返回: JSX(状态条 + 可展开明细)
// 实现逻辑:
//   1. useEffect([deps]): 防抖 500ms;seq 计数丢弃过期结果;调 runPrecheck(draft)
//   2. 状态: idle(灰"等待修改…")/ loading(蓝"预检中…")/ done(绿 PASS n / 黄 WARN m)
//   3. 展开区: CheckItem 明细列表(PASS 绿 / WARN 黄)
//   4. 失败(连接错误): 红色显示错误,不阻断
export default function LivePrecheck({ draft, deps, initial }: LivePrecheckProps) {
  const [items, setItems] = useState<CheckItem[] | null>(null)
  const [err, setErr] = useState<string>('')
  const [loading, setLoading] = useState(false)
  const [open, setOpen] = useState(false)
  const [checkedAt, setCheckedAt] = useState('')
  const seq = useRef(0)

  useEffect(() => {
    if (!initial && deps === '') return
    setLoading(true)
    setErr('')
    const my = ++seq.current
    const t = setTimeout(() => {
      runPrecheck(draft ?? emptyConfig())
        .then((res) => {
          if (seq.current !== my) return
          setItems(res)
          setCheckedAt(new Date().toLocaleTimeString())
        })
        .catch((e: Error) => {
          if (seq.current !== my) return
          setErr(e.message || '预检失败')
        })
        .finally(() => {
          if (seq.current === my) setLoading(false)
        })
    }, 500)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deps])

  const pass = items?.filter((i) => i.result === 'PASS').length ?? 0
  const warn = (items?.length ?? 0) - pass

  const color = err ? '#f5222d' : loading ? '#1677ff' : warn > 0 ? '#faad14' : '#52c41a'
  const text = err
    ? `预检失败: ${err}`
    : loading
      ? '预检中…'
      : items === null
        ? '修改设置后自动预检'
        : `预检 ${pass} 项通过 / ${warn} 项警告${checkedAt ? `(${checkedAt})` : ''}`

  return (
    <div style={{ border: `1px solid ${color}`, borderRadius: 6, padding: '8px 12px', margin: '8px 0', fontSize: 13, background: '#fff' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ color, fontWeight: 600 }}>◉ {text}</span>
        {items && items.length > 0 && (
          <button onClick={() => setOpen(!open)} style={{ fontSize: 12 }}>{open ? '收起' : '明细'}</button>
        )}
      </div>
      {open && items && (
        <ul style={{ margin: '8px 0 0', paddingLeft: 20 }}>
          {items.map((it, i) => (
            <li key={i} style={{ color: it.result === 'PASS' ? '#52c41a' : '#faad14' }}>
              [{it.result}] {it.name}: {it.detail}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
