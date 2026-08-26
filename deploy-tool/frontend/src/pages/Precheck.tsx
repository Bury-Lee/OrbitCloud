// Precheck.tsx —— 预检页:PASS/WARN 两色列表 + 手动重新预检。
import { useState } from 'react'
import { runPrecheck } from '../api/remote'
import LivePrecheck from '../components/LivePrecheck'
import { emptyConfig, type CheckItem } from '../types'

// Precheck 预检页组件。
// 参数: 无
// 返回: JSX
// 实现逻辑:
//   1. 顶部: LivePrecheck(initial=true) 实时预检条(使用已保存配置)
//   2. [重新预检] → runPrecheck(null) → 更新列表(此操作写入历史)
//   3. 列表: PASS 绿 / WARN 黄
export default function Precheck() {
  const [items, setItems] = useState<CheckItem[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  const doCheck = async () => {
    setLoading(true)
    setErr('')
    try {
      setItems(await runPrecheck(emptyConfig()))
    } catch (e) {
      setErr((e as Error).message || '预检失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div>
      <div className="card">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3 style={{ margin: 0 }}>部署前预检(只警告不阻断)</h3>
          <button onClick={doCheck} disabled={loading}>{loading ? '预检中…' : '重新预检'}</button>
        </div>
        <LivePrecheck draft={null} deps="manual" initial />
        {err && <div className="err">{err}</div>}
      </div>
      <div className="card">
        <h3>预检明细({items.length})</h3>
        {items.length === 0 && <div style={{ fontSize: 13, color: '#999' }}>点击"重新预检"或修改设置页参数触发</div>}
        {items.map((it, i) => (
          <div key={i} style={{ marginBottom: 6, fontSize: 13 }}>
            <span style={{ color: it.result === 'PASS' ? '#52c41a' : '#faad14', fontWeight: 600 }}>[{it.result}]</span>{' '}
            <b>{it.name}</b>: {it.detail}
          </div>
        ))}
      </div>
    </div>
  )
}
