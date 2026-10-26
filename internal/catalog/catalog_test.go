package catalog

import (
	"errors"
	"testing"
)

func TestResolve(t *testing.T) {
	cat, err := New([]Model{
		{ID: "opencode/grok-4.7", Connector: "opencode", Upstream: "grok-4.7", Protocol: ProtocolResponses},
		{ID: "devin/claude-haiku-5-5-low", Connector: "devin", Upstream: "claude-haiku-5-5-low", Protocol: ProtocolDevin},
	}, 1700000000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := cat.Resolve("devin/claude-haiku-5-5-low")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Upstream != "claude-haiku-5-5-low" || got.Protocol != ProtocolDevin {
		t.Errorf("resolved %+v, want the Devin model", got)
	}

	if _, err := cat.Resolve("missing/model"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("Resolve(missing) error = %v, want ErrModelNotFound", err)
	}
	if cat.Created() != 1700000000 {
		t.Errorf("Created() = %d, want 1700000000", cat.Created())
	}
}

func TestNewRejectsDuplicateID(t *testing.T) {
	if _, err := New([]Model{{ID: "a"}, {ID: "a"}}, 0); err == nil {
		t.Fatal("New accepted duplicate ids")
	}
}

func TestResolveAlias(t *testing.T) {
	cat, err := New([]Model{
		{ID: "devin/claude-haiku-5-5-low", Connector: "devin", Upstream: "claude-haiku-5-5-low", Protocol: ProtocolDevin, Aliases: []string{"devin/haiku"}},
		{ID: "opencode/glm-5.3", Connector: "opencode", Upstream: "glm-5.3", Protocol: ProtocolChat, Aliases: []string{"opencode/glm"}},
	}, 1700000000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := cat.Resolve("devin/haiku")
	if err != nil {
		t.Fatalf("Resolve(alias): %v", err)
	}
	if got.ID != "devin/claude-haiku-5-5-low" || got.Upstream != "claude-haiku-5-5-low" {
		t.Errorf("Resolve(devin/haiku) = %+v, want the canonical model", got)
	}
	if _, err := cat.Resolve("devin/glm"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("Resolve(devin/glm) error = %v, want ErrModelNotFound (alias is per provider)", err)
	}
}

func TestNewRejectsAliasShadowingID(t *testing.T) {
	_, err := New([]Model{
		{ID: "devin/claude-haiku-5-5-low"},
		{ID: "opencode/glm-5.3", Aliases: []string{"devin/claude-haiku-5-5-low"}},
	}, 0)
	if err == nil {
		t.Fatal("New accepted an alias that shadows a model id")
	}
}

func TestNewRejectsDuplicateAlias(t *testing.T) {
	_, err := New([]Model{
		{ID: "opencode/glm-5.3", Aliases: []string{"opencode/glm"}},
		{ID: "opencode/grok-4.7", Aliases: []string{"opencode/glm"}},
	}, 0)
	if err == nil {
		t.Fatal("New accepted duplicate aliases")
	}
}

func TestResolveTable(t *testing.T) {
	cat, err := New([]Model{
		{ID: "devin/claude-haiku-5-5-low", Connector: "devin", Upstream: "claude-haiku-5-5-low", Protocol: ProtocolDevin},
		{ID: "opencode/glm-5.3", Connector: "opencode", Upstream: "glm-5.3", Protocol: ProtocolChat},
		{ID: "opencode/grok-4.7", Connector: "opencode", Upstream: "grok-4.7", Protocol: ProtocolResponses},
	}, 1700000000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		name      string
		id        string
		wantUp    string
		wantProto Protocol
		wantMiss  bool
	}{
		{
			name:      "devin uid verbatim",
			id:        "devin/claude-haiku-5-5-low",
			wantUp:    "claude-haiku-5-5-low",
			wantProto: ProtocolDevin,
		},
		{
			name:      "opencode chat model",
			id:        "opencode/glm-5.3",
			wantUp:    "glm-5.3",
			wantProto: ProtocolChat,
		},
		{
			name:      "opencode responses model",
			id:        "opencode/grok-4.7",
			wantUp:    "grok-4.7",
			wantProto: ProtocolResponses,
		},
		{name: "unknown namespace", id: "anthropic/claude-3", wantMiss: true},
		{name: "known prefix, unknown uid", id: "devin/nope", wantMiss: true},
		{name: "empty id", id: "", wantMiss: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cat.Resolve(tc.id)
			if tc.wantMiss {
				if !errors.Is(err, ErrModelNotFound) {
					t.Fatalf("Resolve(%q) error = %v, want ErrModelNotFound", tc.id, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.id, err)
			}
			if got.ID != tc.id || got.Upstream != tc.wantUp || got.Protocol != tc.wantProto {
				t.Errorf("Resolve(%q) = %+v, want upstream %q protocol %q", tc.id, got, tc.wantUp, tc.wantProto)
			}
		})
	}
}

func TestModelsPreservesConfigOrder(t *testing.T) {
	models := []Model{{ID: "b"}, {ID: "a"}, {ID: "c"}}
	cat, err := New(models, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := cat.Models()
	for i, want := range []string{"b", "a", "c"} {
		if got[i].ID != want {
			t.Errorf("Models()[%d].id = %q, want %q (config order)", i, got[i].ID, want)
		}
	}

	// Models() hands out a copy: mutating it must not touch the catalog.
	got[0].ID = "mutated"
	if cat.Models()[0].ID != "b" {
		t.Error("Models() exposed internal state to mutation")
	}
}
