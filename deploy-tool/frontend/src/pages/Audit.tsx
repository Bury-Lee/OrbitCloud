// Audit.tsx —— 审计页:环境卡片 / 服务端口表 / 配置项表 / 警告 / 建议(可勾选为待办)。
import { useState } from 'react'
import { runAudit, toggleTodo } from '../api/remote'
import type { AuditResult } from '../types'

// Audit 审计页组件。
// 参数: onResult (r: AuditResult|null) => void 结果上抛(顶部状态灯)
// 返回: JSX
export default function Audit({ onResult }: { onResult: (r: AuditResult | null) => void }) {
  const [result, setResult] = useState<AuditResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  const doAudit = async () => {
    setLoading(true)
    setErr('')
    try {
      const r = await runAudit()
      setResult(r)
      onResult(r)
    } catch (e) {
      const msg = (e as Error).message || '审计失败'
      setErr(msg)
      onResult(null)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div>
      <div className="card">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3 style={{ margin: 0 }}>审计(只读)</h3>
          <button onClick={doAudit} disabled={loading}>{loading ? '审计中…' : '开始审计'}</button>
        </div>
        {err && <div className="err">{err}</div>}
      </div>

      {result && (
        <>
          <div className="card">
            <h3>环境信息</h3>
            <table>
              <tbody>
                <tr><td>主机</td><td>{result.env.hostname}</td><td>后端版本</td><td>{result.env.orbit_version}</td></tr>
                <tr><td>运行时长</td><td>{result.env.uptime}</td><td>Go</td><td>{result.env.go_version}</td></tr>
                <tr><td>内存</td><td>{result.env.mem}</td><td>Rust</td><td>{result.env.rust_version}</td></tr>
                <tr><td>磁盘 /</td><td>{result.env.disk}</td><td>Node</td><td>{result.env.node_version}</td></tr>
              </tbody>
            </table>
          </div>

          <div className="card">
            <h3>服务与端口</h3>
            <table>
              <tbody>
                {Object.entries(result.services).map(([s, ok]) => (
                  <tr key={s}>
                    <td>{s}</td>
                    <td>{ok ? <span className="ok">● active</span> : <span className="err">● inactive</span>}</td>
                  </tr>
                ))}
                {Object.entries(result.ports).map(([p, ok]) => (
                  <tr key={p}>
                    <td>端口 {p}</td>
                    <td>{ok ? <span className="ok">● 监听中</span> : <span className="err">● 未监听</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="card">
            <h3>配置关键项</h3>
            <table>
              <tbody>
                {result.items.map((it, i) => (
                  <tr key={i}>
                    <td>{it.name}</td>
                    <td style={{ color: it.ok ? '#333' : '#f5222d' }}>{it.value}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="card">
            <h3>警告({result.warnings.length})</h3>
            {result.warnings.length === 0 ? (
              <div className="ok">✓ 未发现冲突或非健康设置</div>
            ) : (
              result.warnings.map((w, i) => <div key={i} className="err">⚠ {w}</div>)
            )}
          </div>

          <div className="card">
            <h3>建议({result.tips.length})</h3>
            {result.tips.length === 0 ? (
              <div className="ok">✓ 暂无建议</div>
            ) : (
              result.tips.map((t, i) => (
                <div key={i} style={{ marginBottom: 4 }}>
                  <label>
                    <input type="checkbox" onChange={(e) => toggleTodo(t.text, e.target.checked)} /> 
                    [{t.kind}] {t.text}
                  </label>
                </div>
              ))
            )}
          </div>
        </>
      )}
    </div>
  )
}
