// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren VM's src/optional/wren_opt_meta.c.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

// metaCompile compiles a string of source in the module that called it and
// returns the resulting closure, or null if it did not compile.
func metaCompile(vm *VM) {
	source := vm.GetSlotString(1)
	isExpression := vm.GetSlotBool(2)
	printErrors := vm.GetSlotBool(3)

	// Look up the module surrounding the call site. The -2 walks up the call
	// stack assuming the meta module adds one level of indirection before the
	// user's code; any change to meta may need this adjusted.
	currentFiber := vm.fiber
	fn := currentFiber.frames[len(currentFiber.frames)-2].closure.Fn
	module := fn.Module.Name

	closure := vm.CompileSource(module.Value, source, isExpression, printErrors)

	if closure == nil {
		vm.setSlot(0, NullVal)
	} else {
		vm.setSlot(0, ObjVal(closure))
	}
}

// metaGetModuleVariables returns the names of every top-level variable of a
// loaded module, or null if no module of that name is loaded.
func metaGetModuleVariables(vm *VM) {
	vm.EnsureSlots(3)

	moduleValue := vm.modules.MapGet(vm.slot(1))
	if moduleValue.IsUndefined() {
		vm.setSlot(0, NullVal)
		return
	}

	module := moduleValue.AsModule()
	names := vm.NewList(len(module.VariableNames))
	vm.setSlot(0, ObjVal(names))

	for i := range names.Elements {
		names.Elements[i] = ObjVal(module.VariableNames[i])
	}
}

func metaBindForeignMethod(className string, isStatic bool, signature string) ForeignMethodFn {
	switch signature {
	case "compile_(_,_,_)":
		return metaCompile
	case "getModuleVariables_(_)":
		return metaGetModuleVariables
	}
	return nil
}
