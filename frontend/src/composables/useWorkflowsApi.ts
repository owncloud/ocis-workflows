import { useAuthStore } from '@ownclouders/web-pkg'
import type {
  AutomationStatus,
  ExecutionRecord,
  GraphCollection,
  GraphError,
  NewWorkflowDefinition,
  WebhookTokenInfo,
  WorkflowDefinition
} from '../types/workflow'

export class WorkflowsApiError extends Error {
  code: string
  status: number

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'WorkflowsApiError'
    this.status = status
    this.code = code
  }
}

export function useWorkflowsApi(backendUrl: string) {
  const authStore = useAuthStore()
  const base = backendUrl.replace(/\/$/, '')
  // The webhook trigger's own POST /hooks/{workflowId}/{token} route lives outside
  // /api/v1beta1 (see backend/pkg/server/http/server.go) — it's reached through the same
  // reverse-proxy prefix as everything else in this app (.../workflows/...), just without
  // the /api/v1beta1 suffix every other request in this file uses.
  const hooksBase = base.replace(/\/api\/v1beta1$/, '')

  const buildHeaders = (): Record<string, string> => {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    const token = authStore.accessToken
    if (token) {
      headers['Authorization'] = `Bearer ${token}`
    }
    return headers
  }

  const rawRequest = async (path: string, init?: RequestInit): Promise<Response> => {
    const res = await fetch(`${base}${path}`, {
      ...init,
      headers: { ...buildHeaders(), ...(init?.headers ?? {}) }
    })

    if (!res.ok) {
      let code = 'unknownError'
      let message = `Request failed with status ${res.status}`
      try {
        const body = (await res.json()) as GraphError
        code = body.error?.code ?? code
        message = body.error?.message ?? message
      } catch {
        // response body wasn't JSON, keep the defaults
      }
      throw new WorkflowsApiError(res.status, code, message)
    }

    return res
  }

  const request = async <T>(path: string, init?: RequestInit): Promise<T> => {
    const res = await rawRequest(path, init)
    if (res.status === 204) {
      return undefined as T
    }
    return (await res.json()) as T
  }

  const listWorkflows = (): Promise<WorkflowDefinition[]> =>
    request<GraphCollection<WorkflowDefinition>>('/me/workflows').then((c) => c.value)

  const getWorkflow = (id: string): Promise<WorkflowDefinition> =>
    request<WorkflowDefinition>(`/me/workflows/${encodeURIComponent(id)}`)

  const createWorkflow = (workflow: NewWorkflowDefinition): Promise<WorkflowDefinition> =>
    request<WorkflowDefinition>('/me/workflows', {
      method: 'POST',
      body: JSON.stringify(workflow)
    })

  const updateWorkflow = (
    id: string,
    patch: Partial<NewWorkflowDefinition>
  ): Promise<WorkflowDefinition> =>
    request<WorkflowDefinition>(`/me/workflows/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(patch)
    })

  const deleteWorkflow = (id: string): Promise<void> =>
    request<void>(`/me/workflows/${encodeURIComponent(id)}`, { method: 'DELETE' })

  /** Runs a workflow and returns the id of the resulting execution record (parsed from the
   *  202 response's Location header — the backend runs synchronously today, but this keeps
   *  the frontend honest about the API's async shape). */
  const runWorkflow = async (id: string, resourcePath?: string): Promise<string> => {
    const res = await rawRequest(`/me/workflows/${encodeURIComponent(id)}/run`, {
      method: 'POST',
      body: resourcePath ? JSON.stringify({ resourcePath }) : undefined
    })
    const location = res.headers.get('Location') ?? ''
    const execId = location.split('/').pop()
    if (!execId) {
      throw new WorkflowsApiError(res.status, 'noExecutionId', 'run response had no execution id')
    }
    return execId
  }

  const listExecutions = (workflowId: string): Promise<ExecutionRecord[]> =>
    request<GraphCollection<ExecutionRecord>>(
      `/me/workflows/${encodeURIComponent(workflowId)}/executions`
    ).then((c) => c.value)

  const getExecution = (workflowId: string, execId: string): Promise<ExecutionRecord> =>
    request<ExecutionRecord>(
      `/me/workflows/${encodeURIComponent(workflowId)}/executions/${encodeURIComponent(execId)}`
    )

  const toWebhookTokenInfo = (raw: { token: string; path: string }): WebhookTokenInfo => ({
    token: raw.token,
    url: `${hooksBase}${raw.path}`
  })

  /** "Reveal" — fetches the webhook trigger's current token/URL. 404s (via
   *  WorkflowsApiError) if the workflow isn't a webhook trigger, or no token has been
   *  generated for it yet (shouldn't happen once saved — the backend generates one on
   *  first save of a webhook trigger). */
  const getWebhookToken = (workflowId: string): Promise<WebhookTokenInfo> =>
    request<{ token: string; path: string }>(
      `/me/workflows/${encodeURIComponent(workflowId)}/webhook-token`
    ).then(toWebhookTokenInfo)

  /** "Rotate" — replaces the webhook trigger's token, immediately invalidating the
   *  previous URL for any external caller still using it. */
  const rotateWebhookToken = (workflowId: string): Promise<WebhookTokenInfo> =>
    request<{ token: string; path: string }>(
      `/me/workflows/${encodeURIComponent(workflowId)}/webhook-token/rotate`,
      { method: 'POST' }
    ).then(toWebhookTokenInfo)

  const getAutomationStatus = (): Promise<AutomationStatus> => request<AutomationStatus>('/me/automation')

  const connectAutomation = (): Promise<AutomationStatus> =>
    request<AutomationStatus>('/me/automation', { method: 'POST' })

  const disconnectAutomation = (): Promise<void> => request<void>('/me/automation', { method: 'DELETE' })

  return {
    listWorkflows,
    getWorkflow,
    createWorkflow,
    updateWorkflow,
    deleteWorkflow,
    runWorkflow,
    listExecutions,
    getExecution,
    getWebhookToken,
    rotateWebhookToken,
    getAutomationStatus,
    connectAutomation,
    disconnectAutomation
  }
}
