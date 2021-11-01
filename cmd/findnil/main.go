package main

import (
	"os"

	"github.com/gostaticanalysis/findnil"
)

func main() {
	os.Exit(findnil.Main(os.Args[1:]...))
}
