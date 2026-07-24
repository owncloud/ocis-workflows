import { useGettext } from 'vue3-gettext'
import { useMessages } from '@ownclouders/web-pkg'
import type { AutomationStatus } from '../types/workflow'

interface AutomationApi {
  connectAutomation: () => Promise<AutomationStatus>
}

/** Silently connects background automation and shows a one-time toast for the transition.
 *  Callers are responsible for checking whether automation is already connected before
 *  calling this — it always connects unconditionally. */
export function useAutomationConnect(api: AutomationApi) {
  const { $gettext } = useGettext()
  const { showMessage } = useMessages()

  const connectWithNotice = async (): Promise<AutomationStatus> => {
    const status = await api.connectAutomation()
    showMessage({
      title: $gettext('Background execution enabled for your account'),
      desc: $gettext('This workflow will keep running even when you are signed out.'),
      status: 'success'
    })
    return status
  }

  return { connectWithNotice }
}
