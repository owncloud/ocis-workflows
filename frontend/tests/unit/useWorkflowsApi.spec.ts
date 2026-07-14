import { describe, expect, it, vi, beforeEach } from 'vitest'

vi.mock('@ownclouders/web-pkg', () => ({
  useAuthStore: () => ({ accessToken: 'test-token' })
}))

import { useWorkflowsApi, WorkflowsApiError } from '../../src/composables/useWorkflowsApi'

describe('useWorkflowsApi', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  it('unwraps the Graph-style collection envelope on list', async () => {
    ;(fetch as unknown as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ value: [{ id: '1', name: 'wf' }] })
    })

    const api = useWorkflowsApi('https://example.test/api/v1beta1')
    const result = await api.listWorkflows()

    expect(result).toEqual([{ id: '1', name: 'wf' }])
    expect(fetch).toHaveBeenCalledWith(
      'https://example.test/api/v1beta1/me/workflows',
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: 'Bearer test-token' })
      })
    )
  })

  it('PATCHes for updates, not PUT', async () => {
    ;(fetch as unknown as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ id: '1', name: 'renamed' })
    })

    const api = useWorkflowsApi('https://example.test/api/v1beta1')
    await api.updateWorkflow('1', { name: 'renamed' })

    expect(fetch).toHaveBeenCalledWith(
      'https://example.test/api/v1beta1/me/workflows/1',
      expect.objectContaining({ method: 'PATCH' })
    )
  })

  it('throws a WorkflowsApiError shaped from the Graph-style error envelope', async () => {
    ;(fetch as unknown as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: false,
      status: 404,
      json: async () => ({ error: { code: 'workflowNotFound', message: 'not found' } })
    })

    const api = useWorkflowsApi('https://example.test/api/v1beta1')

    await expect(api.getWorkflow('missing')).rejects.toMatchObject({
      code: 'workflowNotFound',
      status: 404
    } as Partial<WorkflowsApiError>)
  })
})
