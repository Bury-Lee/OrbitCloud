// Script.tsx —— 脚本页:参数化表单(下拉/填值/开关)→ 实时校验 → 生成确定值脚本 → 预览/复制/保存。
import { useEffect, useRef, useState } from 'react'
import { genScript, getScriptForm, saveScript, validateScript } from '../api/remote'
import LivePrecheck from '../components/LivePrecheck'
import { formToConfig, type GenScriptResult, type ScriptForm, type ValidateResult } from '../types'

// Script 脚本页组件。
// 参数: 无
// 返回: JSX
// 实现逻辑:
//   1. 挂载 → getScriptForm() → form 状态初始化(键值对)
//   2. 按 FieldGroup 渲染分组表单: select 下拉 / input / password / number / checkbox
//   3. 任何修改 → 更新 form → 防抖 400ms → validateScript(formToConfig(form))
//      → issues 实时展示(error 红=阻断生成 / warn 黄=提示)
//   4. [生成] → genScript → 只读 <pre> 预览; [复制执行命令] / [保存 .sh]
export default function Script() {
  const [formDef, setFormDef] = useState<ScriptForm | null>(null)
  const [form, setForm] = useState<Record<string, string>>({})
  const [issues, setIssues] = useState<ValidateResult | null>(null)
  const [result, setResult] = useState<GenScriptResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [genLoading, setGenLoading] = useState(false)
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const timer = useRef<number | undefined>(undefined)

  useEffect(() => {
    getScriptForm()
      .then((f) => {
        setFormDef(f)
        setForm({ ...f.last })
      })
      .catch((e) => setErr((e as Error).message || '加载表单失败'))
  }, [])

  // LSP 式实时校验: 修改即防抖校验
  useEffect(() => {
    if (!formDef) return
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => {
      validateScript(formToConfig(form))
        .then(setIssues)
        .catch(() => setIssues(null))
    }, 400)
    return () => window.clearTimeout(timer.current)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form])

  const set = (key: string, value: string) => setForm((f) => ({ ...f, [key]: value }))
  const depsKey = JSON.stringify(form)
  const errors = (issues?.issues ?? []).filter((i) => i.level === 'error')
  const warns = (issues?.issues ?? []).filter((i) => i.level === 'warn')

  const doGen = async () => {
    setErr('')
    setMsg('')
    if (errors.length > 0) {
      setErr(`存在 %d 项错误配置,请修正后再生成: ${errors.map((e) => e.message).join('; ')}`.replace('%d', String(errors.length)))
      return
    }
    setGenLoading(true)
    try {
      setResult(await genScript(formToConfig(form)))
    } catch (e) {
      setErr((e as Error).message || '生成失败')
    } finally {
      setGenLoading(false)
    }
  }

  const doCopy = async () => {
    if (!result) return
    await navigator.clipboard.writeText(result.exec_cmd)
    setMsg('已复制执行命令')
  }

  const doSave = async () => {
    if (!result) return
    try {
      const path = await saveScript(result.script, '')
      setMsg('已保存: ' + path)
    } catch (e) {
      setErr((e as Error).message || '保存失败')
    }
  }

  const renderField = (key: string, label: string, type: string, options: string[], placeholder: string, hint: string) => {
    const value = form[key] ?? ''
    return (
      <div key={key} style={{ marginBottom: 10, display: 'flex', alignItems: 'flex-start', gap: 8 }}>
        <label style={{ width: 180, minWidth: 180, fontSize: 13, paddingTop: 5 }} title={hint}>
          {label}
        </label>
        {type === 'select' ? (
          <select value={value} onChange={(e) => set(key, e.target.value)} style={{ minWidth: 220 }}>
            {options.map((o) => <option key={o} value={o}>{o}</option>)}
          </select>
        ) : type === 'checkbox' ? (
          <label style={{ paddingTop: 5 }}>
            <input type="checkbox" checked={value === 'true'} onChange={(e) => set(key, String(e.target.checked))} />
            <span style={{ fontSize: 12, color: '#999' }}> {hint}</span>
          </label>
        ) : (
          <input
            type={type === 'password' ? 'password' : type === 'number' ? 'number' : 'text'}
            value={value}
            placeholder={placeholder}
            onChange={(e) => set(key, e.target.value)}
            style={{ minWidth: 220, padding: '5px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }}
          />
        )}
        {type !== 'checkbox' && hint && <span style={{ fontSize: 12, color: '#999', paddingTop: 5 }}>{hint}</span>}
      </div>
    )
  }

  return (
    <div>
      <div className="card">
        <h3>生成部署脚本(参数化配置 → 确定值 .sh;工具不执行部署,请审阅后自行执行)</h3>
        <LivePrecheck draft={null} deps={depsKey} />
        {err && <div className="err" style={{ whiteSpace: 'pre-wrap' }}>{err}</div>}
        {msg && <div className="ok">{msg}</div>}
      </div>

      {formDef && (
        <div className="card">
          {formDef.groups.map((g) => (
            <details key={g.key} open>
              <summary style={{ cursor: 'pointer', fontWeight: 600, fontSize: 14, margin: '10px 0 8px', color: '#2f54eb' }}>
                {g.name}
              </summary>
              <div style={{ paddingLeft: 8 }}>
                {g.fields.map((f) =>
                  renderField(f.key, f.label, f.type, f.options ?? [], f.placeholder ?? '', f.hint ?? ''),
                )}
              </div>
            </details>
          ))}
        </div>
      )}

      {issues && (errors.length > 0 || warns.length > 0) && (
        <div className="card">
          <h3>配置检查({errors.length} 错误 / {warns.length} 提示)</h3>
          {errors.map((e, i) => (
            <div key={i} className="err">✕ {e.message}</div>
          ))}
          {warns.map((w, i) => (
            <div key={i} className="warn">⚠ {w.message}</div>
          ))}
        </div>
      )}

      <div className="card" style={{ display: 'flex', gap: 8 }}>
        <button onClick={doGen} disabled={genLoading || !formDef}>{genLoading ? '生成中…' : '生成脚本'}</button>
        <button className="secondary" onClick={doCopy} disabled={!result}>复制执行命令</button>
        <button className="secondary" onClick={doSave} disabled={!result}>保存 .sh</button>
      </div>

      {result && (
        <div className="card">
          <h3>脚本预览({result.lines} 行,确定值)</h3>
          <pre style={{ maxHeight: 420 }}>{result.script}</pre>
        </div>
      )}
    </div>
  )
}
