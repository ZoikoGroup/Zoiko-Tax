// A separate module so that golang.org/x/tools never enters the dependency
// graph of the shipped binary. ADR-0001 §3.3 makes that graph part of the
// replay manifest; a build-time analyzer has no business in it.
module github.com/zoikogroup/zoikotax/backend/tools/fiscalfloat

go 1.25

require golang.org/x/tools v0.30.0

require (
	golang.org/x/mod v0.23.0 // indirect
	golang.org/x/sync v0.11.0 // indirect
)
