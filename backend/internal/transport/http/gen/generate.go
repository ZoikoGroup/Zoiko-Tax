// Package gen holds the wire types generated from the v1 contract.
//
// Nothing here is edited by hand. `make api-gen` regenerates it from the 3.1
// export (ADR-0010 §2.2) and `make api-gen-check` fails when the committed
// output differs, so a contract change that the handlers have not caught up
// with is a build failure rather than a response that quietly stops matching
// what the SDKs were generated from.
//
// The generator is pinned here and run with `go run pkg@version`, so it never
// enters go.mod: it is a build tool, and the module graph is the graph the cell
// ships with (ADR-0001 control 3).
package gen

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml ../../../../../contracts/openapi/export/ztax.v1.3.1.yaml
