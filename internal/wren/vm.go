// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren VM's src/vm/wren_vm.c and src/include/wren.h.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import "runtime"

// ErrorType says which kind of error the host's error function is being told
// about.
type ErrorType int

const (
	// ErrorCompile is a syntax or compile error; the module and line are set.
	ErrorCompile ErrorType = iota
	// ErrorRuntime is a runtime error; there is no module or line.
	ErrorRuntime
	// ErrorStackTrace is one frame of the stack trace that follows a runtime
	// error; the module, line and function name are set.
	ErrorStackTrace
)

// InterpretResult is the outcome of interpreting a chunk of source.
type InterpretResult int

const (
	ResultSuccess InterpretResult = iota
	ResultCompileError
	ResultRuntimeError
)

// LoadModuleResult is what the host returns for a module import: the module's
// source, or an empty Source if it could not provide it.
type LoadModuleResult struct {
	Source string
	Found  bool
}

// Config is the host's configuration of a VM.
type Config struct {
	// ResolveModuleFn turns the name in an import into the canonical name of
	// the module, given the importing module's name. Returning false aborts
	// the import with an error.
	ResolveModuleFn func(vm *VM, importer, module string) (string, bool)

	// LoadModuleFn provides the source of an imported module.
	LoadModuleFn func(vm *VM, module string) LoadModuleResult

	// WriteFn is called with everything the script writes.
	WriteFn func(vm *VM, text string)

	// ErrorFn is called with every compile and runtime error.
	ErrorFn func(vm *VM, typ ErrorType, module string, line int, message string)
}

// maxTempRoots is unused here (Go owns the heap) but the limit is kept so the
// VM's shape stays recognisable against the original.

// VM is a Wren virtual machine.
type VM struct {
	boolClass   *ObjClass
	classClass  *ObjClass
	fiberClass  *ObjClass
	fnClass     *ObjClass
	listClass   *ObjClass
	mapClass    *ObjClass
	nullClass   *ObjClass
	numClass    *ObjClass
	objectClass *ObjClass
	rangeClass  *ObjClass
	stringClass *ObjClass

	// fiber is the fiber currently running.
	fiber *ObjFiber

	// modules holds every loaded module, keyed by name; the main module's key
	// is null.
	modules *ObjMap

	// lastModule is the module whose body most recently finished running.
	lastModule *ObjModule

	// apiStack is the base index, in the running fiber's stack, of the slots a
	// foreign method may use. apiStackValid says whether it is set.
	apiStack      int
	apiStackValid bool

	config Config

	// compiler is the compiler currently running, if any.
	compiler *Compiler

	// methodNames is the one global symbol table of method names; a call is
	// dispatched by the name's index in it.
	methodNames SymbolTable
}

// NewVM creates a VM with the given configuration and the core library
// already set up.
func NewVM(config Config) *VM {
	vm := &VM{config: config}
	vm.modules = vm.NewMap()
	vm.initializeCore()
	return vm
}

// Write hands text to the host's write function.
func (vm *VM) Write(text string) {
	if vm.config.WriteFn != nil {
		vm.config.WriteFn(vm, text)
	}
}

// captureUpvalue captures the local in the given stack slot, reusing an
// existing open upvalue for the same slot so that several closures over one
// variable really do see the same variable.
func (vm *VM) captureUpvalue(fiber *ObjFiber, local int) *ObjUpvalue {
	if fiber.openUpvalues == nil {
		fiber.openUpvalues = vm.NewUpvalue(fiber, local)
		return fiber.openUpvalues
	}

	var prevUpvalue *ObjUpvalue
	upvalue := fiber.openUpvalues

	// Walk down the stack until an existing upvalue turns up or we pass where
	// it would be. The list is kept sorted from the top of the stack down.
	for upvalue != nil && upvalue.slot > local {
		prevUpvalue = upvalue
		upvalue = upvalue.next
	}

	if upvalue != nil && upvalue.slot == local {
		return upvalue
	}

	createdUpvalue := vm.NewUpvalue(fiber, local)
	if prevUpvalue == nil {
		fiber.openUpvalues = createdUpvalue
	} else {
		prevUpvalue.next = createdUpvalue
	}
	createdUpvalue.next = upvalue
	return createdUpvalue
}

