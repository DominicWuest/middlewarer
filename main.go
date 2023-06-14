package main

import (
	"flag"
	"fmt"
	"go/types"
	"log"
	"os"

	"golang.org/x/tools/go/packages"
)

var (
	typeName = flag.String("type", "", "The interface type to wrap")
	output   = flag.String("output", "", "output file name, default srcdir/<type>_middleware.go")
)

func main() {
	flag.Parse()
	if typeName == nil {
		flag.Usage()
		log.Printf("no type name supplied")
		os.Exit(1)
	}

	g := Generator{}
	g.init(*typeName)
	fmt.Println(g.target)
}

type Generator struct {
	p      *packages.Package
	target *types.Interface
}

// init inits the generator.
// It loads the package to parse and looks for the interface
// with name matching the passed target string.
func (g *Generator) init(target string) {
	// Load the package of the current directory
	packs, err := packages.Load(&packages.Config{
		// TODO: Make sure to minimize information here, probably getting too much
		Mode: packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedImports,
	}, ".")
	if err != nil {
		log.Printf("Failed to load packages - %v", err)
		os.Exit(1)
	}

	if len(packs) != 1 {
		log.Printf("Loaded package length is not 1, but %d", len(packs))
		os.Exit(1)
	}
	g.p = packs[0]

	// Look for the matching interface
	obj := g.p.Types.Scope().Lookup(target)
	if obj == nil {
		log.Fatalf("Couldn't find target object '%s' in source file", target)
	}

	iFace, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		log.Fatalf("Provided target object '%s' is not an interface", target)
	}

	g.target = iFace
}
