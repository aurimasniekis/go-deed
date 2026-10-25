// Command deed-vet runs Deed's custom analyzers. Use with: go vet -vettool=$(which deed-vet) ./...
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/aurimasniekis/go-deed/tools/analyzers/txscope"
)

func main() {
	unitchecker.Main(txscope.Analyzer)
}
