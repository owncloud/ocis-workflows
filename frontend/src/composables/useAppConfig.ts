import { reactive } from 'vue'

interface AppConfig {
  backendUrl: string
}

const state = reactive<AppConfig>({
  backendUrl: ''
})

export function useAppConfig() {
  return state
}

export function setAppConfig(config: Partial<AppConfig>) {
  Object.assign(state, config)
}
