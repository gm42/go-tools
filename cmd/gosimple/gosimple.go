// gosimple detects code that could be rewritten in a simpler way.
package main // import "github.com/gm42/go-tools/cmd/gosimple"
import (
	"os"

	"github.com/gm42/go-tools/lint/lintutil"
	"github.com/gm42/go-tools/simple"
)

func main() {
	fs := lintutil.FlagSet("gosimple")
	gen := fs.Bool("generated", false, "Check generated code")
	fs.Parse(os.Args[1:])
	c := simple.NewChecker()
	c.CheckGenerated = *gen

	lintutil.ProcessFlagSet(c, fs)
}
