// 弹窗互斥枚举:同一时刻至多一个弹窗打开(替代视图内多布尔 ref)。
// 用法:const dialog = useDialog(['upload', 'rename', ...])
//       dialog.open('upload')  打开
//       dialog.close()         关闭
//       v-model="dialog.model('upload')"  模板绑定(el-dialog)
import { computed, shallowRef, type Ref } from 'vue'

export interface UseDialog<K extends string> {
  /** 当前打开的弹窗 key;null = 无弹窗 */
  current: Ref<K | null>
  /** 打开弹窗(自动关闭其他弹窗,保证互斥) */
  open: (key: K) => void
  /** 关闭当前弹窗 */
  close: () => void
  /** 是否打开指定弹窗(可直接用于 v-if) */
  is: (key: K) => boolean
  /** el-dialog v-model 绑定(get/set 双向) */
  model: (key: K) => { value: boolean }
}

export function useDialog<K extends string>(_keys: readonly K[]): UseDialog<K> {
  const current: Ref<K | null> = shallowRef<K | null>(null as K | null)

  function open(key: K) {
    current.value = key
  }

  function close() {
    current.value = null
  }

  function is(key: K): boolean {
    return current.value === key
  }

  function model(key: K) {
    return computed({
      get: () => current.value === key,
      set: (v: boolean) => {
        current.value = v ? key : null
      },
    })
  }

  return { current, open, close, is, model }
}
