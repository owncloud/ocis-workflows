// Package executor is the single, server-side graph interpreter used for every workflow
// run — manual, scheduled, or event-triggered alike. It never runs in the frontend.
package executor

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/owncloud/ocis-workflows/pkg/llm"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/notify"
	"github.com/owncloud/ocis-workflows/pkg/textextract"
)

// LLMClient completes chat prompts. Satisfied by *llm.Client.
type LLMClient interface {
	Complete(ctx context.Context, messages []llm.Message, modelOverride string, maxTokens int) (string, error)
}

// FileClient performs file operations in the caller's own space. Satisfied by *webdavfile.Client.
type FileClient interface {
	GetContent(ctx context.Context, authHeader, davPath string) ([]byte, string, error)
	Move(ctx context.Context, authHeader, davPath, destDavPath string) error
	Copy(ctx context.Context, authHeader, davPath, destDavPath string) error
	Comment(ctx context.Context, authHeader, davPath, text string) error
}

// GraphClient performs Graph-API-only operations (tags have no WebDAV equivalent).
// Satisfied by *ocisclient.Client.
type GraphClient interface {
	ResolveItemID(ctx context.Context, authHeader, davPath string) (string, error)
	AssignTag(ctx context.Context, authHeader, itemID, tag string) error
}

// Executor runs a WorkflowDefinition's graph against a target resource.
type Executor struct {
	llm   LLMClient
	files FileClient
	graph GraphClient
	log   *slog.Logger
}

// New builds an Executor.
func New(llmClient LLMClient, files FileClient, graph GraphClient, log *slog.Logger) *Executor {
	return &Executor{llm: llmClient, files: files, graph: graph, log: log}
}

// Run executes wf's graph, starting from its trigger node, using authHeader for every oCIS API
// call (WebDAV/Graph) and the executor's own configured LLM endpoint for every llm node.
// resourcePath is the WebDAV path of the file this run operates on — optional for graphs
// that don't reference {{file.*}} or perform file actions.
//
// Traversal follows every outgoing edge unconditionally for every node kind except
// "condition", which has exactly two outputs ("true"/"false"): only the edge whose
// SourceHandle matches the evaluated comparison is followed (see runCondition and
// targetsFor). Node/edge "condition" *fields* (WorkflowNodeData.Condition,
// EdgeData.Condition) remain a separate, unrelated, still-unimplemented free-form
// per-node/per-edge gate — unaffected by this.
func (e *Executor) Run(ctx context.Context, authHeader string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord {
	record := &model.ExecutionRecord{
		ID:              uuid.NewString(),
		WorkflowID:      wf.ID,
		TriggeredBy:     triggeredBy,
		Status:          "running",
		StartedDateTime: time.Now().UTC().Format(time.RFC3339Nano),
		NodeResults:     []model.NodeResult{},
	}

	vars := map[string]string{}
	currentPath := resourcePath
	if resourcePath != "" {
		content, name, err := e.files.GetContent(ctx, authHeader, resourcePath)
		if err != nil {
			e.log.Warn("run: could not read target file, continuing without file context", "error", err)
		} else {
			vars["file.name"] = name
			vars["file.content"] = string(content)
		}
	}

	byID, outgoing, start := indexGraph(wf.Graph)

	failed := false
	if start != "" {
		visited := map[string]bool{}
		queue := []string{start}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if visited[id] {
				continue
			}
			visited[id] = true

			node, ok := byID[id]
			if !ok {
				continue
			}

			if node.Type == "trigger" {
				queue = append(queue, targetsFor(outgoing[id], nil)...)
				continue
			}

			result := model.NodeResult{NodeID: node.ID, Status: "succeeded"}
			var err error
			// matchedHandle is nil for every node kind except "condition": nil means
			// "follow every outgoing edge" (today's behavior, unchanged for
			// trigger/llm/action nodes, all of which only ever have one output anyway).
			// For a condition node it's set to the evaluated branch ("true"/"false"),
			// so only the edge(s) whose SourceHandle matches get followed.
			var matchedHandle *string

			switch node.Type {
			case "llm":
				err = e.runLLM(ctx, node, vars, &result)
			case "extractText":
				err = e.runExtractText(node, vars, &result)
			case "action":
				currentPath, err = e.runAction(ctx, authHeader, node, vars, currentPath, &result)
			case "condition":
				var outcome bool
				outcome, err = e.runCondition(node, vars, &result)
				if err == nil {
					handle := "false"
					if outcome {
						handle = "true"
					}
					matchedHandle = &handle
				}
			default:
				err = fmt.Errorf("unknown node type %q", node.Type)
			}

			if err != nil {
				result.Status = "failed"
				result.Error = &model.ErrorDetail{Code: "nodeFailed", Message: err.Error()}
				record.NodeResults = append(record.NodeResults, result)
				failed = true
				break
			}
			record.NodeResults = append(record.NodeResults, result)
			queue = append(queue, targetsFor(outgoing[id], matchedHandle)...)
		}
	}

	if failed {
		record.Status = "failed"
		record.Error = &model.ErrorDetail{Code: "executionFailed", Message: "one or more nodes failed"}
	} else {
		record.Status = "succeeded"
	}
	record.CompletedDateTime = time.Now().UTC().Format(time.RFC3339Nano)
	return record
}

