package models

import (
	"encoding/json"
	"testing"
)

func TestMessageUnmarshalContentForms(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"string", `{"role":"user","content":"hi"}`, "hi", false},
		{"text parts", `{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`, "ab", false},
		{"input_text parts", `{"role":"user","content":[{"type":"input_text","text":"a"}]}`, "a", false},
		{"non-text dropped", `{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"cap"}]}`, "cap", false},
		{"empty array", `{"role":"user","content":[]}`, "", false},
		{"null", `{"role":"user","content":null}`, "", false},
		{"missing", `{"role":"user"}`, "", false},
		{"object rejected", `{"role":"user","content":{"type":"text","text":"x"}}`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m Message
			err := json.Unmarshal([]byte(tc.in), &m)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("content = %q, want an error", m.Content)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if m.Content != tc.want {
				t.Errorf("content = %q, want %q", m.Content, tc.want)
			}
		})
	}
}

func TestMessageUnmarshalFields(t *testing.T) {
	in := `{"role":"developer","content":"rules","name":"n",` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}],` +
		`"tool_call_id":"call_1","reasoning_content":"think"}`
	var m Message
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Role != RoleDeveloper {
		t.Errorf("role = %q, want developer", m.Role)
	}
	if m.Name != "n" || m.ToolCallID != "call_1" || m.ReasoningContent != "think" {
		t.Errorf("scalars = %+v", m)
	}
	if len(m.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(m.ToolCalls))
	}
	call := m.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "lookup" || call.Arguments != `{"q":"x"}` {
		t.Errorf("tool call = %+v, want nested function decoded", call)
	}
}

func TestToolCallUnmarshalFlat(t *testing.T) {
	var call ToolCall
	if err := json.Unmarshal([]byte(`{"id":"c1","name":"f","arguments":"{}"}`), &call); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if call.ID != "c1" || call.Name != "f" || call.Arguments != "{}" {
		t.Errorf("call = %+v", call)
	}
}
