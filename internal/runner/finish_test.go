package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

// scriptedModel returns canned responses in order and records what it was sent.
type scriptedModel struct {
	responses []*ai.ChatResponse
	sent      [][]ai.ConversationMessage
	tools     [][]string
}

func (m *scriptedModel) SupportsNativeTools() bool { return true }
func (m *scriptedModel) ReviewDiff(context.Context, ai.ReviewRequest) (string, error) {
	return "", nil
}
func (m *scriptedModel) Chat(context.Context, []ai.Message, string, ai.ChatOpts) (string, error) {
	return "", nil
}
func (m *scriptedModel) ChatWithTools(_ context.Context, msgs []ai.ConversationMessage, tools []ai.ToolDef, _ ai.ChatOpts) (*ai.ChatResponse, error) {
	m.sent = append(m.sent, append([]ai.ConversationMessage(nil), msgs...))
	var names []string
	for _, t := range tools {
		names = append(names, t.Name)
	}
	m.tools = append(m.tools, names)
	if len(m.responses) == 0 {
		return nil, errors.New("script exhausted")
	}
	r := m.responses[0]
	m.responses = m.responses[1:]
	return r, nil
}

func text(s string) *ai.ChatResponse {
	return &ai.ChatResponse{Content: []ai.ContentBlock{
		{Type: "thinking", Thinking: &ai.ThinkingBlock{Type: "thinking", Signature: "sig"}},
		{Type: "text", Text: s}}}
}

func call(id, name string, input map[string]any) *ai.ChatResponse {
	return &ai.ChatResponse{Content: []ai.ContentBlock{{Type: "tool_use", ToolCall: &ai.ToolCall{ID: id, Name: name, Input: input}}}}
}

func TestFinishToolNudgeRefusalAndExtras(t *testing.T) {
	s := openTestDB(t)
	sess, _ := CreateSession(s.db, ModeReview, "o", "r", 1, "pr", "p")

	recorded := 0
	note := RegisteredTool{
		Def: ai.ToolDef{Name: "note", Parameters: jsonSchema(map[string]any{}, nil)},
		Execute: func(context.Context, *Workspace, map[string]any) (string, error) {
			recorded++
			return "noted", nil
		},
	}
	finishCalls := 0
	model := &scriptedModel{responses: []*ai.ChatResponse{
		text("Looks fine to me."), // plain text → nudge
		call("c1", "finish", nil), // refused: nothing recorded yet
		call("c2", "note", nil),   // records something
		call("c3", "finish", nil), // accepted
	}}
	r := New(model, &Workspace{Dir: t.TempDir()}, &LogReporter{}, sess, Config{
		Mode: ModeReview, MaxTurns: 10, ExtraTools: []RegisteredTool{note}, FinishTool: "finish", RequireFinishTool: true,
		OnFinish: func(map[string]any) (string, error) {
			finishCalls++
			if recorded == 0 {
				return "", errors.New("record at least one note first")
			}
			return "rendered review", nil
		},
	})
	// the finish tool itself is declared by the caller in real use
	r.tools.register(RegisteredTool{Def: ai.ToolDef{Name: "finish", Parameters: jsonSchema(map[string]any{}, nil)},
		Execute: func(context.Context, *Workspace, map[string]any) (string, error) { return "", nil }})

	res, err := r.Run(context.Background(), "review this")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusComplete || res.Text != "rendered review" || res.TurnCount != 4 || finishCalls != 2 {
		t.Fatalf("result = %+v, finish calls %d", res, finishCalls)
	}

	// The nudge replays the text answer with its thinking block, then asks for tools.
	second := model.sent[1]
	nudgeAssistant, nudge := second[len(second)-2], second[len(second)-1]
	if nudgeAssistant.Content != "Looks fine to me." || len(nudgeAssistant.Thinking) != 1 || !strings.Contains(nudge.Content, "finish by calling finish") {
		t.Errorf("nudge turn = %+v / %+v", nudgeAssistant, nudge)
	}
	// The refused finish comes back as an error result.
	third := model.sent[2]
	refusal := third[len(third)-1].ToolResults[0]
	if !refusal.IsError || !strings.Contains(refusal.Content, "record at least one note first") {
		t.Errorf("refusal = %+v", refusal)
	}
	// done is gone, the extra tool is there.
	tools := strings.Join(model.tools[0], ",")
	if strings.Contains(tools, "done") || !strings.Contains(tools, "note") {
		t.Errorf("tools = %s", tools)
	}
}

func TestPlainTextEndsRunWithoutRequireFinish(t *testing.T) {
	s := openTestDB(t)
	sess, _ := CreateSession(s.db, ModeReview, "o", "r", 2, "pr", "p")
	model := &scriptedModel{responses: []*ai.ChatResponse{text("final answer")}}
	res, err := New(model, &Workspace{Dir: t.TempDir()}, &LogReporter{}, sess, Config{Mode: ModeReview, MaxTurns: 5}).Run(context.Background(), "q")
	if err != nil || res.Text != "final answer" || res.TurnCount != 1 {
		t.Errorf("res = %+v err = %v", res, err)
	}
}
