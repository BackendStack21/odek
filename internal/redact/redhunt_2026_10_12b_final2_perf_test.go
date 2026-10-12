package redact

import (
	"strings"
	"testing"
	"time"
)

// Rejected values are judged on a short first window, so adversarial input
// made of rejected credential names stays cheap in absolute terms.
func TestRED_RejectedValuesShortWindowAbsoluteBound(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("absolute bound")
	}
	for _, unit := range []string{"aToken=", "xtoken=", "passwd=&", "password=&token=", "api_key=a.b("} {
		in := strings.Repeat(unit, (1<<20)/len(unit))
		if d := finalRedactTime(in, 2); d > 1500*time.Millisecond {
			t.Errorf("%q x 1MiB took %v", unit, d)
		}
	}
}
