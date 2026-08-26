// remote.ts —— wailsjs 绑定调用封装(前端唯一数据通道)。
import {
  ExportTodos,
  GenScript,
  GetHistory,
  GetScriptForm,
  LoadConfig,
  RunAudit,
  RunPrecheck,
  SaveConfig,
  SaveScript,
  TestConn,
  ToggleTodo,
  ValidateScript,
} from '../../wailsjs/go/main/App'
import type {
  AuditResult,
  CheckItem,
  GenScriptResult,
  History,
  ScriptConfig,
  ScriptForm,
  ToolConfig,
  ValidateResult,
} from '../types'

export const loadConfig = (): Promise<ToolConfig> => LoadConfig()
export const saveConfig = (c: ToolConfig): Promise<void> => SaveConfig(c)
export const testConn = (c: ToolConfig): Promise<string> => TestConn(c)
export const runAudit = (): Promise<AuditResult> => RunAudit()
// RunPrecheck 必传 ToolConfig;传空配置({}) = 使用已保存配置(Go 侧判定)
export const runPrecheck = (c: ToolConfig): Promise<CheckItem[]> => RunPrecheck(c)
export const genScript = (sc: ScriptConfig): Promise<GenScriptResult> => GenScript(sc)
export const getScriptForm = (): Promise<ScriptForm> => GetScriptForm()
export const validateScript = (sc: ScriptConfig): Promise<ValidateResult> => ValidateScript(sc)
export const saveScript = (content: string, dir: string): Promise<string> => SaveScript(content, dir)
export const getHistory = (): Promise<History[]> => GetHistory()
export const toggleTodo = (text: string, on: boolean): Promise<void> => ToggleTodo(text, on)
export const exportTodos = (): Promise<string> => ExportTodos()
