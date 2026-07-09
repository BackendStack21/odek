package extended

// Associations is the P3/P5 extension point for linking related atoms. In
// P0-P2 it is a stub.
type Associations struct{}

// NewAssociations creates a new Associations stub.
func NewAssociations() *Associations {
	return &Associations{}
}

// Link is a no-op stub.
func (a *Associations) Link(fromID, toID string) {}

// Related is a no-op stub.
func (a *Associations) Related(id string) []string {
	return nil
}
