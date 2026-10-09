package id

import (
	"fmt"
	"strings"
)

// ExternalReference is another system's identifier for something this one
// holds: a billing system's invoice number, an ERP's document key
// (ZTAX-DOM-REQ-0003, ADR-0012 §2.5).
//
// It is never a key here. It is not unique — two systems, or one system
// twice, can use the same value — and it is never parsed, so it carries the
// system and the namespace that give the value its meaning, stored apart so
// nothing is tempted to concatenate them into something that looks like an
// identifier.
type ExternalReference struct {
	SourceSystem string
	Namespace    string
	Value        string
}

// MaxExternalPart bounds each part of an external reference.
const MaxExternalPart = 255

// Validate refuses a reference that does not say whose it is.
func (r ExternalReference) Validate() error {
	for name, part := range map[string]string{"sourceSystem": r.SourceSystem, "namespace": r.Namespace, "value": r.Value} {
		if strings.TrimSpace(part) == "" || len(part) > MaxExternalPart {
			return fmt.Errorf("id: an external reference's %s is between 1 and %d characters", name, MaxExternalPart)
		}
	}
	return nil
}
