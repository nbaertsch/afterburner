package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nbaertsch/afterburner/internal/cli"
)

var version = "dev"

func main() {
	code, err := cli.Run(context.Background(), os.Args[1:], cli.Options{
		Version: version,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "afterburn:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
