package executor

import (
	"context"
	"testing"

	"github.com/owncloud/ocis-workflows/pkg/model"
)

// conditionWorkflow builds trigger -> cond-1 -> {action-true (tag), action-false (comment)},
// with the condition wired to the given operator/right value against {{file.name}}.
func conditionWorkflow(operator, right string) model.WorkflowDefinition {
	return model.WorkflowDefinition{
		ID: "wf-cond",
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "cond-1", Type: "condition", Data: map[string]any{
					"left": "{{file.name}}", "operator": operator, "right": right,
				}},
				{ID: "action-true", Type: "action", Data: map[string]any{
					"actionType":   "tag",
					"actionParams": map[string]any{"tag": "true-branch"},
				}},
				{ID: "action-false", Type: "action", Data: map[string]any{
					"actionType":   "comment",
					"actionParams": map[string]any{"comment": "false-branch"},
				}},
			},
			Edges: []model.WorkflowEdge{
				{ID: "e1", Source: "trigger", Target: "cond-1"},
				{ID: "e2", Source: "cond-1", Target: "action-true", SourceHandle: "true"},
				{ID: "e3", Source: "cond-1", Target: "action-false", SourceHandle: "false"},
			},
		},
	}
}

func TestRunConditionOperators(t *testing.T) {
	e := New(&fakeLLM{}, &fakeFiles{}, &fakeGraph{}, discardLogger())
	vars := map[string]string{"llm.output": "invoice"}

	cases := []struct {
		name             string
		operator         string
		right            string
		want             bool
		wantErrSubstring string
	}{
		{name: "equals true", operator: "equals", right: "invoice", want: true},
		{name: "equals false", operator: "equals", right: "receipt", want: false},
		{name: "notEquals true", operator: "notEquals", right: "receipt", want: true},
		{name: "notEquals false", operator: "notEquals", right: "invoice", want: false},
		{name: "contains true", operator: "contains", right: "voi", want: true},
		{name: "contains false", operator: "contains", right: "xyz", want: false},
		{name: "notContains true", operator: "notContains", right: "xyz", want: true},
		{name: "notContains false", operator: "notContains", right: "voi", want: false},
		{name: "matches true", operator: "matches", right: "^inv.*", want: true},
		{name: "matches false", operator: "matches", right: "^rec.*", want: false},
		{name: "matches invalid regex", operator: "matches", right: "(unterminated", wantErrSubstring: "invalid regex"},
		{name: "unknown operator", operator: "greaterThan", right: "x", wantErrSubstring: "unknown condition operator"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := model.WorkflowNode{
				ID:   "cond-1",
				Type: "condition",
				Data: map[string]any{"left": "{{llm.output}}", "operator": tc.operator, "right": tc.right},
			}
			result := model.NodeResult{}
			got, err := e.runCondition(node, vars, &result)

			if tc.wantErrSubstring != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got nil", tc.wantErrSubstring)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("runCondition() = %v, want %v", got, tc.want)
			}
			wantOutput := "false"
			if tc.want {
				wantOutput = "true"
			}
			if result.Output != wantOutput {
				t.Fatalf("result.Output = %v, want %q", result.Output, wantOutput)
			}
		})
	}
}

func TestRunConditionTrueBranchOnlyExecutesTrueBranch(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "x", name: "invoice.pdf"}
	fGraph := &fakeGraph{}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", conditionWorkflow("equals", "invoice.pdf"), "manual", "/invoice.pdf")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if len(record.NodeResults) != 2 {
		t.Fatalf("expected 2 node results (condition + true-branch action), got %d: %+v", len(record.NodeResults), record.NodeResults)
	}
	if record.NodeResults[0].NodeID != "cond-1" || record.NodeResults[0].Output != "true" {
		t.Fatalf("unexpected condition node result: %+v", record.NodeResults[0])
	}
	if record.NodeResults[1].NodeID != "action-true" {
		t.Fatalf("expected the true-branch action to run, got %+v", record.NodeResults[1])
	}
	if fGraph.taggedWith != "true-branch" {
		t.Fatalf("expected true-branch tag action to run, got taggedWith=%q", fGraph.taggedWith)
	}
	if fFiles.commented != ([2]string{}) {
		t.Fatalf("false-branch comment action must not have run, got commented=%+v", fFiles.commented)
	}
}

func TestRunConditionFalseBranchOnlyExecutesFalseBranch(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "x", name: "receipt.pdf"}
	fGraph := &fakeGraph{}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", conditionWorkflow("equals", "invoice.pdf"), "manual", "/receipt.pdf")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if len(record.NodeResults) != 2 {
		t.Fatalf("expected 2 node results (condition + false-branch action), got %d: %+v", len(record.NodeResults), record.NodeResults)
	}
	if record.NodeResults[0].NodeID != "cond-1" || record.NodeResults[0].Output != "false" {
		t.Fatalf("unexpected condition node result: %+v", record.NodeResults[0])
	}
	if record.NodeResults[1].NodeID != "action-false" {
		t.Fatalf("expected the false-branch action to run, got %+v", record.NodeResults[1])
	}
	if fFiles.commented != ([2]string{"/receipt.pdf", "false-branch"}) {
		t.Fatalf("expected false-branch comment action to run, got commented=%+v", fFiles.commented)
	}
	if fGraph.taggedWith != "" {
		t.Fatalf("true-branch tag action must not have run, got taggedWith=%q", fGraph.taggedWith)
	}
}

