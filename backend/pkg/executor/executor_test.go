package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/owncloud/ocis-workflows/pkg/llm"
	"github.com/owncloud/ocis-workflows/pkg/model"
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

	sharedItemID string
	sharedWith   string
	sharedRole   string
	shareErr     error
}

func (f *fakeGraph) ResolveItemID(_ context.Context, _, davPath string) (string, error) {
	return "item-for-" + davPath, nil
}
func (f *fakeGraph) AssignTag(_ context.Context, _, itemID, tag string) error {
	f.taggedPath = itemID
	f.taggedWith = tag
	return nil
}
func (f *fakeGraph) Share(_ context.Context, _, itemID, recipient, role string) error {
	if f.shareErr != nil {
		return f.shareErr
	}
	f.sharedItemID = itemID
	f.sharedWith = recipient
	f.sharedRole = role
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

// TestActionOutputIsReferenceableDownstream proves that an action node's result is not just
// recorded on the NodeResult but also written into the shared vars map under a
// "<actionType>.output" key (mirroring llm.output), so a later node's {{...}} template can
// reference what an earlier action node produced.
func TestActionOutputIsReferenceableDownstream(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "file body", name: "doc.txt"}
	fGraph := &fakeGraph{}

	wf := model.WorkflowDefinition{
		ID: "wf-2",
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "action-tag", Type: "action", Data: map[string]any{
					"actionType":   "tag",
					"actionParams": map[string]any{"tag": "reviewed"},
				}},
				{ID: "action-comment", Type: "action", Data: map[string]any{
					"actionType":   "comment",
					"actionParams": map[string]any{"comment": "applied tag: {{tag.output}}"},
				}},
			},
			Edges: []model.WorkflowEdge{
				{ID: "e1", Source: "trigger", Target: "action-tag"},
				{ID: "e2", Source: "action-tag", Target: "action-comment"},
			},
		},
	}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", wf, "manual", "/Docs/doc.txt")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if fFiles.commented[1] != "applied tag: reviewed" {
		t.Fatalf("expected downstream node to see the tag action's output via {{tag.output}}, got comment %q", fFiles.commented[1])
	}
}

func shareWorkflow() model.WorkflowDefinition {
	return model.WorkflowDefinition{
		ID: "wf-share",
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "action-1", Type: "action", Data: map[string]any{
					"actionType":   "share",
					"actionParams": map[string]any{"recipient": "{{file.name}}-owner@example.com", "role": "editor"},
				}},
			},
			Edges: []model.WorkflowEdge{
				{ID: "e1", Source: "trigger", Target: "action-1"},
			},
		},
	}
}

func TestRunShareAction(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "file body", name: "invoice.pdf"}
	fGraph := &fakeGraph{}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", shareWorkflow(), "manual", "/Invoices/invoice.pdf")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if len(record.NodeResults) != 1 {
		t.Fatalf("expected 1 node result (action), got %d", len(record.NodeResults))
	}

	// The recipient template must have been rendered against vars (here, {{file.name}}).
	if fGraph.sharedWith != "invoice.pdf-owner@example.com" {
		t.Fatalf("expected rendered recipient, got %q", fGraph.sharedWith)
	}
	if fGraph.sharedRole != "editor" {
		t.Fatalf("expected role %q, got %q", "editor", fGraph.sharedRole)
	}
	if fGraph.sharedItemID != "item-for-/Invoices/invoice.pdf" {
		t.Fatalf("expected share applied to resolved item id, got %q", fGraph.sharedItemID)
	}
	if record.NodeResults[0].Output != "invoice.pdf-owner@example.com" {
		t.Fatalf("expected node result output to be the rendered recipient, got %v", record.NodeResults[0].Output)
	}
}

func TestRunShareActionDefaultsToViewerRole(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "x", name: "x.txt"}
	fGraph := &fakeGraph{}

	wf := model.WorkflowDefinition{
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "action-1", Type: "action", Data: map[string]any{
					"actionType":   "share",
					"actionParams": map[string]any{"recipient": "accounting@example.com"},
				}},
			},
			Edges: []model.WorkflowEdge{{ID: "e1", Source: "trigger", Target: "action-1"}},
		},
	}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", wf, "manual", "/x.txt")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if fGraph.sharedRole != "viewer" {
		t.Fatalf("expected default role %q, got %q", "viewer", fGraph.sharedRole)
	}
}

func TestRunShareActionRequiresRecipient(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "x", name: "x.txt"}
	fGraph := &fakeGraph{}

	wf := model.WorkflowDefinition{
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "action-1", Type: "action", Data: map[string]any{
					"actionType":   "share",
					"actionParams": map[string]any{},
				}},
			},
			Edges: []model.WorkflowEdge{{ID: "e1", Source: "trigger", Target: "action-1"}},
		},
	}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", wf, "manual", "/x.txt")

	if record.Status != "failed" {
		t.Fatalf("expected status failed when recipient is missing, got %s", record.Status)
	}
	if fGraph.sharedWith != "" {
		t.Fatal("Share must not have been called without a recipient")
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

// testWorkflowExtractText wires trigger -> extractText -> llm, with the llm prompt
// referencing whichever output variable extractText is expected to populate (the
// default file.text, or outputVar if non-empty), so a rendered-prompt assertion proves
// the variable really landed in vars, not just that the node "succeeded".
func testWorkflowExtractText(outputVar string) (model.WorkflowDefinition, string) {
	varName := "file.text"
	extractData := map[string]any{}
	if outputVar != "" {
		varName = outputVar
		extractData["outputVariable"] = outputVar
	}

	wf := model.WorkflowDefinition{
		ID: "wf-extract",
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "extract-1", Type: "extractText", Data: extractData},
				{ID: "llm-1", Type: "llm", Data: map[string]any{"prompt": "Summarize {{" + varName + "}}"}},
			},
			Edges: []model.WorkflowEdge{
				{ID: "e1", Source: "trigger", Target: "extract-1"},
				{ID: "e2", Source: "extract-1", Target: "llm-1"},
			},
		},
	}
	return wf, varName
}

