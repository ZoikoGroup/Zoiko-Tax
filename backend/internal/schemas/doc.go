// Package schemas holds the Go half of the canonical JSON Schemas under
// contracts/schemas/. It has no production code: its test reads each schema's
// closed vocabularies and fails when one disagrees with the Go domain it
// describes, so a family, status or treatment added on one side only is a red
// build rather than a contract that quietly lies (ADR-0006 §2.2 makes JSON
// Schema the schema authority across the boundary).
//
// It lives outside internal/domain because reading a file is I/O, which the
// domain does not do, tests included (ADR-0007 §2.5).
package schemas
