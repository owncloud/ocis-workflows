import { useGettext } from 'vue3-gettext'
import { useMessages } from '@ownclouders/web-pkg'
import type { AutomationStatus } from '../types/workflow'

interface AutomationApi {
  connectAutomation: () => Promise<AutomationStatus>
}

/** Connects background automation, and separately, notifying the user of that transition
 *  via a toast. `connect` and `notifyConnected` are split so a caller that has more work to
 *  do after connecting (e.g. persisting a workflow) can defer the toast until that work has
 *  actually completed — showing "success" before the real action is durable would let a user
 *  navigate away and lose unsaved work. `connectWithNotice` combines both steps for callers
 *  with nothing further to wait on (e.g. a mount-time self-heal with no separate persist
 *  step). Callers are responsible for checking whether automation is already connected
 *  before calling any of these — they always connect unconditionally. */
export function useAutomationConnect(api: AutomationApi) {
  const { $gettext } = useGettext()
  const { showMessage } = useMessages()

  const notifyConnected = (): void => {
    showMessage({
      title: $gettext('Background execution enabled for your account'),
      desc: $gettext('This workflow will keep running even when you are signed out.'),
      status: 'success'
    })
  }

  const connect = (): Promise<AutomationStatus> => api.connectAutomation()

  const connectWithNotice = async (): Promise<AutomationStatus> => {
    const status = await connect()
    notifyConnected()
    return status
  }

  return { connect, notifyConnected, connectWithNotice }
}
