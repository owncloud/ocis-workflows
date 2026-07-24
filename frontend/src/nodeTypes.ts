import type { ActionType, TriggerType, WorkflowNodeData } from './types/workflow'

export type CanvasNodeKind = 'trigger' | 'llm' | 'action' | 'extractText'

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
// File-manipulation actions only (tag, comment, move, copy, rename): things that mutate the
// file itself. "Send Notification" is deliberately kept out of this bucket — see
// NOTIFICATION_CATEGORY below — because it doesn't touch the file at all, it sends a message
// to an external destination (Slack, email, a webhook, ...). Grouping it with file operations
// made the "Actions" picker section read as "everything that isn't AI", which stops being a
// useful scanning aid as more node types are added.
export const ACTION_CATEGORY = 'Actions'
export const NOTIFICATION_CATEGORY = 'Notifications'

export const NODE_TYPES: NodeTypeDefinition[] = [
  {
    id: 'trigger-manual',
    nodeKind: 'trigger',
    label: 'Manual Trigger',
    description: 'Runs only when you click "Run now"',
    icon: 'play-circle',
    category: TRIGGER_CATEGORY,
    defaultData: { label: 'Manual', triggerType: 'manual' }
  },
  {
    id: 'trigger-schedule',
    nodeKind: 'trigger',
    label: 'Schedule Trigger',
    description: 'Runs on a recurring schedule',
    icon: 'time',
    category: TRIGGER_CATEGORY,
    defaultData: { label: 'Schedule', triggerType: 'schedule', schedule: '0 * * * *' }
  },
  {
    id: 'trigger-event',
    nodeKind: 'trigger',
    label: 'File Event Trigger',
    description: 'Runs when a file is uploaded, moved, shared, or locked',
    icon: 'flashlight',
    category: TRIGGER_CATEGORY,
    defaultData: { label: 'File event', triggerType: 'event', event: { type: 'upload' } }
  },
  {
    id: 'trigger-webhook',
    nodeKind: 'trigger',
    label: 'Webhook Trigger',
    description: 'Runs when an external request hits a per-workflow URL',
    icon: 'link',
    category: TRIGGER_CATEGORY,
    defaultData: { label: 'Webhook', triggerType: 'webhook' }
  },
  {
    id: 'llm',
    nodeKind: 'llm',
    label: 'LLM Prompt',
    description: 'Ask an LLM to summarize, classify, or transform the file',
    icon: 'chat-3',
    category: AI_CATEGORY,
    defaultData: { prompt: '' }
  },
  {
    id: 'extract-text',
    nodeKind: 'extractText',
    label: 'Extract Text',
    description: 'Pull plain text out of a PDF or Word document for later steps to use',
    icon: 'file-text',
    category: AI_CATEGORY,
    defaultData: {}
  },
  {
    id: 'action-tag',
    nodeKind: 'action',
    actionType: 'tag',
    label: 'Add Tag',
    description: 'Add a tag to the file',
    icon: 'price-tag-3',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'tag' }
  },
  {
    id: 'action-comment',
    nodeKind: 'action',
    actionType: 'comment',
    label: 'Add Comment',
    description: 'Add a comment to the file',
    icon: 'chat-1',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'comment' }
  },
  {
    id: 'action-move',
    nodeKind: 'action',
    actionType: 'move',
    label: 'Move File',
    description: 'Move the file to another location',
    icon: 'folder-transfer',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'move' }
  },
  {
    id: 'action-copy',
    nodeKind: 'action',
    actionType: 'copy',
    label: 'Copy File',
    description: 'Copy the file to another location',
    icon: 'file-copy',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'copy' }
  },
  {
    id: 'action-rename',
    nodeKind: 'action',
    actionType: 'rename',
    label: 'Rename File',
    description: 'Rename the file',
    icon: 'edit',
    category: ACTION_CATEGORY,
    defaultData: { actionType: 'rename' }
  },
  {
    id: 'action-notify',
    nodeKind: 'action',
    actionType: 'notify',
    label: 'Send Notification',
    description: 'Send a notification to Slack, email, or 100+ other services',
    icon: 'notification-3',
    category: NOTIFICATION_CATEGORY,
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