// closeUpvalues closes every open upvalue for a stack slot at last or above.
func closeUpvalues(fiber *ObjFiber, last int) {
	for fiber.openUpvalues != nil && fiber.openUpvalues.slot >= last {
		upvalue := fiber.openUpvalues
		upvalue.closed = fiber.stack[upvalue.slot]
		upvalue.isOpen = false
		fiber.openUpvalues = upvalue.next
	}
}

// findForeignMethod looks up a foreign method, trying the built-in optional
// modules since this VM binds no host foreign methods of its own.
func (vm *VM) findForeignMethod(moduleName, className string, isStatic bool,
	signature string) ForeignMethodFn {
	if moduleName == "meta" {
		return metaBindForeignMethod(className, isStatic, signature)
	}
	if moduleName == "random" {
		return randomBindForeignMethod(className, isStatic, signature)
	}
	return nil
}

// bindMethodValue defines a method on a class. methodValue is a string holding
// the signature for a foreign method, or a function for a Wren method.
func (vm *VM) bindMethodValue(methodType Code, symbol int, module *ObjModule,
	classObj *ObjClass, methodValue Value) {
	className := classObj.Name.Value
	if methodType == CodeMethodStatic {
		classObj = classObj.classObj
	}

	var method Method
	if methodValue.IsString() {
		name := methodValue.AsCString()
		method.typ = MethodForeign
		method.foreign = vm.findForeignMethod(module.Name.Value, className,
			methodType == CodeMethodStatic, name)
		if method.foreign == nil {
			vm.fiber.err = vm.StringFormat(
				"Could not find foreign method '@' for class $ in module '$'.",
				methodValue, classObj.Name.Value, module.Name.Value)
			return
		}
	} else {
		method.closure = methodValue.AsClosure()
		method.typ = MethodBlock
		// Patch up the bytecode now that the superclass is known.
		BindMethodCode(classObj, method.closure.Fn)
	}

	vm.BindMethod(classObj, symbol, method)
}

func (vm *VM) callForeign(fiber *ObjFiber, foreign ForeignMethodFn, numArgs int) {
	vm.apiStack = fiber.stackTop - numArgs
	vm.apiStackValid = true

	foreign(vm)

	// Discard the argument and temporary slots but leave one for the result.
	fiber.stackTop = vm.apiStack + 1
	vm.apiStackValid = false
}

// runtimeError handles the current fiber having aborted. It walks the chain of
// callers, aborting each, until one is willing to catch the error; if none is,
// the stack trace is printed and the VM stops.
func (vm *VM) runtimeError() {
	current := vm.fiber
	err := current.err

	for current != nil {
		// Every fiber along the chain is aborted with the same error.
		current.err = err

		// A caller that ran this fiber with try() is given the error.
		if current.state == FiberTry {
			current.caller.stack[current.caller.stackTop-1] = vm.fiber.err
			vm.fiber = current.caller
			return
		}

		// Otherwise unhook the caller: we will never return to it.
		caller := current.caller
		current.caller = nil
		current = caller
	}

	// Nothing caught the error, so show the stack trace.
	vm.debugPrintStackTrace()
	vm.fiber = nil
	vm.apiStackValid = false
}

// methodNotFound aborts the current fiber with the error for an unknown method.
func (vm *VM) methodNotFound(classObj *ObjClass, symbol int) {
	vm.fiber.err = vm.StringFormat("@ does not implement '$'.",
		ObjVal(classObj.Name), vm.methodNames[symbol].Value)
}

// getModule returns the loaded module with the given name, or nil.
func (vm *VM) getModule(name Value) *ObjModule {
	moduleValue := vm.modules.MapGet(name)
	if moduleValue.IsUndefined() {
		return nil
	}
	return moduleValue.AsModule()
}

