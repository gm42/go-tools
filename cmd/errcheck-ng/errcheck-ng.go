package main // import "github.com/gm42/go-tools/cmd/errcheck-ng"

import (
	"os"

	"github.com/gm42/go-tools/errcheck"
	"github.com/gm42/go-tools/lint/lintutil"
)

func main() {
	c := errcheck.NewChecker()
	lintutil.ProcessArgs("errcheck-ng", c, os.Args[1:])
}
