package simple

import (
	"testing"

	"github.com/gm42/go-tools/lint/testutil"
)

func TestAll(t *testing.T) {
	testutil.TestAll(t, NewChecker(), "")
}
