// Settings.tsx —— 设置页:连接参数 / 路径 / 测试连接 / 保存 / 历史表格 / 导出待办。
import { useEffect, useState } from 'react'
import { exportTodos, getHistory, loadConfig, saveConfig, testConn } from '../api/remote'
import LivePrecheck from '../components/LivePrecheck'
import type { History, ToolConfig } from '../types'

// Settings 设置页组件。
// 参数: 无
// 返回: JSX
// 实现逻辑:
//   1. 挂载 → loadConfig() 回填表单
//   2. 表单修改即触发 LivePrecheck(草稿配置,LSP 式)
//   3. [测试连接] → testConn(form);[保存] → saveConfig
//   4. 历史表格 + 导出待办
export default function Settings() {
  const [form, setForm] = useState<ToolConfig | null>(null)
  const [hist, setHist] = useState<History[]>([])
  const [conn, setConn] = useState('')
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')

  const refresh = async () => {
    try {
      setForm(await loadConfig())
      setHist(await getHistory())
    } catch (e) {
      setErr((e as Error).message || '加载配置失败')
    }
  }
  useEffect(() => { void refresh() }, [])

  if (!form) return <div>加载中…</div>

  const set = (patch: Partial<ToolConfig>) => setForm((f) => (f ? Object.assign(f, patch) : f))
  const depsKey = JSON.stringify(form)

  const doTest = async () => {
    setConn('')
    setErr('')
    try {
      setConn(await testConn(form))
    } catch (e) {
      setErr((e as Error).message || '连接失败')
    }
  }

  const doSave = async () => {
    setErr('')
    setMsg('')
    try {
      await saveConfig(form)
      setMsg('配置已保存')
      await refresh()
    } catch (e) {
      setErr((e as Error).message || '保存失败')
    }
  }

  const doExport = async () => {
    try {
      const text = await exportTodos()
      if (!text) { setMsg('暂无待办'); return }
      await navigator.clipboard.writeText(text)
      setMsg('待办已复制到剪贴板(可粘贴进 tem/TODO.md)')
    } catch (e) {
      setErr((e as Error).message || '导出失败')
    }
  }

  const Row = ({ label, value, onChange, ph, type }: {
    label: string; value: string; onChange: (v: string) => void; ph?: string; type?: string
  }) => (
    <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 8 }}>
      <label style={{ width: 150, marginRight: 0 }}>{label}</label>
      <input
        type={type ?? 'text'} value={value} placeholder={ph}
        onChange={(e) => onChange(e.target.value)} style={{ flex: 1, maxWidth: 360 }}
      />
    </div>
  )

  return (
    <div>
      <div className="card">
        <h3>工具配置</h3>
        <Row label="sshbox 路径" value={form.sshbox_path} onChange={(v) => set({ sshbox_path: v })} ph="tool/sshbox.exe" />
        <Row label="sshbox.json" value={form.sshbox_json} onChange={(v) => set({ sshbox_json: v })} />
        <Row label="服务器地址 Host" value={form.host} onChange={(v) => set({ host: v })} />
        <Row label="用户" value={form.user} onChange={(v) => set({ user: v })} />
        <Row label="密码(留空=不改)" value={form.pass} onChange={(v) => set({ pass: v })} type="password" />
        <Row label="端口(0=读json)" value={String(form.port)} onChange={(v) => set({ port: Number(v) || 0 })} />
        <Row label="远端仓库路径" value={form.repo_path} onChange={(v) => set({ repo_path: v })} />
        <Row label="远端部署目录" value={form.deployed_path} onChange={(v) => set({ deployed_path: v })} />
        <div style={{ marginTop: 8 }}>
          <button onClick={doTest}>测试连接</button>
          <button onClick={doSave}>保存</button>
          <button className="secondary" onClick={doExport}>导出待办</button>
        </div>
        {conn && <div className="ok" style={{ marginTop: 8 }}>{conn}</div>}
        {err && <div className="err">{err}</div>}
        {msg && <div className="ok">{msg}</div>}
        <LivePrecheck draft={form} deps={depsKey} />
      </div>

      <div className="card">
        <h3>操作历史</h3>
        <table>
          <thead>
            <tr><th>时间</th><th>动作</th><th>目标</th><th>摘要</th></tr>
          </thead>
          <tbody>
            {hist.map((h, i) => (
              <tr key={i}>
                <td>{new Date(h.time).toLocaleString()}</td>
                <td>{h.action}</td>
                <td>{h.target || '-'}</td>
                <td>{h.summary}</td>
              </tr>
            ))}
            {hist.length === 0 && <tr><td colSpan={4} style={{ color: '#999' }}>暂无历史</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}
