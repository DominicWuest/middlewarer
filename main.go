package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/types"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/tools/go/packages"
)

var (
	typeName = flag.String("type", "", "The interface type to wrap")
	output   = flag.String("output", "", "Output file name, default srcdir/<type>_middleware.go")
	debug    = flag.Bool("d", false, "Enable debug mode, write output to os.Stdout")
)

func main() {
	log.SetPrefix("middlewarer: ")

	flag.Parse()
	if *typeName == "" {
		flag.Usage()
		log.Printf("no type name supplied")
		os.Exit(1)
	}

	var destWriter io.Writer

	if *debug {
		destWriter = os.Stdout
	} else {
		outFileName := fmt.Sprintf("%s_middleware.go", strings.ToLower(*typeName))
		if *output != "" {
			outFileName = *output
		}

		out, err := os.OpenFile(outFileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			log.Fatalf("Couldn't open output file %s - %v", outFileName, err)
		}
		destWriter = out
		defer func() {
			if err := out.Close(); err != nil {
				log.Fatalf("Failed to close output file - %v", err)
			}
		}()
	}

	g := Generator{}
	g.init(*typeName)

	g.generateWrapperCode()

	cmd := exec.Command("goimports")

	// Open stdin and stdout pipes
	cmdIn := new(bytes.Buffer)
	cmd.Stdin = cmdIn
	cmdOut := new(bytes.Buffer)
	cmd.Stdout = cmdOut

	// Print generated code to formatter
	g.print(cmdIn)

	// Start command
	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start format command - %v", err)
	}

	if err := cmd.Wait(); err != nil {
		log.Fatalf("Command to format code failed - %v", err)
	}

	res, err := io.ReadAll(cmdOut)
	if err != nil {
		log.Fatalf("Failed to format generated code - %v", err)
	}

	fmt.Fprint(destWriter, string(res))
}

type Generator struct {
	p          *packages.Package
	target     *types.Interface
	targetName string

	targetFirstLetter string
	structName        string

	// Buffers for the different sections of the generated code
	wrapFunction     *bytes.Buffer
	middlewareStruct *bytes.Buffer
	handlerFuncTypes *bytes.Buffer
	interfaceMethods *bytes.Buffer
}

