package extended

import "testing"

func TestUserModelStub(t *testing.T) {
	um := NewUserModel()
	um.Update(MemoryAtom{Text: "test", Type: TypeFact})
	if got := um.Summary(); got != "" {
		t.Errorf("expected empty summary from stub, got %q", got)
	}
}
