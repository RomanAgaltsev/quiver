// Command qv is the quiver CLI entry point; the exit code is the process exit code.
package main

import (
	"os"

	"github.com/RomanAgaltsev/quiver/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
