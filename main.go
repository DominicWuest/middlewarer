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
	log.SetFlags(0)
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

	// Generate the actual code
	g.generateWrapperCode()

	// Format the code and add imports
	cmd := exec.Command("goimports")

	// Open stdin and stdout pipes
	cmdIn := new(bytes.Buffer)
	cmd.Stdin = cmdIn
	cmdOut := new(bytes.Buffer)
	cmd.Stdout = cmdOut
	cmdStderr := new(bytes.Buffer)
	cmd.Stderr = cmdStderr

	// Print generated code to formatter
	g.print(cmdIn)

	// Start command
	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start format command - %v", err)
	}

	if err := cmd.Wait(); err != nil {
		stderr, _ := io.ReadAll(cmdStderr)
		log.Fatalf("Command to format code failed - %v\nStderr: %s\n", err, string(stderr))
	}

	res, err := io.ReadAll(cmdOut)
	if err != nil {
		log.Fatalf("Failed to format generated code - %v\n", err)
	}

	fmt.Fprint(destWriter, string(res))
}

// The Generator generates the code
type Generator struct {
	p          *packages.Package // The package in which this generator was invoked
	target     *types.Interface  // The target we want to wrap
	targetName string

	targetFirstLetter string // The first letter of the target name, used as the receiver
	structName        string // The name of the middleware struct we are generating

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

// Format string of the function returning a wrapped instance of the passed interface
// The arguments for the format string are:
//
//	[1]: The interface type name we are wrapping
//	[2]: The name of the middleware struct
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
		sigBuf := new(bytes.Buffer)
		types.WriteSignature(sigBuf, fun.Type().(*types.Signature), g.typeStringQuantifier)
		sigString, _ := io.ReadAll(sigBuf)
		fmt.Fprintf(g.handlerFuncTypes, "type %s func%s\n", handlerTypeName, string(sigString))

		// Generate the struct field
		structFieldName := fmt.Sprintf("%sMiddleware", fun.Name())
		fmt.Fprintf(g.middlewareStruct, "\t%s func(%[2]s) %[2]s\n", structFieldName, handlerTypeName)

		// Generate the middleware method
		g.generateMiddlewareMethod(fun)
	}
}

// generateMiddlewareMethod generates the code needed by the method implementation of the function
func (g *Generator) generateMiddlewareMethod(fun *types.Func) {
	methodSignature := fun.Type().(*types.Signature)

	parametersList := strings.Builder{}
	argumentsList := strings.Builder{}

	for i := 0; i < methodSignature.Params().Len(); i++ {
		param := methodSignature.Params().At(i)
		typeString := types.TypeString(param.Type(), g.typeStringQuantifier)
		fmt.Fprintf(&argumentsList, "a%d, ", i)
		fmt.Fprintf(&parametersList, "a%d %s, ", i, typeString)
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
		returnTypes := make([]string, methodSignature.Results().Len())
		for i := 0; i < methodSignature.Results().Len(); i++ {
			returnTypes[i] = types.TypeString(methodSignature.Results().At(i).Type(), g.typeStringQuantifier)
		}
		returnType := strings.Join(returnTypes, ", ")

		if methodSignature.Results().Len() != 1 {
			returnType = "(" + returnType + ")"
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

// typeStringQuantifier is to be used as the quantifier for calls to [types.TypeString]
func (g Generator) typeStringQuantifier(p *types.Package) string {
	if p.Path() == g.p.PkgPath {
		return ""
	}
	return p.Name()
}