func TestRunExtractTextPlainTextPassthroughDefaultOutputVar(t *testing.T) {
	fLLM := &fakeLLM{response: "ok"}
	fFiles := &fakeFiles{content: "already plain text", name: "notes.txt"}
	fGraph := &fakeGraph{}

	wf, _ := testWorkflowExtractText("")
	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", wf, "manual", "/notes.txt")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if len(fLLM.lastReq) != 1 || fLLM.lastReq[0].Content != "Summarize already plain text" {
		t.Fatalf("expected file.text to hold the passed-through plain text, got llm request %+v", fLLM.lastReq)
	}

	extractResult := record.NodeResults[0]
	if extractResult.NodeID != "extract-1" {
		t.Fatalf("expected extract-1 to be the first node result, got %+v", extractResult)
	}
	wantOutput := fmt.Sprintf("%d characters extracted", len("already plain text"))
	if extractResult.Output != wantOutput {
		t.Fatalf("extract-1 result.Output = %q, want %q", extractResult.Output, wantOutput)
	}
}

func TestRunExtractTextCustomOutputVariable(t *testing.T) {
	fLLM := &fakeLLM{response: "ok"}
	fFiles := &fakeFiles{content: "custom var content", name: "notes.txt"}
	fGraph := &fakeGraph{}

	wf, _ := testWorkflowExtractText("myVar")
	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", wf, "manual", "/notes.txt")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if len(fLLM.lastReq) != 1 || fLLM.lastReq[0].Content != "Summarize custom var content" {
		t.Fatalf("expected the custom output variable to hold the extracted text, got llm request %+v", fLLM.lastReq)
	}
}

func TestRunExtractTextPDFEndToEnd(t *testing.T) {
	fLLM := &fakeLLM{response: "ok"}
	fFiles := &fakeFiles{content: string(buildTestPDFFixture("Hello From PDF")), name: "invoice.pdf"}
	fGraph := &fakeGraph{}

	wf, _ := testWorkflowExtractText("")
	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", wf, "manual", "/invoice.pdf")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if len(fLLM.lastReq) != 1 {
		t.Fatalf("expected one rendered llm request, got %+v", fLLM.lastReq)
	}
	rendered := strings.Join(strings.Fields(fLLM.lastReq[0].Content), " ")
	if !strings.Contains(rendered, "Hello From PDF") {
		t.Fatalf("expected the PDF's real text in the rendered prompt, got %q", fLLM.lastReq[0].Content)
	}
}

func TestRunExtractTextNodeFailsOnCorruptDocument(t *testing.T) {
	fLLM := &fakeLLM{response: "should not be called"}
	fFiles := &fakeFiles{content: "not a real pdf", name: "broken.pdf"}
	fGraph := &fakeGraph{}

	wf, _ := testWorkflowExtractText("")
	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", wf, "manual", "/broken.pdf")

	if record.Status != "failed" {
		t.Fatalf("expected status failed for a corrupt pdf, got %s", record.Status)
	}
	if len(record.NodeResults) != 1 || record.NodeResults[0].NodeID != "extract-1" {
		t.Fatalf("expected execution to stop at the failing extractText node, got %+v", record.NodeResults)
	}
	if len(fLLM.lastReq) != 0 {
		t.Fatal("llm node must not have run after the extractText node failed")
	}
}

// buildTestPDFFixture constructs a minimal, valid single-page PDF containing text,
// with a correct xref table computed from real byte offsets. This mirrors
// pkg/textextract's own test fixture builder; it's kept small and local here rather
// than exported from textextract, since exporting a fixture-only helper from the
// production package just to save a few duplicated lines in one other test file isn't
// worth the added public surface.
func buildTestPDFFixture(text string) []byte {
	var buf bytes.Buffer
	offsets := make([]int, 6)
	buf.WriteString("%PDF-1.4\n")

	write := func(i int, s string) {
		offsets[i] = buf.Len()
		buf.WriteString(s)
	}

	write(1, "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	write(2, "2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	write(3, "3 0 obj\n<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> "+
		"/MediaBox [0 0 200 200] /Contents 5 0 R >>\nendobj\n")
	write(4, "4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	content := fmt.Sprintf("BT /F1 24 Tf 10 100 Td (%s) Tj ET", text)
	write(5, fmt.Sprintf("5 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content))

	xrefStart := buf.Len()
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	buf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n")
	fmt.Fprintf(&buf, "%d\n", xrefStart)
	buf.WriteString("%%EOF")

	return buf.Bytes()
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
