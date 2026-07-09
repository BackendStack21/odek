package extended

// UserModel is the P3 extension point for inferring persistent user
// preferences and state from atoms. In P0-P2 it is a stub.
type UserModel struct{}

// NewUserModel creates a new UserModel stub.
func NewUserModel() *UserModel {
	return &UserModel{}
}

// Update is a no-op stub.
func (u *UserModel) Update(atom MemoryAtom) {}

// Summary is a no-op stub.
func (u *UserModel) Summary() string {
	return ""
}
