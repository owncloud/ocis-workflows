package executor

import (
	"context"
	"log/slog"
	"testing"

	"github.com/LukasHirt/ocis-workflows/pkg/llm"
	"github.com/LukasHirt/ocis-workflows/pkg/model"
)

type fakeLLM struct {
	response string
	err      error
	lastReq  []llm.Message
}

func (f *fakeLLM) Complete(_ context.Context, messages []llm.Message, _ string, _ int) (string, error) {
	f.lastReq = messages
	return f.response, f.err
}

type fakeFiles struct {
	content   string
	name      string
	moved     [2]string
	commented [2]string
}

func (f *fakeFiles) GetContent(_ context.Context, _, _ string) ([]byte, string, error) {
	return []byte(f.content), f.name, nil
}
func (f *fakeFiles) Move(_ context.Context, _, from, to string) error {
	f.moved = [2]string{from, to}
	return nil
}
func (f *fakeFiles) Copy(_ context.Context, _, from, to string) error {
	f.moved = [2]string{from, to}
	return nil
}
func (f *fakeFiles) Comment(_ context.Context, _, path, text string) error {
	f.commented = [2]string{path, text}
	return nil
}

type fakeGraph struct {
	taggedPath string
	taggedWith string
}

func (f *fakeGraph) ResolveItemID(_ context.Context, _, davPath string) (string, error) {
	return "item-for-" + davPath, nil
}
func (f *fakeGraph) AssignTag(_ context.Context, _, itemID, tag string) error {
	f.taggedPath = itemID
	f.taggedWith = tag
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func testWorkflow() model.WorkflowDefinition {
	return model.WorkflowDefinition{
		ID: "wf-1",
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "llm-1", Type: "llm", Data: map[string]any{"prompt": "Summarize {{file.content}}"}},
				{ID: "action-1", Type: "action", Data: map[string]any{
					"actionType":   "tag",
					"actionParams": map[string]any{"tag": "summary:{{llm.output}}"},
				}},
			},
			Edges: []model.WorkflowEdge{
				{ID: "e1", Source: "trigger", Target: "llm-1"},
				{ID: "e2", Source: "llm-1", Target: "action-1"},
			},
		},
	}
}

func TestRunTriggerLLMAction(t *testing.T) {
	fLLM := &fakeLLM{response: "a short summary"}
	fFiles := &fakeFiles{content: "file body", name: "doc.txt"}
	fGraph := &fakeGraph{}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", testWorkflow(), "manual", "/Docs/doc.txt")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if len(record.NodeResults) != 2 {
		t.Fatalf("expected 2 node results (llm + action), got %d", len(record.NodeResults))
	}
	if record.NodeResults[0].NodeID != "llm-1" || record.NodeResults[0].Output != "a short summary" {
		t.Fatalf("unexpected llm node result: %+v", record.NodeResults[0])
	}
	if fGraph.taggedWith != "summary:a short summary" {
		t.Fatalf("expected tag templated with llm output, got %q", fGraph.taggedWith)
	}
	if fGraph.taggedPath != "item-for-/Docs/doc.txt" {
		t.Fatalf("expected tag applied to resolved item id, got %q", fGraph.taggedPath)
	}

	// The LLM prompt itself must have had {{file.content}} substituted.
	if len(fLLM.lastReq) != 1 || fLLM.lastReq[0].Content != "Summarize file body" {
		t.Fatalf("expected rendered prompt, got %+v", fLLM.lastReq)
	}
}

func TestRunStopsOnNodeFailure(t *testing.T) {
	fLLM := &fakeLLM{err: errFakeLLM}
	fFiles := &fakeFiles{content: "x", name: "x.txt"}
	fGraph := &fakeGraph{}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", testWorkflow(), "manual", "/x.txt")

	if record.Status != "failed" {
		t.Fatalf("expected status failed, got %s", record.Status)
	}
	if len(record.NodeResults) != 1 {
		t.Fatalf("expected execution to stop after the failing llm node, got %d results", len(record.NodeResults))
	}
	if fGraph.taggedWith != "" {
		t.Fatal("action node must not have run after the llm node failed")
	}
}

var errFakeLLM = &testError{"llm unavailable"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestRender(t *testing.T) {
	vars := map[string]string{"file.name": "a.txt", "llm.output": "ok"}
	got := render("{{file.name}} -> {{llm.output}}", vars)
	if got != "a.txt -> ok" {
		t.Fatalf("render() = %q", got)
	}
}

func TestBaseNameDirName(t *testing.T) {
	if got := baseName("/Invoices/foo.pdf"); got != "foo.pdf" {
		t.Fatalf("baseName() = %q", got)
	}
	if got := dirName("/Invoices/foo.pdf"); got != "Invoices" {
		t.Fatalf("dirName() = %q", got)
	}
	if got := dirName("foo.pdf"); got != "" {
		t.Fatalf("dirName() for root-level file = %q, want empty", got)
	}
}
