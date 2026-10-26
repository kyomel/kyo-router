// Package catalog owns the model list and the id -> model lookup. It is a pure
// value built once at startup: no I/O, no clock reads after New.
package catalog

import (
	"errors"
	"fmt"
	"slices"
)

// Protocol is the upstream wire format a model speaks. The set matches the
// config values, which config validates before a Model is built.
type Protocol string

const (
	ProtocolDevin     Protocol = "devin"
	ProtocolChat      Protocol = "chat"
	ProtocolResponses Protocol = "responses"
)

// ErrModelNotFound tags a resolve miss. Callers classify with errors.Is, never
// by comparing message text.
var ErrModelNotFound = errors.New("model not found")

// Model is one configured model. ID is what clients ask for; Upstream is what
// the provider expects. Aliases are extra public ids for the same model.
type Model struct {
	ID        string
	Connector string
	Upstream  string
	Protocol  Protocol
	Features  []string
	Aliases   []string
}

// Catalog is an immutable list plus its id and alias indexes.
type Catalog struct {
	models  []Model
	byID    map[string]Model
	byAlias map[string]Model
	created int64
}

// New indexes the configured models once, so a request never scans the list.
// created is passed in instead of read from the clock: the catalog stays a pure
// value and its tests need no clock seam.
func New(models []Model, created int64) (*Catalog, error) {
	byID := make(map[string]Model, len(models))
	for _, m := range models {
		if _, dup := byID[m.ID]; dup {
			return nil, fmt.Errorf("duplicate model id %q", m.ID)
		}
		byID[m.ID] = m
	}
	byAlias := make(map[string]Model)
	for _, m := range models {
		for _, alias := range m.Aliases {
			if _, dup := byID[alias]; dup {
				return nil, fmt.Errorf("alias %q shadows a model id", alias)
			}
			if _, dup := byAlias[alias]; dup {
				return nil, fmt.Errorf("duplicate alias %q", alias)
			}
			byAlias[alias] = m
		}
	}
	return &Catalog{models: slices.Clone(models), byID: byID, byAlias: byAlias, created: created}, nil
}

// Models returns the list in config order.
func (c *Catalog) Models() []Model { return slices.Clone(c.models) }

// Created is the catalog build time, used for the OpenAI model object.
func (c *Catalog) Created() int64 { return c.created }

// Resolve returns the model for a canonical id or an alias. Its only failure
// is a miss, tagged ErrModelNotFound.
func (c *Catalog) Resolve(id string) (Model, error) {
	if m, ok := c.byID[id]; ok {
		return m, nil
	}
	if m, ok := c.byAlias[id]; ok {
		return m, nil
	}
	return Model{}, fmt.Errorf("%w: %q", ErrModelNotFound, id)
}