func (vm *VM) compileInModule(name Value, source string,
	isExpression, printErrors bool) *ObjClosure {
	module := vm.getModule(name)
	if module == nil {
		module = vm.NewModule(name.AsString())

		// Register it before compiling so the same module is not loaded twice.
		vm.modules.MapSet(name, ObjVal(module))

		// Implicitly import the core module.
		coreModule := vm.getModule(NullVal)
		for i := range coreModule.Variables {
			vm.DefineVariable(module, coreModule.VariableNames[i].Value,
				coreModule.Variables[i], nil)
		}
	}

	fn := vm.Compile(module, source, isExpression, printErrors)
	if fn == nil {
		return nil
	}

	// Functions are always wrapped in closures.
	return vm.NewClosure(fn)
}

// validateSuperclass checks that a class may inherit from the given value. It
// returns null when it may, and the runtime error message when it may not.
func (vm *VM) validateSuperclass(name, superclassValue Value, numFields int) Value {
	if !superclassValue.IsClass() {
		return vm.StringFormat("Class '@' cannot inherit from a non-class object.", name)
	}

	// Inheriting from a sealed built-in type is not allowed: the primitive
	// methods on those classes assume the receiver is the matching object
	// type rather than an instance.
	superclass := superclassValue.AsClass()
	if superclass == vm.classClass ||
		superclass == vm.fiberClass ||
		superclass == vm.fnClass ||
		superclass == vm.listClass ||
		superclass == vm.mapClass ||
		superclass == vm.rangeClass ||
		superclass == vm.stringClass ||
		superclass == vm.boolClass ||
		superclass == vm.nullClass ||
		superclass == vm.numClass {
		return vm.StringFormat("Class '@' cannot inherit from built-in class '@'.",
			name, ObjVal(superclass.Name))
	}

	if superclass.NumFields == -1 {
		return vm.StringFormat("Class '@' cannot inherit from foreign class '@'.",
			name, ObjVal(superclass.Name))
	}

	if numFields == -1 && superclass.NumFields > 0 {
		return vm.StringFormat("Foreign class '@' may not inherit from a class with fields.", name)
	}

	if superclass.NumFields+numFields > maxFields {
		return vm.StringFormat(
			"Class '@' may not have more than 255 fields, including inherited ones.", name)
	}

	return NullVal
}

func (vm *VM) bindForeignClass(classObj *ObjClass, module *ObjModule) {
	// Only the built-in optional modules define foreign classes here, since
	// this VM binds no foreign classes of its own.
	var allocate ForeignMethodFn
	if module.Name.Value == "random" {
		allocate = randomBindForeignClassAllocate(classObj.Name.Value)
	}

	// The symbols are added even when there is no allocator or finalizer, so
	// that they are always present in the symbol table.
	symbol := vm.symbolTableEnsure(&vm.methodNames, "<allocate>")
	if allocate != nil {
		vm.BindMethod(classObj, symbol, Method{typ: MethodForeign, foreign: allocate})
	}
	vm.symbolTableEnsure(&vm.methodNames, "<finalize>")
}

// endClass moves a class's attribute data from compile time to run time. The
// attributes instance and the class are on top of the stack.
func (vm *VM) endClass() {
	attributes := vm.fiber.stack[vm.fiber.stackTop-2]
	classValue := vm.fiber.stack[vm.fiber.stackTop-1]
	vm.fiber.stackTop -= 2

	classValue.AsClass().Attributes = attributes
}

// createClass creates a class whose name and superclass are on top of the
// stack, leaving the class there. numFields of -1 makes a foreign class.
func (vm *VM) createClass(numFields int, module *ObjModule) {
	name := vm.fiber.stack[vm.fiber.stackTop-2]
	superclass := vm.fiber.stack[vm.fiber.stackTop-1]

	// Two values are on the stack and one is left, so discard the other slot.
	vm.fiber.stackTop--

	vm.fiber.err = vm.validateSuperclass(name, superclass, numFields)
	if vm.fiber.hasError() {
		return
	}

	classObj := vm.NewClass(superclass.AsClass(), numFields, name.AsString())
	vm.fiber.stack[vm.fiber.stackTop-1] = ObjVal(classObj)

	if numFields == -1 {
		vm.bindForeignClass(classObj, module)
	}
}

