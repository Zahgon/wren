// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Distributed under the MIT licence; see LICENSE in the repository root.

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// wrenBinary is the command built once for the whole test run.
var wrenBinary string

// exitCode returns the exit status of a finished command, or -1 if it did not
// run at all.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wren-build")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create a build directory:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	wrenBinary = filepath.Join(dir, "wren")
	build := exec.Command("go", "build", "-o", wrenBinary, ".")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot build the wren command:", err)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// The expectation comments the upstream test scripts carry.
var (
	expectPattern          = regexp.MustCompile(`// expect: ?(.*)`)
	expectErrorPattern     = regexp.MustCompile(`// expect error`)
	expectErrorLinePattern = regexp.MustCompile(`// expect error line (\d+)`)
	expectRuntimePattern   = regexp.MustCompile(`// expect (handled )?runtime error: (.+)`)
	errorLinePattern       = regexp.MustCompile(`^\[.* line (\d+)\] Error`)
	stackTracePattern      = regexp.MustCompile(`^(?:\[\./)?test/.* line (\d+)\] in`)
	skipPattern            = regexp.MustCompile(`// skip: (.*)`)
	nonTestPattern         = regexp.MustCompile(`// nontest`)
)

// expectations is what one upstream test script says its run should produce.
type expectations struct {
	output        []string
	compileErrors map[int]bool
	runtimeLine   int
	runtimeError  string
	exitCode      int
	skip          bool
}

// parseExpectations reads the "// expect" comments out of a script.
func parseExpectations(source string) expectations {
	exp := expectations{compileErrors: map[int]bool{}}
	for i, line := range regexp.MustCompile(`\r\n|\n`).Split(source, -1) {
		lineNum := i + 1
		if line == "" {
			continue
		}
		if nonTestPattern.MatchString(line) || skipPattern.MatchString(line) {
			exp.skip = true
			return exp
		}
		if m := expectPattern.FindStringSubmatch(line); m != nil {
			exp.output = append(exp.output, m[1])
		}
		if expectErrorPattern.MatchString(line) && !strings.Contains(line, "// expect error line") {
			exp.compileErrors[lineNum] = true
			exp.exitCode = 65
		}
		if m := expectErrorLinePattern.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			exp.compileErrors[n] = true
			exp.exitCode = 65
		}
		if m := expectRuntimePattern.FindStringSubmatch(line); m != nil {
			exp.runtimeLine = lineNum
			exp.runtimeError = m[2]
			if m[1] != "handled " {
				exp.exitCode = 70
			}
		}
	}
	return exp
}

// splitLines splits output into lines without a trailing empty element.
func splitLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// check compares one run against the script's expectations the way the
// upstream Python test runner does.
func check(t *testing.T, exp expectations, stdout, stderr []byte, code int) {
	t.Helper()

	if exp.runtimeError != "" {
		lines := splitLines(stderr)
		// The message may be preceded by the errors of a module that failed
		// to compile, so look for it anywhere in the error output.
		reported := -1
		for i, line := range lines {
			if line == exp.runtimeError {
				reported = i
				break
			}
		}
		if reported == -1 {
			t.Errorf("expected the runtime error %q, error output was %q", exp.runtimeError, lines)
		} else {
			// Find the stack frame for the script itself and check its line.
			found := false
			for _, line := range lines[reported+1:] {
				if m := stackTracePattern.FindStringSubmatch(line); m != nil {
					n, _ := strconv.Atoi(m[1])
					if n != exp.runtimeLine {
						t.Errorf("runtime error reported on line %d, want %d", n, exp.runtimeLine)
					}
					found = true
					break
				}
			}
			if !found && len(lines) > reported+1 {
				t.Errorf("no stack frame for the script in %q", lines[reported+1:])
			}
		}
	} else if len(exp.compileErrors) > 0 {
		got := map[int]bool{}
		for _, line := range splitLines(stderr) {
			if m := errorLinePattern.FindStringSubmatch(line); m != nil {
				n, _ := strconv.Atoi(m[1])
				got[n] = true
			}
		}
		for want := range exp.compileErrors {
			if !got[want] {
				t.Errorf("expected a compile error on line %d; errors were %v", want, got)
			}
		}
		for have := range got {
			if !exp.compileErrors[have] {
				t.Errorf("unexpected compile error on line %d", have)
			}
		}
	} else if len(stderr) > 0 {
		t.Errorf("unexpected error output:\n%s", stderr)
	}

	if code != exp.exitCode {
		t.Errorf("exit status = %d, want %d", code, exp.exitCode)
	}

	gotLines := splitLines(stdout)
	for i, want := range exp.output {
		if i >= len(gotLines) {
			t.Errorf("output line %d missing, want %q", i+1, want)
			continue
		}
		if gotLines[i] != want {
			t.Errorf("output line %d = %q, want %q", i+1, gotLines[i], want)
		}
	}
	if len(gotLines) > len(exp.output) {
		t.Errorf("unexpected extra output: %q", gotLines[len(exp.output):])
	}
}

