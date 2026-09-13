package tool

// Effects declares the resources an extension call may read or write. Paths
// are canonicalized by the executor. Unknown effects serialize with all
// calls; an empty declaration is an explicitly side-effect-free operation.
// This is scheduling metadata and never changes operation approval policy.
type Effects struct {
	Reads   []string
	Writes  []string
	Unknown bool
}