func (vm *VM) createForeign(fiber *ObjFiber, stackStart int) {
	classObj := fiber.stack[stackStart].AsClass()

	symbol := vm.methodNames.Find("<allocate>")
	if symbol == -1 || symbol >= len(classObj.Methods) {
		return
	}
	method := &classObj.Methods[symbol]
	if method.typ != MethodForeign {
		return
	}

	// The constructor arguments are passed to the allocator too.
	vm.apiStack = stackStart
	vm.apiStackValid = true
	method.foreign(vm)
	vm.apiStackValid = false
}

// resolveModule lets the host turn an imported module's name into its
// canonical form.
func (vm *VM) resolveModule(name Value) Value {
	if vm.config.ResolveModuleFn == nil {
		return name
	}

	fiber := vm.fiber
	fn := fiber.frames[len(fiber.frames)-1].closure.Fn
	importer := fn.Module.Name

	resolved, ok := vm.config.ResolveModuleFn(vm, importer.Value, name.AsCString())
	if !ok {
		vm.fiber.err = vm.StringFormat(
			"Could not resolve module '@' imported from '@'.", name, ObjVal(importer))
		return NullVal
	}

	if resolved == name.AsCString() {
		return name
	}
	return vm.NewString(resolved)
}

func (vm *VM) importModule(name Value) Value {
	name = vm.resolveModule(name)
	if vm.fiber.hasError() {
		return NullVal
	}

	// An already loaded module needs nothing done.
	existing := vm.modules.MapGet(name)
	if !existing.IsUndefined() {
		return existing
	}

	var result LoadModuleResult
	if vm.config.LoadModuleFn != nil {
		result = vm.config.LoadModuleFn(vm, name.AsCString())
	}

	// If the host did not provide it, it may be one of the built-in optional
	// modules.
	if !result.Found {
		switch name.AsCString() {
		case "meta":
			result = LoadModuleResult{Source: metaModuleSource, Found: true}
		case "random":
			result = LoadModuleResult{Source: randomModuleSource, Found: true}
		}
	}

	if !result.Found {
		vm.fiber.err = vm.StringFormat("Could not load module '@'.", name)
		return NullVal
	}

	moduleClosure := vm.compileInModule(name, result.Source, false, true)
	if moduleClosure == nil {
		vm.fiber.err = vm.StringFormat("Could not compile module '@'.", name)
		return NullVal
	}

	// Return the closure that runs the module body.
	return ObjVal(moduleClosure)
}

func (vm *VM) getModuleVariable(module *ObjModule, variableName Value) Value {
	variable := variableName.AsString()
	variableEntry := module.VariableNames.Find(variable.Value)

	if variableEntry != -1 {
		return module.Variables[variableEntry]
	}

	vm.fiber.err = vm.StringFormat(
		"Could not find a variable named '@' in module '@'.",
		variableName, ObjVal(module.Name))
	return NullVal
}

func (vm *VM) checkArity(value Value, numArgs int) bool {
	fn := value.AsClosure().Fn

	// Only missing arguments matter, not extra ones. The "- 1" is because
	// numArgs counts the receiver, which is the function itself.
	if numArgs-1 >= fn.Arity {
		return true
	}

	vm.fiber.err = vm.NewString("Function expects more arguments.")
	return false
}

// callFunction pushes closure onto fiber's call stack, expecting numArgs
// arguments (including the receiver) to be on top of the stack already.
func (vm *VM) callFunction(fiber *ObjFiber, closure *ObjClosure, numArgs int) {
	// Grow the stack if needed.
	needed := fiber.stackTop + closure.Fn.MaxSlots
	fiber.ensureStack(needed)

	fiber.appendCallFrame(closure, fiber.stackTop-numArgs)
}

