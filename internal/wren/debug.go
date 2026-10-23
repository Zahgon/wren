// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren VM's src/vm/wren_debug.c.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

// debugPrintStackTrace reports an uncaught runtime error and then one line per
// call frame, innermost first. Frames of the core module and of the stub
// functions used to call into the VM are left out, so that which part of the
// core library is written in Wren is not exposed.
func (vm *VM) debugPrintStackTrace() {
	// Bail out if the host does not want errors printed.
	if vm.config.ErrorFn == nil {
		return
	}

	fiber := vm.fiber
	if fiber.err.IsString() {
		vm.config.ErrorFn(vm, ErrorRuntime, "", -1, fiber.err.AsCString())
	} else {
		vm.config.ErrorFn(vm, ErrorRuntime, "", -1, "[error object]")
	}

	for i := len(fiber.frames) - 1; i >= 0; i-- {
		frame := &fiber.frames[i]
		fn := frame.closure.Fn

		// Skip the stub functions used to call methods from outside the VM.
		if fn.Module == nil {
			continue
		}
		// The built-in core module has no name, and is deliberately left out.
		if fn.Module.Name == nil {
			continue
		}

		// -1 because the instruction pointer has moved past the instruction
		// that was being executed. A frame that has not run an instruction at
		// all cannot name a line; the original reads past the start of its
		// line table there, so nothing sensible is defined for this case.
		index := frame.ip - 1
		if index < 0 {
			index = 0
		}
		line := fn.Debug.SourceLines[index]
		vm.config.ErrorFn(vm, ErrorStackTrace, fn.Module.Name.Value, line, fn.Debug.Name)
	}
}