// runScript runs one script from the repository root, as the command is meant
// to be used, and returns what it produced.
func runScript(t *testing.T, rel string) ([]byte, []byte, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(wrenBinary, rel)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), exitCode(err)
}

// upstreamScripts lists the Wren scripts of the upstream suite that this port
// is responsible for: everything under test/ except the tests of the C
// embedding API and the benchmarks, which are out of scope.
func upstreamScripts(t *testing.T) []string {
	t.Helper()
	var scripts []string
	err := filepath.Walk("test", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch filepath.Base(path) {
			case "api", "benchmark":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".wren" {
			scripts = append(scripts, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cannot walk the test directory: %v", err)
	}
	if len(scripts) == 0 {
		t.Fatal("no upstream test scripts found")
	}
	return scripts
}

// TestUpstreamScripts runs the upstream language and core-library tests and
// checks them against their own "// expect" comments.
func TestUpstreamScripts(t *testing.T) {
	for _, script := range upstreamScripts(t) {
		script := script
		t.Run(script, func(t *testing.T) {
			source, err := os.ReadFile(script)
			if err != nil {
				t.Fatalf("cannot read the script: %v", err)
			}
			exp := parseExpectations(string(source))
			if exp.skip {
				t.Skip("the script is marked as not a test")
			}
			stdout, stderr, code := runScript(t, script)
			check(t, exp, stdout, stderr, code)
		})
	}
}

// TestExamples runs the example scripts. They carry no expectation comments,
// so all that is checked is that each one runs to completion without an error.
func TestExamples(t *testing.T) {
	entries, err := filepath.Glob("example/*.wren")
	if err != nil {
		t.Fatalf("cannot list the examples: %v", err)
	}
	for _, script := range entries {
		script := script
		t.Run(script, func(t *testing.T) {
			_, stderr, code := runScript(t, script)
			if code != 0 {
				t.Errorf("exit status = %d, want 0; error output: %s", code, stderr)
			}
			if len(stderr) > 0 {
				t.Errorf("unexpected error output:\n%s", stderr)
			}
		})
	}
}

// TestCommandLine covers the command's own behaviour: its usage text, its
// version line and the status it exits with.
func TestCommandLine(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		stdout string
		stderr string
		code   int
	}{
		{"no arguments", nil, "This is a Wren test runner.\nUsage: wren_test [file]\n", "", exUsage},
		{"version", []string{"--version"},
			"wren_test is running on Wren version " + versionString + "\n", "", 1},
		{"missing file", []string{"no-such-file.wren"}, "",
			"Could not find file \"no-such-file.wren\".\n", exNoInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(wrenBinary, tc.args...)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
			code := exitCode(cmd.Run())
			if stdout.String() != tc.stdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.stdout)
			}
			if stderr.String() != tc.stderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.stderr)
			}
			if code != tc.code {
				t.Errorf("exit status = %d, want %d", code, tc.code)
			}
		})
	}
}
