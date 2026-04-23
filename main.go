package main

import (
	"os"

	"github.com/leosocy/diffah/cmd"
)

func main() {
	os.Exit(cmd.Execute(os.Stderr))
}
