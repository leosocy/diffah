package main

import (
	"os"

	"github.com/leosocy/diffah/cmd"
)

func main() {
	if err := cmd.Execute(os.Stderr); err != nil {
		os.Exit(1)
	}
}
