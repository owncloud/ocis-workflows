import { useAuthStore } from '@ownclouders/web-pkg'
import type {
  ExecutionRecord,
  GraphCollection,
  GraphError,
  NewWorkflowDefinition,
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

  return {
    listWorkflows,
    getWorkflow,
    createWorkflow,
    updateWorkflow,
    deleteWorkflow,
    runWorkflow,
    listExecutions,
    getExecution
  }
}