// runInterpreter is the main bytecode interpreter loop.
func (vm *VM) runInterpreter(fiber *ObjFiber) InterpretResult {
	vm.fiber = fiber
	fiber.state = FiberRoot

	var frame *CallFrame
	var stackStart int
	var ip int
	var fn *ObjFn

	loadFrame := func() {
		frame = &fiber.frames[len(fiber.frames)-1]
		stackStart = frame.stackStart
		ip = frame.ip
		fn = frame.closure.Fn
	}

	push := func(value Value) {
		fiber.stack[fiber.stackTop] = value
		fiber.stackTop++
	}
	pop := func() Value {
		fiber.stackTop--
		return fiber.stack[fiber.stackTop]
	}
	drop := func() { fiber.stackTop-- }
	peek := func() Value { return fiber.stack[fiber.stackTop-1] }
	peek2 := func() Value { return fiber.stack[fiber.stackTop-2] }
	readByte := func() int {
		b := fn.Code[ip]
		ip++
		return int(b)
	}
	readShort := func() int {
		ip += 2
		return (int(fn.Code[ip-2]) << 8) | int(fn.Code[ip-1])
	}

	loadFrame()

	for {
		instruction := Code(readByte())

		switch {
		case instruction >= CodeLoadLocal0 && instruction <= CodeLoadLocal8:
			push(fiber.stack[stackStart+int(instruction-CodeLoadLocal0)])

		case instruction == CodeLoadLocal:
			push(fiber.stack[stackStart+readByte()])

		case instruction == CodeLoadFieldThis:
			field := readByte()
			instance := fiber.stack[stackStart].AsInstance()
			push(instance.Fields[field])

		case instruction == CodePop:
			drop()

		case instruction == CodeNull:
			push(NullVal)

		case instruction == CodeFalse:
			push(FalseVal)

		case instruction == CodeTrue:
			push(TrueVal)

		case instruction == CodeStoreLocal:
			fiber.stack[stackStart+readByte()] = peek()

		case instruction == CodeConstant:
			push(fn.Constants[readShort()])

		case (instruction >= CodeCall0 && instruction <= CodeCall16) ||
			(instruction >= CodeSuper0 && instruction <= CodeSuper16):
			// The opcodes for method and superclass calls share everything
			// but where the receiver's class comes from.
			var numArgs, symbol int
			var classObj *ObjClass
			if instruction <= CodeCall16 {
				numArgs = int(instruction-CodeCall0) + 1
				symbol = readShort()
				classObj = vm.GetClass(fiber.stack[fiber.stackTop-numArgs])
			} else {
				numArgs = int(instruction-CodeSuper0) + 1
				symbol = readShort()
				// The superclass is stored in a constant.
				classObj = fn.Constants[readShort()].AsClass()
			}
			argsBase := fiber.stackTop - numArgs

			if symbol >= len(classObj.Methods) ||
				classObj.Methods[symbol].typ == MethodNone {
				vm.methodNotFound(classObj, symbol)
				frame.ip = ip
				vm.runtimeError()
				if vm.fiber == nil {
					return ResultRuntimeError
				}
				fiber = vm.fiber
				loadFrame()
				continue
			}

			method := &classObj.Methods[symbol]
			switch method.typ {
			case MethodPrimitive:
				if method.primitive(vm, fiber.stack[argsBase:]) {
					// The result is in the first argument slot; discard the
					// rest.
					fiber.stackTop -= numArgs - 1
				} else {
					// An error, a fiber switch or a call frame change.
					frame.ip = ip

					fiber = vm.fiber
					if fiber == nil {
						return ResultSuccess
					}
					if fiber.hasError() {
						vm.runtimeError()
						if vm.fiber == nil {
							return ResultRuntimeError
						}
						fiber = vm.fiber
					}
					loadFrame()
				}

			case MethodFunctionCall:
				if !vm.checkArity(fiber.stack[argsBase], numArgs) {
					frame.ip = ip
					vm.runtimeError()
					if vm.fiber == nil {
						return ResultRuntimeError
					}
					fiber = vm.fiber
					loadFrame()
					continue
				}
				frame.ip = ip
				method.primitive(vm, fiber.stack[argsBase:])
				loadFrame()

			case MethodForeign:
				vm.callForeign(fiber, method.foreign, numArgs)
				if fiber.hasError() {
					frame.ip = ip
					vm.runtimeError()
					if vm.fiber == nil {
						return ResultRuntimeError
					}
					fiber = vm.fiber
					loadFrame()
				}

			case MethodBlock:
				frame.ip = ip
				vm.callFunction(fiber, method.closure, numArgs)
				loadFrame()
			}

		case instruction == CodeLoadUpvalue:
			upvalues := frame.closure.Upvalues
			push(upvalues[readByte()].get())

		case instruction == CodeStoreUpvalue:
			upvalues := frame.closure.Upvalues
			upvalues[readByte()].set(peek())

		case instruction == CodeLoadModuleVar:
			push(fn.Module.Variables[readShort()])

		case instruction == CodeStoreModuleVar:
			fn.Module.Variables[readShort()] = peek()

		case instruction == CodeStoreFieldThis:
			field := readByte()
			instance := fiber.stack[stackStart].AsInstance()
			instance.Fields[field] = peek()

		case instruction == CodeLoadField:
			field := readByte()
			receiver := pop()
			push(receiver.AsInstance().Fields[field])

		case instruction == CodeStoreField:
			field := readByte()
			receiver := pop()
			receiver.AsInstance().Fields[field] = peek()

		case instruction == CodeJump:
			offset := readShort()
			ip += offset

		case instruction == CodeLoop:
			// Jump back to the top of the loop.
			offset := readShort()
			ip -= offset

		case instruction == CodeJumpIf:
			offset := readShort()
			condition := pop()
			if condition.IsFalsy() {
				ip += offset
			}

		case instruction == CodeAnd:
			offset := readShort()
			condition := peek()
			if condition.IsFalsy() {
				// Short-circuit the right hand side.
				ip += offset
			} else {
				drop()
			}

		case instruction == CodeOr:
			offset := readShort()
			condition := peek()
			if condition.IsFalsy() {
				drop()
			} else {
				ip += offset
			}

		case instruction == CodeCloseUpvalue:
			closeUpvalues(fiber, fiber.stackTop-1)
			drop()

		case instruction == CodeReturn:
			result := pop()
			fiber.frames = fiber.frames[:len(fiber.frames)-1]

			// Close any upvalues still in scope.
			closeUpvalues(fiber, stackStart)

			if len(fiber.frames) == 0 {
				// See if there is another fiber to return to.
				if fiber.caller == nil {
					// Leave the result at the bottom of the stack.
					fiber.stack[0] = result
					fiber.stackTop = 1
					return ResultSuccess
				}

				resumingFiber := fiber.caller
				fiber.caller = nil
				fiber = resumingFiber
				vm.fiber = resumingFiber

				// Store the result in the resuming fiber.
				fiber.stack[fiber.stackTop-1] = result
			} else {
				// Store the result where the caller expects it and discard
				// the call frame's slots, leaving one for the result.
				fiber.stack[stackStart] = result
				fiber.stackTop = frame.stackStart + 1
			}

			loadFrame()

		case instruction == CodeConstruct:
			fiber.stack[stackStart] = vm.NewInstance(fiber.stack[stackStart].AsClass())

		case instruction == CodeForeignConstruct:
			vm.createForeign(fiber, stackStart)
			if fiber.hasError() {
				frame.ip = ip
				vm.runtimeError()
				if vm.fiber == nil {
					return ResultRuntimeError
				}
				fiber = vm.fiber
				loadFrame()
			}

		case instruction == CodeClosure:
			// Create the closure and push it before capturing the upvalues.
			function := fn.Constants[readShort()].AsFn()
			closure := vm.NewClosure(function)
			push(ObjVal(closure))

			for i := 0; i < function.NumUpvalues; i++ {
				isLocal := readByte()
				index := readByte()
				if isLocal != 0 {
					closure.Upvalues[i] = vm.captureUpvalue(fiber, frame.stackStart+index)
				} else {
					closure.Upvalues[i] = frame.closure.Upvalues[index]
				}
			}

		case instruction == CodeEndClass:
			vm.endClass()
			if fiber.hasError() {
				frame.ip = ip
				vm.runtimeError()
				if vm.fiber == nil {
					return ResultRuntimeError
				}
				fiber = vm.fiber
				loadFrame()
			}

		case instruction == CodeClass:
			vm.createClass(readByte(), nil)
			if fiber.hasError() {
				frame.ip = ip
				vm.runtimeError()
				if vm.fiber == nil {
					return ResultRuntimeError
				}
				fiber = vm.fiber
				loadFrame()
			}

		case instruction == CodeForeignClass:
			vm.createClass(-1, fn.Module)
			if fiber.hasError() {
				frame.ip = ip
				vm.runtimeError()
				if vm.fiber == nil {
					return ResultRuntimeError
				}
				fiber = vm.fiber
				loadFrame()
			}

		case instruction == CodeMethodInstance || instruction == CodeMethodStatic:
			symbol := readShort()
			classObj := peek().AsClass()
			method := peek2()
			vm.bindMethodValue(instruction, symbol, fn.Module, classObj, method)
			if fiber.hasError() {
				frame.ip = ip
				vm.runtimeError()
				if vm.fiber == nil {
					return ResultRuntimeError
				}
				fiber = vm.fiber
				loadFrame()
				continue
			}
			drop()
			drop()

		case instruction == CodeEndModule:
			vm.lastModule = fn.Module
			push(NullVal)

		case instruction == CodeImportModule:
			// Make a slot for the module fiber's return value, and keep the
			// imported module's closure in it meanwhile.
			push(vm.importModule(fn.Constants[readShort()]))
			if fiber.hasError() {
				frame.ip = ip
				vm.runtimeError()
				if vm.fiber == nil {
					return ResultRuntimeError
				}
				fiber = vm.fiber
				loadFrame()
				continue
			}

			// With a closure, call it to run the module body.
			if peek().IsClosure() {
				frame.ip = ip
				closure := peek().AsClosure()
				vm.callFunction(fiber, closure, 1)
				loadFrame()
			} else {
				// The module was already loaded; remember it so variables can
				// be imported from it.
				vm.lastModule = peek().AsModule()
			}

		case instruction == CodeImportVariable:
			variable := fn.Constants[readShort()]
			result := vm.getModuleVariable(vm.lastModule, variable)
			if fiber.hasError() {
				frame.ip = ip
				vm.runtimeError()
				if vm.fiber == nil {
					return ResultRuntimeError
				}
				fiber = vm.fiber
				loadFrame()
				continue
			}
			push(result)

		case instruction == CodeEnd:
			// A CODE_END is always preceded by a CODE_RETURN, so reaching it
			// means the compiler generated wrong code.
			return ResultRuntimeError
		}
	}
}

