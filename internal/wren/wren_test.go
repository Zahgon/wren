// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import (
	"math"
	"strings"
	"testing"
)

// run interprets source in a fresh VM and returns what it wrote, the errors it
// reported and the result.
func run(t *testing.T, source string) (string, string, InterpretResult) {
	t.Helper()
	var out, errs strings.Builder
	vm := NewVM(Config{
		WriteFn: func(vm *VM, text string) { out.WriteString(text) },
		ErrorFn: func(vm *VM, typ ErrorType, module string, line int, message string) {
			switch typ {
			case ErrorCompile:
				errs.WriteString("[" + module + " line " + FormatG14(float64(line)) + "] " + message + "\n")
			case ErrorRuntime:
				errs.WriteString(message + "\n")
			case ErrorStackTrace:
				errs.WriteString("[" + module + " line " + FormatG14(float64(line)) + "] in " + message + "\n")
			}
		},
	})
	result := vm.Interpret("main", source)
	return out.String(), errs.String(), result
}

func TestInterpret(t *testing.T) {
	cases := []struct {
		name   string
		source string
		out    string
		result InterpretResult
	}{
		{"print", `System.print("hi")`, "hi\n", ResultSuccess},
		{"arithmetic", "System.print(1 + 2 * 3)", "7\n", ResultSuccess},
		{"interpolation", `System.print("a%(1 + 1)b")`, "a2b\n", ResultSuccess},
		{"class", "class A {\n  construct new() { _x = 1 }\n  x { _x }\n}\nSystem.print(A.new().x)",
			"1\n", ResultSuccess},
		{"closure", "var f = Fn.new {|a| a * 2}\nSystem.print(f.call(21))", "42\n", ResultSuccess},
		{"fiber", "var f = Fiber.new { Fiber.yield(1) }\nSystem.print(f.call())", "1\n", ResultSuccess},
		{"compile error", "var a = )", "", ResultCompileError},
		{"runtime error", "null.nope", "", ResultRuntimeError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, result := run(t, tc.source)
			if out != tc.out {
				t.Errorf("output = %q, want %q", out, tc.out)
			}
			if result != tc.result {
				t.Errorf("result = %v, want %v", result, tc.result)
			}
		})
	}
}

// TestFormatG14 checks the number rendering the VM relies on, which is C's
// "%.14g": fourteen significant digits, trailing zeros removed, and exponent
// form outside a fixed range.
func TestFormatG14(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "-0"},
		{1, "1"},
		{1.0 / 3.0, "0.33333333333333"},
		{1e15, "1e+15"},
		{1e14, "1e+14"},
		{123456789012345, "1.2345678901234e+14"},
		{1e-5, "1e-05"},
		{0.0001, "0.0001"},
		{1e100, "1e+100"},
		{math.MaxFloat64, "1.7976931348623e+308"},
	}
	for _, tc := range cases {
		if got := FormatG14(tc.value); got != tc.want {
			t.Errorf("FormatG14(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

// TestMapOrder pins the iteration order of a map, which the open-addressed
// table's layout makes observable.
func TestMapOrder(t *testing.T) {
	out, _, result := run(t, `var m = {}
for (i in 1..5) m[i] = i
System.print(m.keys.toList)
System.print(m)`)
	if result != ResultSuccess {
		t.Fatalf("result = %v", result)
	}
	const want = "[2, 1, 3, 5, 4]\n{2: 2, 1: 1, 3: 3, 5: 5, 4: 4}\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}

// TestSignedZeroKeys checks that a map tells 0 and -0 apart, because its keys
// are compared by their bit patterns.
func TestSignedZeroKeys(t *testing.T) {
	out, _, result := run(t, "var m = {}\nm[0] = \"a\"\nm[-0] = \"b\"\nSystem.print(m.count)")
	if result != ResultSuccess {
		t.Fatalf("result = %v", result)
	}
	if out != "2\n" {
		t.Errorf("output = %q, want %q", out, "2\n")
	}
}

// TestUtf8RoundTrip checks the encoder and decoder against each other over
// every code point, including the surrogate range the VM encodes as it is.
func TestUtf8RoundTrip(t *testing.T) {
	var buf [4]byte
	for cp := 0; cp <= 0x10ffff; cp++ {
		n := Utf8Encode(cp, buf[:])
		if n != Utf8EncodeNumBytes(cp) {
			t.Fatalf("code point %#x encoded in %d bytes, want %d", cp, n, Utf8EncodeNumBytes(cp))
		}
		if got := Utf8Decode(string(buf[:n])); got != cp {
			t.Fatalf("code point %#x decoded back as %#x", cp, got)
		}
	}
	for _, invalid := range []string{"\x80", "\xbf", "\xc2", "\xe0\xa0", "\xf0\x90\x80", "\xf8", "\xff"} {
		if got := Utf8Decode(invalid); got != -1 {
			t.Errorf("Utf8Decode(%q) = %d, want -1", invalid, got)
		}
	}
}

// TestSeededRandom pins the generator the random module uses, whose output
// depends on the exact seeding of its state.
func TestSeededRandom(t *testing.T) {
	out, errs, result := run(t, `import "random" for Random
var r = Random.new(12345)
System.print(r.float())
System.print(r.int(1000))`)
	if result != ResultSuccess {
		t.Fatalf("result = %v, errors = %q", result, errs)
	}
	const want = "0.42174239565409\n432\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}
