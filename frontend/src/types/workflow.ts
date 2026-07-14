export type TriggerType = 'manual' | 'schedule' | 'event'
export type EventTriggerType = 'upload' | 'move' | 'share' | 'lock'
export type ActionType = 'tag' | 'comment' | 'move' | 'copy' | 'rename' | 'notify'
export type ExecutionStatus = 'running' | 'succeeded' | 'failed'
export type ExecutionTrigger = 'manual' | 'schedule' | 'event'

export interface WorkflowTrigger {
  type: TriggerType
  schedule?: string
  event?: {
    type: EventTriggerType
    filters?: {
      pathPrefix?: string
      extension?: string
      spaceId?: string
    }
  }
}

export interface WorkflowNodeData {
  label?: string
  // trigger node
  triggerType?: TriggerType
  schedule?: string
  event?: {
    type: EventTriggerType
    filters?: {
      pathPrefix?: string
      extension?: string
      spaceId?: string
    }
  }
  // llm node
  prompt?: string
  model?: string
  outputVariable?: string
  // action node
  actionType?: ActionType
  actionParams?: Record<string, unknown>
  condition?: string
}

export interface WorkflowNode {
  id: string
  type: 'trigger' | 'llm' | 'action'
  position: { x: number; y: number }
  data: WorkflowNodeData
}

export interface WorkflowEdge {
  id: string
  source: string
  target: string
  data?: { condition?: string }
}

export interface WorkflowGraph {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
}

export interface WorkflowDefinition {
  id: string
  name: string
  description?: string
  enabled: boolean
  trigger: WorkflowTrigger
  graph: WorkflowGraph
  createdDateTime: string
  lastModifiedDateTime: string
}

export interface NewWorkflowDefinition {
  name: string
  description?: string
  enabled: boolean
  trigger: WorkflowTrigger
  graph: WorkflowGraph
}

export interface WorkflowNodeResult {
  nodeId: string
  status: ExecutionStatus
  output?: unknown
  error?: { code: string; message: string }
}

export interface ExecutionRecord {
  id: string
  workflowId: string
  triggeredBy: ExecutionTrigger
  status: ExecutionStatus
  startedDateTime: string
  completedDateTime?: string
  nodeResults: WorkflowNodeResult[]
  error?: { code: string; message: string }
}

export interface AutomationStatus {
  connected: boolean
  expirationDateTime?: string
}

export interface GraphCollection<T> {
  value: T[]
}

export interface GraphError {
  error: {
    code: string
    message: string
  }
}
