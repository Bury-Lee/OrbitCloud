// App.tsx —— 主窗口布局:顶部状态条 + 四个 Tab(审计/预检/脚本/设置)。
import { useState } from 'react'
import Audit from './pages/Audit'
import Precheck from './pages/Precheck'
import Script from './pages/Script'
import Settings from './pages/Settings'
import type { AuditResult } from './types'

type TabKey = 'audit' | 'precheck' | 'script' | 'settings'

const TABS: { key: TabKey; label: string }[] = [
  { key: 'audit', label: '审计' },
  { key: 'precheck', label: '预检' },
  { key: 'script', label: '脚本' },
  { key: 'settings', label: '设置' },
]

// App 根组件。
// 实现逻辑:
//   1. state: active TabKey;lastAudit(审计页结果上抛,顶部状态灯用)
//   2. 状态灯: 有审计结果且无 inactive 服务 → 绿"服务正常";有 inactive → 红;未审计 → 灰
export default function App() {
  const [tab, setTab] = useState<TabKey>('audit')
  const [audit, setAudit] = useState<AuditResult | null>(null)

  const anyDown = audit ? Object.values(audit.services).some((v) => !v) : false
  const lampColor = audit === null ? '#bbb' : anyDown ? '#f5222d' : '#52c41a'
  const lampText = audit === null ? '未审计' : anyDown ? '存在异常' : '服务正常'

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100vh' }}>
      <header style={{ background: '#2f54eb', color: '#fff', padding: '8px 16px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontWeight: 600 }}>OrbitCloud 部署运维工具</span>
        <span style={{ fontSize: 13 }}>
          <span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: '50%', background: lampColor, marginRight: 6 }} />
          {lampText}
        </span>
      </header>
      <nav style={{ padding: '6px 16px', background: '#fff', borderBottom: '1px solid #eee' }}>
        {TABS.map((t) => (
          <button key={t.key} className={tab === t.key ? '' : 'secondary'} onClick={() => setTab(t.key)}>
            {t.label}
          </button>
        ))}
      </nav>
      <main style={{ flex: 1, overflow: 'auto', padding: 16 }}>
        {tab === 'audit' && <Audit onResult={setAudit} />}
        {tab === 'precheck' && <Precheck />}
        {tab === 'script' && <Script />}
        {tab === 'settings' && <Settings />}
      </main>
    </div>
  )
}
