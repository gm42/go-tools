package errcheck

import (
	"testing"

	"github.com/gm42/go-tools/lint/testutil"
)

func TestAll(t *testing.T) {
	c := NewChecker()
	testutil.TestAll(t, c, "")
}
