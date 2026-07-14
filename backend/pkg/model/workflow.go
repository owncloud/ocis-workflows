// Package model defines the Graph-shaped resources exposed by the workflows REST API.
package model

// WorkflowTrigger describes what starts a workflow run.
type WorkflowTrigger struct {
	Type     string        `json:"type"` // manual | schedule | event
	Schedule string        `json:"schedule,omitempty"`
	Event    *EventTrigger `json:"event,omitempty"`
}

// EventTrigger describes a file-activity trigger and its filters.
type EventTrigger struct {
	Type    string        `json:"type"` // upload | move | share | lock
	Filters *EventFilters `json:"filters,omitempty"`
}

// EventFilters narrows which resources an event trigger reacts to.
type EventFilters struct {
	PathPrefix string `json:"pathPrefix,omitempty"`
	Extension  string `json:"extension,omitempty"`
	SpaceID    string `json:"spaceId,omitempty"`
}

// WorkflowGraph is the Vue Flow node/edge graph, stored verbatim.
type WorkflowGraph struct {
	Nodes []WorkflowNode `json:"nodes"`
	Edges []WorkflowEdge `json:"edges"`
}

// WorkflowNode is a single step in the graph (trigger, llm, or action).
type WorkflowNode struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Position NodePosition   `json:"position"`
	Data     map[string]any `json:"data"`
}

// NodePosition is the node's canvas coordinates.
type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// WorkflowEdge connects two nodes, optionally guarded by a condition.
type WorkflowEdge struct {
	ID     string    `json:"id"`
	Source string    `json:"source"`
	Target string    `json:"target"`
	Data   *EdgeData `json:"data,omitempty"`
}

// EdgeData carries an optional condition expression for the edge.
type EdgeData struct {
	Condition string `json:"condition,omitempty"`
}

// WorkflowDefinition is the user-owned workflow resource, stored via WebDAV in the
// caller's own space. There is no owner id field: ownership is implicit in whichever
// user's space the resource was read from.
type WorkflowDefinition struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	Enabled              bool            `json:"enabled"`
	Trigger              WorkflowTrigger `json:"trigger"`
	Graph                WorkflowGraph   `json:"graph"`
	CreatedDateTime      string          `json:"createdDateTime"`
	LastModifiedDateTime string          `json:"lastModifiedDateTime"`
}

// WorkflowPatch is the subset of WorkflowDefinition fields a PATCH request may update.
type WorkflowPatch struct {
	Name        *string          `json:"name,omitempty"`
	Description *string          `json:"description,omitempty"`
	Enabled     *bool            `json:"enabled,omitempty"`
	Trigger     *WorkflowTrigger `json:"trigger,omitempty"`
	Graph       *WorkflowGraph   `json:"graph,omitempty"`
}

// ExecutionRecord is a single run of a workflow.
type ExecutionRecord struct {
	ID                string       `json:"id"`
	WorkflowID        string       `json:"workflowId"`
	TriggeredBy       string       `json:"triggeredBy"` // manual | schedule | event
	Status            string       `json:"status"`      // running | succeeded | failed
	StartedDateTime   string       `json:"startedDateTime"`
	CompletedDateTime string       `json:"completedDateTime,omitempty"`
	NodeResults       []NodeResult `json:"nodeResults"`
	Error             *ErrorDetail `json:"error,omitempty"`
}

// NodeResult is the outcome of a single node during an execution.
type NodeResult struct {
	NodeID string       `json:"nodeId"`
	Status string       `json:"status"`
	Output any          `json:"output,omitempty"`
	Error  *ErrorDetail `json:"error,omitempty"`
}

// ErrorDetail is the Graph-style nested error body.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Collection wraps a list response in Graph's "value" envelope.
type Collection[T any] struct {
	Value []T `json:"value"`
}

// ErrorResponse is the Graph-style top-level error envelope.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}