// TestRunConditionDeadEndsCleanlyWhenOnlyOneBranchIsWired covers the "condition node
// with only one branch wired up" shape (e.g. "if spam, delete; otherwise do nothing").
// Evaluating to the branch with no outgoing edge must be a clean dead end, not an error.
func TestRunConditionDeadEndsCleanlyWhenOnlyOneBranchIsWired(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "x", name: "receipt.pdf"}
	fGraph := &fakeGraph{}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	wf := model.WorkflowDefinition{
		ID: "wf-cond-single-branch",
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "cond-1", Type: "condition", Data: map[string]any{
					"left": "{{file.name}}", "operator": "equals", "right": "invoice.pdf",
				}},
				{ID: "action-true", Type: "action", Data: map[string]any{
					"actionType":   "tag",
					"actionParams": map[string]any{"tag": "true-branch"},
				}},
			},
			Edges: []model.WorkflowEdge{
				{ID: "e1", Source: "trigger", Target: "cond-1"},
				{ID: "e2", Source: "cond-1", Target: "action-true", SourceHandle: "true"},
				// No edge for the "false" branch at all.
			},
		},
	}

	// file.name is "receipt.pdf" -> condition evaluates to false -> no wired edge for
	// "false" -> execution must stop cleanly, without error, after just the condition node.
	record := e.Run(context.Background(), "token", wf, "manual", "/receipt.pdf")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded (dead end, not an error), got %s (error: %v)", record.Status, record.Error)
	}
	if len(record.NodeResults) != 1 {
		t.Fatalf("expected only the condition node to have run, got %d results: %+v", len(record.NodeResults), record.NodeResults)
	}
	if fGraph.taggedWith != "" {
		t.Fatalf("true-branch action must not have run, got taggedWith=%q", fGraph.taggedWith)
	}
}

func TestRunConditionInvalidRegexIsAClearErrorNotAPanic(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "x", name: "invoice.pdf"}
	fGraph := &fakeGraph{}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	record := e.Run(context.Background(), "token", conditionWorkflow("matches", "(unterminated"), "manual", "/invoice.pdf")

	if record.Status != "failed" {
		t.Fatalf("expected status failed, got %s", record.Status)
	}
	if len(record.NodeResults) != 1 || record.NodeResults[0].NodeID != "cond-1" {
		t.Fatalf("expected exactly one (failed) node result for the condition node, got %+v", record.NodeResults)
	}
	if record.NodeResults[0].Status != "failed" || record.NodeResults[0].Error == nil {
		t.Fatalf("expected the condition node result to carry an error, got %+v", record.NodeResults[0])
	}
	if fGraph.taggedWith != "" || fFiles.commented != ([2]string{}) {
		t.Fatal("no downstream action should have run after an invalid regex error")
	}
}

// TestRunFansOutFromNonConditionNode is a regression check: a non-condition node with
// multiple outgoing edges must still run every downstream branch unconditionally, exactly
// like before condition-node support was added.
func TestRunFansOutFromNonConditionNode(t *testing.T) {
	fLLM := &fakeLLM{}
	fFiles := &fakeFiles{content: "body", name: "a.txt"}
	fGraph := &fakeGraph{}

	e := New(fLLM, fFiles, fGraph, discardLogger())
	wf := model.WorkflowDefinition{
		ID: "wf-fanout",
		Graph: model.WorkflowGraph{
			Nodes: []model.WorkflowNode{
				{ID: "trigger", Type: "trigger", Data: map[string]any{}},
				{ID: "action-1", Type: "action", Data: map[string]any{
					"actionType": "tag", "actionParams": map[string]any{"tag": "t1"},
				}},
				{ID: "action-2", Type: "action", Data: map[string]any{
					"actionType": "comment", "actionParams": map[string]any{"comment": "c1"},
				}},
			},
			Edges: []model.WorkflowEdge{
				{ID: "e1", Source: "trigger", Target: "action-1"},
				{ID: "e2", Source: "trigger", Target: "action-2"},
			},
		},
	}

	record := e.Run(context.Background(), "token", wf, "manual", "/a.txt")

	if record.Status != "succeeded" {
		t.Fatalf("expected status succeeded, got %s (error: %v)", record.Status, record.Error)
	}
	if len(record.NodeResults) != 2 {
		t.Fatalf("expected both fanned-out actions to run, got %d results: %+v", len(record.NodeResults), record.NodeResults)
	}
	if fGraph.taggedWith != "t1" {
		t.Fatalf("expected action-1 to run, got taggedWith=%q", fGraph.taggedWith)
	}
	if fFiles.commented[1] != "c1" {
		t.Fatalf("expected action-2 to run, got commented=%+v", fFiles.commented)
	}
}