// init inits the generator.
// It loads the package to parse and looks for the interface
// with name matching the passed target string.
func (g *Generator) init(target string) {
	g.targetName = target

	// Load the package of the current directory
	packs, err := packages.Load(&packages.Config{
		// TODO: Make sure to minimize information here, probably getting too much
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedImports,
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

	if iFace.Empty() {
		log.Fatalf("Trying to generate middlewarer for an empty interface")
	}

	g.target = iFace
}

const wrapFunctionFormat = `// Wrap%[1]s returns the passed %[1]s wrapped in the middleware defined in %[2]s
func Wrap%[1]s(toWrap %[1]s, wrapper %[2]s) %[1]s {
	wrapper.wrapped = toWrap
	return &wrapper
}
`

// generateWrapperCode generates the code for the wrapper of the target interface
func (g *Generator) generateWrapperCode() {
	g.wrapFunction = new(bytes.Buffer)
	g.middlewareStruct = new(bytes.Buffer)
	g.handlerFuncTypes = new(bytes.Buffer)
	g.interfaceMethods = new(bytes.Buffer)

	g.structName = fmt.Sprintf("%sMiddleware", g.targetName)
	g.targetFirstLetter = strings.ToLower(g.targetName[0:1])

	// Write wrap function
	fmt.Fprintf(g.wrapFunction, wrapFunctionFormat, g.targetName, g.structName)

	// Write header of middleware struct
	fmt.Fprintf(g.middlewareStruct, "// %s implements %s\n", g.structName, g.targetName)
	fmt.Fprintf(g.middlewareStruct, "type %s struct {\n", g.structName)
	fmt.Fprintf(g.middlewareStruct, "\twrapped %s\n", g.targetName)
	fmt.Fprintln(g.middlewareStruct)

	g.generateInterfaceMethods(g.target)

	// Write footer of middleware struct
	fmt.Fprint(g.middlewareStruct, "}\n")
}

// interfaceMethodFormatReturn is the format string for interface methods
// which have a return value
// The arguments for the format string are:
//
//	[1]: The first letter of the receiver type
//	[2]: The receiver type
//	[3]: The function name
//	[4]: The function parameters
//	[5]: The function return type
//	[6]: The function arguments list
const interfaceMethodFormatReturn = `func (%[1]s *%[2]s) %[3]s(%[4]s) %[5]s {
	fun := %[1]s.wrapped.%[3]s
	if %[1]s.%[3]sMiddleware != nil {
		fun = %[1]s.%[3]sMiddleware(fun)
	}
	return fun(%[6]s)
}

`

// interfaceMethodFormatReturn is the format string for interface methods
// which have no return value
// The arguments for the format string are:
//
//	[1]: The first letter of the receiver type
//	[2]: The receiver type
//	[3]: The function name
//	[4]: The function parameters
//	[5]: The function arguments list
const interfaceMethodFormatVoid = `func (%[1]s *%[2]s) %[3]s(%[4]s) {
	fun := %[1]s.wrapped.%[3]s
	if %[1]s.%[3]sMiddleware != nil {
		fun = %[1]s.%[3]sMiddleware(fun)
	}
	fun(%[5]s)
}

`

// generateInterfaceMethods generates the function declarations of
// the methods required by the wrapper to implement
func (g *Generator) generateInterfaceMethods(target *types.Interface) {
	for i := 0; i < target.NumMethods(); i++ {
		fun := target.Method(i)

		// Generate the handler type
		handlerTypeName := fmt.Sprintf("%sHandler", fun.Name())
		fmt.Fprintf(g.handlerFuncTypes, "type %s %s\n", handlerTypeName, fun.Type())

		// Generate the struct field
		structFieldName := fmt.Sprintf("%sMiddleware", fun.Name())
		fmt.Fprintf(g.middlewareStruct, "\t%s func(%[2]s) %[2]s\n", structFieldName, handlerTypeName)

		// Generate the middleware method
		g.generateMiddlewareMethod(fun)
	}
}

func (g *Generator) generateMiddlewareMethod(fun *types.Func) {

	methodSignature := fun.Type().(*types.Signature)

	parametersList := strings.Builder{}
	argumentsList := strings.Builder{}

	for i := 0; i < methodSignature.Params().Len(); i++ {
		param := methodSignature.Params().At(i)
		fmt.Fprintf(&argumentsList, "a%d, ", i)
		fmt.Fprintf(&parametersList, "a%d %s, ", i, param.Type().String())
	}

	// Remove trailing commas
	parameters := strings.TrimSuffix(parametersList.String(), ", ")
	arguments := strings.TrimSuffix(argumentsList.String(), ", ")

	if methodSignature.Results().Len() == 0 {
		fmt.Fprintf(g.interfaceMethods, interfaceMethodFormatVoid,
			g.targetFirstLetter,
			g.structName,
			fun.Name(),
			parameters,
			arguments,
		)
	} else {
		returnType := methodSignature.Results().String()
		if methodSignature.Results().Len() == 1 {
			returnType = strings.Trim(returnType, "()")
		}

		fmt.Fprintf(g.interfaceMethods, interfaceMethodFormatReturn,
			g.targetFirstLetter,
			g.structName,
			fun.Name(),
			parameters,
			returnType,
			arguments,
		)
	}
}

// print writes the generated code to the provided io.Writer
func (g *Generator) print(w io.Writer) {
	// Print header
	fmt.Fprintf(w, "// Code generated by \"middlewarer %s\"; DO NOT EDIT.\n", strings.Join(os.Args[1:], " "))
	fmt.Fprintf(w, "package %s\n", g.p.Name)
	fmt.Fprintln(w)

	// Print the generated code
	w.Write(g.wrapFunction.Bytes())
	fmt.Fprintln(w)
	w.Write(g.middlewareStruct.Bytes())
	fmt.Fprintln(w)
	w.Write(g.handlerFuncTypes.Bytes())
	fmt.Fprintln(w)
	w.Write(g.interfaceMethods.Bytes())
	fmt.Fprintln(w)
}