// Interpret compiles and runs source as the body of the named module.
func (vm *VM) Interpret(module, source string) InterpretResult {
	closure := vm.CompileSource(module, source, false, true)
	if closure == nil {
		return ResultCompileError
	}

	fiber := vm.NewFiber(closure)
	vm.apiStackValid = false

	return vm.runInterpreter(fiber)
}

// CompileSource compiles source in the context of the named module and wraps
// it in a closure that can run it.
func (vm *VM) CompileSource(module, source string, isExpression, printErrors bool) *ObjClosure {
	nameValue := NullVal
	if module != "" {
		nameValue = vm.NewString(module)
	}
	return vm.compileInModule(nameValue, source, isExpression, printErrors)
}

// interpretCore runs the core module's own source, whose module name is null.
func (vm *VM) interpretCore(source string) InterpretResult {
	closure := vm.compileInModule(NullVal, source, false, true)
	if closure == nil {
		return ResultCompileError
	}
	fiber := vm.NewFiber(closure)
	vm.apiStackValid = false
	return vm.runInterpreter(fiber)
}

// FindVariable returns the value of the module-level variable named name.
func (vm *VM) FindVariable(module *ObjModule, name string) Value {
	symbol := module.VariableNames.Find(name)
	return module.Variables[symbol]
}