// indexGraph builds lookup structures for traversal: nodes by id, outgoing edges by
// source node id, and the id of the trigger node execution starts from (empty if none).
func indexGraph(graph model.WorkflowGraph) (byID map[string]model.WorkflowNode, outgoing map[string][]model.WorkflowEdge, start string) {
	byID = make(map[string]model.WorkflowNode, len(graph.Nodes))
	for _, n := range graph.Nodes {
		byID[n.ID] = n
	}

	outgoing = make(map[string][]model.WorkflowEdge)
	for _, edge := range graph.Edges {
		outgoing[edge.Source] = append(outgoing[edge.Source], edge)
	}

	for _, n := range graph.Nodes {
		if n.Type == "trigger" {
			start = n.ID
			break
		}
	}
	return byID, outgoing, start
}

// targetsFor resolves which of a node's outgoing edges to follow next. handle == nil
// means "every node kind except condition": follow every outgoing edge, same as before
// branching existed. A non-nil handle (only ever set for a condition node's evaluated
// "true"/"false" result) restricts traversal to edges whose SourceHandle matches; if the
// evaluated branch has no wired edge, this simply returns nothing — a clean dead end for
// that branch, not an error, since a condition node with only one branch connected (e.g.
// "if spam, delete; otherwise do nothing") is a perfectly valid workflow shape.
func targetsFor(edges []model.WorkflowEdge, handle *string) []string {
	var targets []string
	for _, edge := range edges {
		if handle != nil && edge.SourceHandle != *handle {
			continue
		}
		targets = append(targets, edge.Target)
	}
	return targets
}

func (e *Executor) runLLM(ctx context.Context, node model.WorkflowNode, vars map[string]string, result *model.NodeResult) error {
	rawPrompt, _ := node.Data["prompt"].(string)
	prompt := render(rawPrompt, vars)
	if prompt == "" {
		return fmt.Errorf("llm node has no prompt configured")
	}

	modelOverride, _ := node.Data["model"].(string)
	output, err := e.llm.Complete(ctx, []llm.Message{{Role: "user", Content: prompt}}, modelOverride, 0)
	if err != nil {
		return err
	}

	vars["llm.output"] = output
	result.Output = output
	return nil
}

// runExtractText converts the triggering file's content (vars["file.content"], loaded
// by Run for every execution that has a target resource) into plain text when it's a
// supported binary document format (currently PDF and DOCX; see pkg/textextract for
// what's out of scope, notably image OCR). Plain-text files pass through unchanged.
// The result is written to vars[outputVariable] — vars["file.text"] by default, or
// node.Data["outputVariable"] when set — so a later node (typically an LLM Prompt) can
// reference it via {{file.text}}.
func (e *Executor) runExtractText(node model.WorkflowNode, vars map[string]string, result *model.NodeResult) error {
	outputVar, _ := node.Data["outputVariable"].(string)
	if outputVar == "" {
		outputVar = "file.text"
	}

	text, err := textextract.Extract(vars["file.name"], []byte(vars["file.content"]))
	if err != nil {
		return err
	}

	vars[outputVar] = text
	result.Output = fmt.Sprintf("%d characters extracted", len(text))
	return nil
}

