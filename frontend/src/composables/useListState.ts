// 列表加载三态复用:loading / error(可重试) / ready。
// 所有列表页(Buckets/Files/Shares/Groups/Users/MyGroups)同构。
import { ref, type Ref } from 'vue'

export interface UseListState {
  /** 加载中 */
  loading: Ref<boolean>
  /** 错误信息(空串 = 无错误) */
  error: Ref<string>
  /** 执行加载:成功返回数据,失败置 error 并返回 null(错误提示由调用方决定) */
  run: <T>(fn: () => Promise<T>, onError?: (msg: string) => void) => Promise<T | null>
}

export function useListState(): UseListState {
  const loading = ref(false)
  const error = ref('')

  async function run<T>(fn: () => Promise<T>, onError?: (msg: string) => void): Promise<T | null> {
    loading.value = true
    error.value = ''
    try {
      return await fn()
    } catch (e) {
      const msg = e instanceof Error ? e.message : '请求失败'
      error.value = msg
      onError?.(msg)
      return null
    } finally {
      loading.value = false
    }
  }

  return { loading, error, run }
}
