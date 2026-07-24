import { describe, expect, it, vi } from 'vitest'

const showMessage = vi.fn()
vi.mock('@ownclouders/web-pkg', () => ({
  useMessages: () => ({ showMessage })
}))
vi.mock('vue3-gettext', () => ({
  useGettext: () => ({ $gettext: (msg: string) => msg })
}))

import { useAutomationConnect } from '../../src/composables/useAutomationConnect'

describe('useAutomationConnect', () => {
  it('connects and shows a one-time toast', async () => {
    const connectAutomation = vi.fn().mockResolvedValue({ connected: true, expirationDateTime: '2026-10-01T00:00:00Z' })
    const { connectWithNotice } = useAutomationConnect({ connectAutomation })

    const status = await connectWithNotice()

    expect(status).toEqual({ connected: true, expirationDateTime: '2026-10-01T00:00:00Z' })
    expect(connectAutomation).toHaveBeenCalledOnce()
    expect(showMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        title: 'Background execution enabled for your account',
        status: 'success'
      })
    )
  })

  it('propagates a failed connect without showing a toast', async () => {
    showMessage.mockClear()
    const connectAutomation = vi.fn().mockRejectedValue(new Error('boom'))
    const { connectWithNotice } = useAutomationConnect({ connectAutomation })

    await expect(connectWithNotice()).rejects.toThrow('boom')
    expect(showMessage).not.toHaveBeenCalled()
  })
})
