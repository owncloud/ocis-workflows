import type { ActionType, TriggerType, WorkflowNodeData } from './types/workflow'

export type CanvasNodeKind = 'trigger' | 'llm' | 'action'

export interface NodeTypeDefinition {
  /** Picker entry id — distinct even when several entries share the same canvas node kind. */
  id: string
  nodeKind: CanvasNodeKind
  actionType?: ActionType
  label: string
  description: string
  icon: string
  category: string
  defaultData: WorkflowNodeData
}

export const TRIGGER_CATEGORY = 'Triggers'
export const AI_CATEGORY = 'AI'
export const ACTION_CATEGORY = 'Actions'

export const NODE_TYPES: NodeTypeDefinition[] = [
  {
    id: 'trigger-manual',
    nodeKind: 'trigger',
    label: 'Manual Trigger',
    description: 'Runs only when you click "Run now"',
    icon: 'play-circle-line',
    category: TRIGGER_CATEGORY,
    defaultData: { label: 'Manual', triggerType: 'manual' }
  },
  {
    id: 'trigger-schedule',
    nodeKind: 'trigger',
    label: 'Schedule Trigger',
    description: 'Runs on a recurring schedule',
    icon: 'time-line',
    category: TRIGGER_CATEGORY,
    defaultData: { label: 'Schedule', triggerType: 'schedule', schedule: '0 * * * *' }
  },
  {
    id: 'trigger-event',
    nodeKind: 'trigger',
    label: 'File Event Trigger',
    description: 'Runs when a file is uploaded, moved, shared, or locked',
    icon: 'flashlight-line',
    category: TRIGGER_CATEGORY,
    defaultData: { label: 'File event', triggerType: 'event', event: { type: 'upload' } }
  },
  {
    id: 'llm',
    nodeKind: 'llm',
    label: 'LLM Prompt',
    description: 'Ask an LLM to summarize, classify, or transform the file',
    icon: 'chat-3-line',
    category: AI_CATEGORY,
    defaultData: { prompt: '' }
  },
  {
    id: 'action-tag',
    nodeKind: 'action',
    actionType: 'tag',
    label: 'Add Tag',
    description: 'Add a tag to the file',
    icon: 'price-tag-3-line',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'tag' }
  },
  {
    id: 'action-comment',
    nodeKind: 'action',
    actionType: 'comment',
    label: 'Add Comment',
    description: 'Add a comment to the file',
    icon: 'chat-1-line',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'comment' }
  },
  {
    id: 'action-move',
    nodeKind: 'action',
    actionType: 'move',
    label: 'Move File',
    description: 'Move the file to another location',
    icon: 'folder-transfer-line',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'move' }
  },
  {
    id: 'action-copy',
    nodeKind: 'action',
    actionType: 'copy',
    label: 'Copy File',
    description: 'Copy the file to another location',
    icon: 'file-copy-line',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'copy' }
  },
  {
    id: 'action-rename',
    nodeKind: 'action',
    actionType: 'rename',
    label: 'Rename File',
    description: 'Rename the file',
    icon: 'edit-line',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'rename' }
  },
  {
    id: 'action-notify',
    nodeKind: 'action',
    actionType: 'notify',
    label: 'Send Notification',
    description: 'Send a notification to Slack, email, or 100+ other services',
    icon: 'notification-3-line',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'notify' }
  }
]

export function findNodeType(id: string): NodeTypeDefinition | undefined {
  return NODE_TYPES.find((t) => t.id === id)
}

/** The picker entry that matches an existing canvas node, used to show its icon/label in the NDV. */
export function findNodeTypeForNode(
  nodeKind: CanvasNodeKind,
  discriminator?: ActionType | TriggerType
): NodeTypeDefinition | undefined {
  if (nodeKind === 'action') {
    return NODE_TYPES.find((t) => t.nodeKind === 'action' && t.actionType === discriminator)
  }
  if (nodeKind === 'trigger') {
    return (
      NODE_TYPES.find((t) => t.nodeKind === 'trigger' && t.defaultData.triggerType === discriminator) ??
      NODE_TYPES.find((t) => t.nodeKind === 'trigger')
    )
  }
  return NODE_TYPES.find((t) => t.nodeKind === nodeKind)
}
