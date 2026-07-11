// Command dctl is the dantofa platform control CLI.
package main

import (
	"os"

	"github.com/dantofa/platform/internal/commands"
)

func main() {
	os.Exit(commands.Execute())
}
