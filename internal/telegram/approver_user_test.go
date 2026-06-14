package telegram

import (
	"testing"
)

// TestTelegramApprover_BindsCallbackToOriginatingUser verifies that an approval
// callback is only accepted from the user who initiated the operation.
func TestTelegramApprover_BindsCallbackToOriginatingUser(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	const originatingUser int64 = 111
	const otherUser int64 = 999

	a := NewTelegramApprover(bot, 1, originatingUser)
	id := a.newID()
	pr := &pendingRequest{resp: make(chan string, 1), userID: originatingUser}
	a.pending[id] = pr

	// Callback from a different user must be rejected.
	handled := a.HandleCallback(cbPrefixApprove+id, otherUser)
	if !handled {
		t.Fatal("HandleCallback should return true for known approval callback")
	}

	select {
	case <-pr.resp:
		t.Fatal("callback from a different user should not be accepted")
	default:
	}

	// Callback from the originating user must be accepted.
	handled = a.HandleCallback(cbPrefixApprove+id, originatingUser)
	if !handled {
		t.Fatal("HandleCallback should return true for known approval callback")
	}

	select {
	case action := <-pr.resp:
		if action != "approve" {
			t.Fatalf("expected approve action, got %q", action)
		}
	default:
		t.Fatal("callback from originating user should have been accepted")
	}
}

// TestTelegramApprover_SameUserCanDenyAndTrust verifies that the originating
// user can also deny or trust a pending operation.
func TestTelegramApprover_SameUserCanDenyAndTrust(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	const originatingUser int64 = 111

	a := NewTelegramApprover(bot, 1, originatingUser)

	for _, tc := range []struct {
		prefix string
		want   string
	}{
		{cbPrefixDeny, "deny"},
		{cbPrefixTrust, "trust"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			id := a.newID()
			pr := &pendingRequest{resp: make(chan string, 1)}
			a.pending[id] = pr

			handled := a.HandleCallback(tc.prefix+id, originatingUser)
			if !handled {
				t.Fatalf("HandleCallback should return true for %s callback", tc.want)
			}

			select {
			case action := <-pr.resp:
				if action != tc.want {
					t.Fatalf("response action = %q, want %q", action, tc.want)
				}
			default:
				t.Fatalf("%s callback from originating user should be accepted", tc.want)
			}
		})
	}
}

// TestTelegramApprover_ZeroUserIDAllowsAnyCallback verifies backward
// compatibility: if no originating user is known (userID == 0), callbacks from
// any user are accepted. This matches the legacy behavior before user binding.
func TestTelegramApprover_ZeroUserIDAllowsAnyCallback(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	bot := testBot(t, ts)

	a := NewTelegramApprover(bot, 1, 0)
	id := a.newID()
	pr := &pendingRequest{resp: make(chan string, 1), userID: 0}
	a.pending[id] = pr

	handled := a.HandleCallback(cbPrefixApprove+id, 999)
	if !handled {
		t.Fatal("HandleCallback should return true for known approval callback")
	}

	select {
	case action := <-pr.resp:
		if action != "approve" {
			t.Fatalf("expected approve action, got %q", action)
		}
	default:
		t.Fatal("callback should be accepted when originating user ID is zero")
	}
}