// DeclareVariable adds an implicitly declared top-level variable named name,
// recording the line of the use site that forced it. It returns the new
// variable's symbol, or -2 if there are too many variables.
func (vm *VM) DeclareVariable(module *ObjModule, name string, line int) int {
	if len(module.Variables) == maxModuleVars {
		return -2
	}

	// An implicitly declared variable's "value" is the line it was first used
	// on, so the error can be reported there later.
	module.Variables = append(module.Variables, NumVal(float64(line)))
	return vm.symbolTableAdd(&module.VariableNames, name)
}

// DefineVariable adds a top-level variable named name with the given value,
// and if line is not nil stores the line of the implicit first use there. It
// returns the variable's symbol, -1 if one with that name is already defined,
// -2 if there are too many variables, or -3 if this is a lowercase top-level
// variable that was used before being defined.
func (vm *VM) DefineVariable(module *ObjModule, name string, value Value, line *int) int {
	if len(module.Variables) == maxModuleVars {
		return -2
	}

	symbol := module.VariableNames.Find(name)

	if symbol == -1 {
		symbol = vm.symbolTableAdd(&module.VariableNames, name)
		module.Variables = append(module.Variables, value)
	} else if module.Variables[symbol].IsNum() {
		// An implicitly declared variable's value is always a number; this is
		// the real definition.
		if line != nil {
			*line = int(module.Variables[symbol].AsNum())
		}
		module.Variables[symbol] = value

		// A lowercase name that was referenced before this definition is an
		// error.
		if IsLocalName(name) {
			symbol = -3
		}
	} else {
		symbol = -1
	}

	return symbol
}

