// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren repository's test/main.c and test/test.c.
// Distributed under the MIT licence; see LICENSE in the repository root.

// Command wren runs a Wren script file. It is the runner the Wren repository
// builds from its own sources: it serves one purpose, running the language
// level tests and benchmarks, and is not a general purpose VM or REPL.
package main

import (
	"fmt"
	"os"

	"github.com/wren-lang/wren/internal/wren"
)

// Exit codes used by the wren binaries, following the BSD standard.
const (
	// exUsage means the interpreter was used with the wrong number of
	// arguments.
	exUsage = 64
	// exDataErr is a compilation error.
	exDataErr = 65
	// exNoInput means the input file could not be opened.
	exNoInput = 66
	// exSoftware is a runtime error.
	exSoftware = 70
	// exIOErr is an I/O error.
	exIOErr = 74
)

// versionString is the version of the VM this runner is built against.
const versionString = "0.4.0"

// readFile returns the contents of the file at path. It reports that the file
// is missing by returning false, and exits if the file exists but cannot be
// read.
func readFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		fmt.Fprintf(os.Stderr, "Could not read file \"%s\".\n", path)
		os.Exit(exIOErr)
	}
	return string(data), true
}

// cString returns the part of s up to its first zero byte. The original hands
// everything it prints to C's printf as a null-terminated string, so output
// and error text stop at a zero byte even though Wren strings may contain
// them.
func cString(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i]
		}
	}
	return s
}

// write is the VM's output function: everything the script writes goes to
// standard output, stopping at a zero byte.
func write(vm *wren.VM, text string) {
	fmt.Print(cString(text))
}

// reportError prints the three kinds of error the VM reports. A compile error
// and a stack trace frame carry a module and a line; a runtime error is just
// its message.
func reportError(vm *wren.VM, typ wren.ErrorType, module string, line int, message string) {
	switch typ {
	case wren.ErrorCompile:
		fmt.Fprintf(os.Stderr, "[%s line %d] %s\n", cString(module), line, cString(message))
	case wren.ErrorRuntime:
		fmt.Fprintf(os.Stderr, "%s\n", cString(message))
	case wren.ErrorStackTrace:
		fmt.Fprintf(os.Stderr, "[%s line %d] in %s\n", cString(module), line, cString(message))
	}
}

// readModule loads an imported module from the file of the same name with a
// ".wren" extension added.
func readModule(vm *wren.VM, module string) wren.LoadModuleResult {
	source, found := readFile(module + ".wren")
	return wren.LoadModuleResult{Source: source, Found: found}
}

// resolveModule applies the import resolution policy. The rules are:
//
//   - If module starts with "./" or "../" it is a relative import, relative to
//     importer. The resolved path is the name joined onto the directory
//     containing importer, then normalized. Importing "./a/./b/../c" from
//     "./d/e/f" therefore gives "./d/e/a/c".
//   - Any other import string is logical and is used as it is.
func resolveModule(vm *wren.VM, importer, module string) (string, bool) {
	if pathTypeOf(module) == PathTypeSimple {
		return module, true
	}
	path := pathDirName(importer)
	path = pathJoin(path, module)
	return pathNormalize(path), true
}

func newVM() *wren.VM {
	return wren.NewVM(wren.Config{
		ResolveModuleFn: resolveModule,
		LoadModuleFn:    readModule,
		WriteFn:         write,
		ErrorFn:         reportError,
	})
}

// runFile interprets the script at path. The module name is the path with any
// extension removed, made explicitly relative if it has no prefix so that it
// can be told apart from a logical import name.
func runFile(vm *wren.VM, path string) wren.InterpretResult {
	source, found := readFile(path)
	if !found {
		fmt.Fprintf(os.Stderr, "Could not find file \"%s\".\n", path)
		os.Exit(exNoInput)
	}

	module := path
	if pathTypeOf(module) == PathTypeSimple {
		module = pathJoin(".", path)
	}
	module = pathRemoveExtension(module)

	return vm.Interpret(module, source)
}

// handleArgs deals with the arguments that do not run a file. It returns the
// exit code to stop with, or 0 to carry on and run the named file.
func handleArgs(args []string) int {
	if len(args) < 2 {
		fmt.Print("This is a Wren test runner.\nUsage: wren_test [file]\n")
		return exUsage
	}
	if len(args) == 2 && args[1] == "--version" {
		fmt.Printf("wren_test is running on Wren version %s\n", versionString)
		return 1
	}
	return 0
}

func main() {
	if handled := handleArgs(os.Args); handled != 0 {
		os.Exit(handled)
	}

	vm := newVM()
	result := runFile(vm, os.Args[1])

	switch result {
	case wren.ResultCompileError:
		os.Exit(exDataErr)
	case wren.ResultRuntimeError:
		os.Exit(exSoftware)
	}
	os.Exit(0)
}
