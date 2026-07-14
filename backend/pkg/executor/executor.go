// Package executor is the single, server-side graph interpreter used for every workflow
// run — manual, scheduled, or event-triggered alike. It never runs in the frontend.
package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/owncloud/ocis-workflows/pkg/llm"
	"github.com/owncloud/ocis-workflows/pkg/model"
	"github.com/owncloud/ocis-workflows/pkg/notify"
)

// LLMClient completes chat prompts. Satisfied by *llm.Client.
type LLMClient interface {
	Complete(ctx context.Context, messages []llm.Message, modelOverride string, maxTokens int) (string, error)
}

// FileClient performs file operations in the caller's own space. Satisfied by *webdavfile.Client.
type FileClient interface {
	GetContent(ctx context.Context, token, davPath string) ([]byte, string, error)
	Move(ctx context.Context, token, davPath, destDavPath string) error
	Copy(ctx context.Context, token, davPath, destDavPath string) error
	Comment(ctx context.Context, token, davPath, text string) error
}

// GraphClient performs Graph-API-only operations (tags have no WebDAV equivalent).
// Satisfied by *ocisclient.Client.
type GraphClient interface {
	ResolveItemID(ctx context.Context, token, davPath string) (string, error)
	AssignTag(ctx context.Context, token, itemID, tag string) error
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

// Run executes wf's graph, starting from its trigger node, using token for every oCIS API
// call (WebDAV/Graph) and the executor's own configured LLM endpoint for every llm node.
// resourcePath is the WebDAV path of the file this run operates on — optional for graphs
// that don't reference {{file.*}} or perform file actions.
func (e *Executor) Run(ctx context.Context, token string, wf model.WorkflowDefinition, triggeredBy, resourcePath string) *model.ExecutionRecord {
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
		content, name, err := e.files.GetContent(ctx, token, resourcePath)
		if err != nil {
			e.log.Warn("run: could not read target file, continuing without file context", "error", err)
		} else {
			vars["file.name"] = name
			vars["file.content"] = string(content)
		}
	}

	failed := false
	for _, node := range e.orderedNodes(wf.Graph) {
		if node.Type == "trigger" {
			continue
		}

		result := model.NodeResult{NodeID: node.ID, Status: "succeeded"}
		var err error

		switch node.Type {
		case "llm":
			err = e.runLLM(ctx, node, vars, &result)
		case "action":
			currentPath, err = e.runAction(ctx, token, node, vars, currentPath, &result)
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

// orderedNodes walks the graph from its trigger node following edges, so nodes execute in
// the order the user chained them. Node/edge "condition" fields are stored but not yet
// evaluated — every reachable node always runs. Deferred, not forgotten.
func (e *Executor) orderedNodes(graph model.WorkflowGraph) []model.WorkflowNode {
	byID := make(map[string]model.WorkflowNode, len(graph.Nodes))
	for _, n := range graph.Nodes {
		byID[n.ID] = n
	}

	outgoing := make(map[string][]string)
	for _, edge := range graph.Edges {
		outgoing[edge.Source] = append(outgoing[edge.Source], edge.Target)
	}

	var start string
	for _, n := range graph.Nodes {
		if n.Type == "trigger" {
			start = n.ID
			break
		}
	}
	if start == "" {
		return nil
	}

	var ordered []model.WorkflowNode
	visited := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		if n, ok := byID[id]; ok {
			ordered = append(ordered, n)
		}
		queue = append(queue, outgoing[id]...)
	}
	return ordered
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

func (e *Executor) runAction(ctx context.Context, token string, node model.WorkflowNode, vars map[string]string, currentPath string, result *model.NodeResult) (string, error) {
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
		itemID, err := e.graph.ResolveItemID(ctx, token, currentPath)
		if err != nil {
			return currentPath, err
		}
		if err := e.graph.AssignTag(ctx, token, itemID, tag); err != nil {
			return currentPath, err
		}
		result.Output = tag
		return currentPath, nil

	case "comment":
		text := param("comment")
		if text == "" || currentPath == "" {
			return currentPath, fmt.Errorf("comment action needs both a target file and comment text")
		}
		if err := e.files.Comment(ctx, token, currentPath, text); err != nil {
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
			err = e.files.Move(ctx, token, currentPath, destPath)
		} else {
			err = e.files.Copy(ctx, token, currentPath, destPath)
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
		if err := e.files.Move(ctx, token, currentPath, destPath); err != nil {
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
