// staticcheck detects a myriad of bugs and inefficiencies in your
// code.
package main // import "github.com/gm42/go-tools/cmd/staticcheck"

import (
	"os"

	"github.com/gm42/go-tools/lint/lintutil"
	"github.com/gm42/go-tools/staticcheck"
)

func main() {
	fs := lintutil.FlagSet("staticcheck")
	gen := fs.Bool("generated", false, "Check generated code")
	fs.Parse(os.Args[1:])
	c := staticcheck.NewChecker()
	c.CheckGenerated = *gen
	lintutil.ProcessFlagSet(c, fs)
}