// IsLocalName reports whether name is a local variable name, that is whether
// it starts with a lowercase letter.
func IsLocalName(name string) bool {
	return len(name) > 0 && name[0] >= 'a' && name[0] <= 'z'
}

// CollectGarbage runs a collection. Wren's own collector is replaced by Go's,
// so this simply asks the Go runtime to collect.
func (vm *VM) CollectGarbage() { runtime.GC() }

// The slot API, as far as the built-in optional modules need it ---------------

func (vm *VM) slot(slot int) Value { return vm.fiber.stack[vm.apiStack+slot] }

func (vm *VM) setSlot(slot int, value Value) { vm.fiber.stack[vm.apiStack+slot] = value }

// EnsureSlots makes sure the foreign call has at least numSlots slots.
func (vm *VM) EnsureSlots(numSlots int) {
	if !vm.apiStackValid {
		vm.fiber = vm.NewFiber(nil)
		vm.apiStack = 0
		vm.apiStackValid = true
	}
	currentSize := vm.fiber.stackTop - vm.apiStack
	if currentSize >= numSlots {
		return
	}
	needed := vm.apiStack + numSlots
	vm.fiber.ensureStack(needed)
	vm.fiber.stackTop = vm.apiStack + numSlots
}

// GetSlotDouble, GetSlotString and GetSlotBool read a foreign call's slots.
func (vm *VM) GetSlotDouble(slot int) float64 { return vm.slot(slot).AsNum() }
func (vm *VM) GetSlotString(slot int) string  { return vm.slot(slot).AsCString() }
func (vm *VM) GetSlotBool(slot int) bool      { return vm.slot(slot).AsBool() }

// GetSlotForeign returns the payload of the foreign object in a slot.
func (vm *VM) GetSlotForeign(slot int) *ObjForeign { return vm.slot(slot).AsForeign() }

// SetSlotDouble and SetSlotNull write a foreign call's slots.
func (vm *VM) SetSlotDouble(slot int, value float64) { vm.setSlot(slot, NumVal(value)) }
func (vm *VM) SetSlotNull(slot int)                  { vm.setSlot(slot, NullVal) }

// SetSlotNewForeign creates a foreign instance of the class in classSlot and
// stores it in slot, returning it so its payload can be filled in.
func (vm *VM) SetSlotNewForeign(slot, classSlot int) *ObjForeign {
	classObj := vm.slot(classSlot).AsClass()
	foreign := vm.NewForeign(classObj)
	vm.setSlot(slot, ObjVal(foreign))
	return foreign
}
