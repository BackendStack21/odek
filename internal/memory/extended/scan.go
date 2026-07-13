package extended

import (
	"context"
	"fmt"

	"github.com/BackendStack21/odek/internal/guard"
)

// ScanContent checks atom content for security threats. It delegates to the
// shared guard package so Extended Memory and legacy memory share the same
// scanning logic without an import cycle.
func ScanContent(content string) error {
	if err := guard.ScanContent(context.Background(), content, nil, nil); err != nil {
		return fmt.Errorf("extended memory: %v", err)
	}
	return nil
}