// runCondition renders a condition node's left/right templates against vars (exactly
// like every action param already is) and evaluates the comparison. It returns the
// boolean outcome, which the caller uses to pick the matching "true"/"false" outgoing
// edge; it never itself decides traversal. result.Output is set to "true"/"false" for
// visibility in the execution log — no new vars entries are added.
func (e *Executor) runCondition(node model.WorkflowNode, vars map[string]string, result *model.NodeResult) (bool, error) {
	rawLeft, _ := node.Data["left"].(string)
	left := render(rawLeft, vars)

	rawRight, _ := node.Data["right"].(string)
	right := render(rawRight, vars)

	operator, _ := node.Data["operator"].(string)

	var outcome bool
	switch operator {
	case "equals":
		outcome = left == right
	case "notEquals":
		outcome = left != right
	case "contains":
		outcome = strings.Contains(left, right)
	case "notContains":
		outcome = !strings.Contains(left, right)
	case "matches":
		matched, err := regexp.MatchString(right, left)
		if err != nil {
			return false, fmt.Errorf("condition node has an invalid regex pattern %q: %w", right, err)
		}
		outcome = matched
	default:
		return false, fmt.Errorf("unknown condition operator %q", operator)
	}

	if outcome {
		result.Output = "true"
	} else {
		result.Output = "false"
	}
	return outcome, nil
}

func (e *Executor) runAction(ctx context.Context, authHeader string, node model.WorkflowNode, vars map[string]string, currentPath string, result *model.NodeResult) (string, error) {
	actionType, _ := node.Data["actionType"].(string)
	params, _ := node.Data["actionParams"].(map[string]any)
	param := func(key string) string {
		if params == nil {
			return ""
		}
		v, _ := params[key].(string)
		return render(v, vars)
	}

	switch actionType {
	case "tag":
		tag := param("tag")
		if tag == "" || currentPath == "" {
			return currentPath, fmt.Errorf("tag action needs both a target file and a tag value")
		}
		itemID, err := e.graph.ResolveItemID(ctx, authHeader, currentPath)
		if err != nil {
			return currentPath, err
		}
		if err := e.graph.AssignTag(ctx, authHeader, itemID, tag); err != nil {
			return currentPath, err
		}
		result.Output = tag
		return currentPath, nil

	case "comment":
		text := param("comment")
		if text == "" || currentPath == "" {
			return currentPath, fmt.Errorf("comment action needs both a target file and comment text")
		}
		if err := e.files.Comment(ctx, authHeader, currentPath, text); err != nil {
			return currentPath, err
		}
		result.Output = text
		return currentPath, nil

	case "move", "copy":
		dest := param("destination")
		if dest == "" || currentPath == "" {
			return currentPath, fmt.Errorf("%s action needs both a target file and a destination", actionType)
		}
		destPath := strings.TrimRight(dest, "/") + "/" + baseName(currentPath)
		var err error
		if actionType == "move" {
			err = e.files.Move(ctx, authHeader, currentPath, destPath)
		} else {
			err = e.files.Copy(ctx, authHeader, currentPath, destPath)
		}
		if err != nil {
			return currentPath, err
		}
		result.Output = destPath
		if actionType == "move" {
			currentPath = destPath
		}
		return currentPath, nil

	case "rename":
		newName := param("newName")
		if newName == "" || currentPath == "" {
			return currentPath, fmt.Errorf("rename action needs both a target file and a new name")
		}
		destPath := dirName(currentPath) + "/" + newName
		if err := e.files.Move(ctx, authHeader, currentPath, destPath); err != nil {
			return currentPath, err
		}
		result.Output = destPath
		return destPath, nil

	case "notify":
		target := param("target")
		if target == "" {
			return currentPath, fmt.Errorf("notify action needs a target")
		}
		message := param("message")
		if err := notify.Send(ctx, target, "Workflows", message); err != nil {
			return currentPath, err
		}
		result.Output = "sent"
		return currentPath, nil

	default:
		return currentPath, fmt.Errorf("unknown action type %q", actionType)
	}
}

func render(tmpl string, vars map[string]string) string {
	out := tmpl
	for key, value := range vars {
		out = strings.ReplaceAll(out, "{{"+key+"}}", value)
	}
	return out
}

func baseName(davPath string) string {
	parts := strings.Split(strings.Trim(davPath, "/"), "/")
	return parts[len(parts)-1]
}

func dirName(davPath string) string {
	trimmed := strings.Trim(davPath, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return ""
	}
	return trimmed[:idx]
}
