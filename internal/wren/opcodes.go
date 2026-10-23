// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren VM's src/vm/wren_opcodes.h.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

// Code is a bytecode instruction.
type Code int

// The bytecode instructions, in the order the original defines them. Each one
// is listed below with its stack effect: the amount by which it changes the
// size of the stack.
const (
	// CodeConstant loads the constant at the index in its argument.
	CodeConstant Code = iota
	// CodeNull, CodeFalse and CodeTrue push those values.
	CodeNull
	CodeFalse
	CodeTrue

	// CodeLoadLocal0..8 push the value in that local slot.
	CodeLoadLocal0
	CodeLoadLocal1
	CodeLoadLocal2
	CodeLoadLocal3
	CodeLoadLocal4
	CodeLoadLocal5
	CodeLoadLocal6
	CodeLoadLocal7
	CodeLoadLocal8

	// CodeLoadLocal pushes the value in the local slot in its argument, and
	// CodeStoreLocal stores the top of the stack there without popping it.
	CodeLoadLocal
	CodeStoreLocal

	CodeLoadUpvalue
	CodeStoreUpvalue

	CodeLoadModuleVar
	CodeStoreModuleVar

	// CodeLoadFieldThis and CodeStoreFieldThis act on a field of the receiver
	// of the current method, which is the common case.
	CodeLoadFieldThis
	CodeStoreFieldThis

	// CodeLoadField pops an instance and pushes one of its fields;
	// CodeStoreField pops an instance and stores into one of its fields.
	CodeLoadField
	CodeStoreField

	CodePop

	// CodeCall0..16 invoke the method with the symbol in the argument; the
	// number is the argument count, not counting the receiver.
	CodeCall0
	CodeCall1
	CodeCall2
	CodeCall3
	CodeCall4
	CodeCall5
	CodeCall6
	CodeCall7
	CodeCall8
	CodeCall9
	CodeCall10
	CodeCall11
	CodeCall12
	CodeCall13
	CodeCall14
	CodeCall15
	CodeCall16

	// CodeSuper0..16 invoke a superclass method.
	CodeSuper0
	CodeSuper1
	CodeSuper2
	CodeSuper3
	CodeSuper4
	CodeSuper5
	CodeSuper6
	CodeSuper7
	CodeSuper8
	CodeSuper9
	CodeSuper10
	CodeSuper11
	CodeSuper12
	CodeSuper13
	CodeSuper14
	CodeSuper15
	CodeSuper16

	CodeJump
	CodeLoop
	CodeJumpIf

	// CodeAnd jumps forward if the top of the stack is false, otherwise pops
	// it; CodeOr does the opposite.
	CodeAnd
	CodeOr

	CodeCloseUpvalue
	CodeReturn

	// CodeClosure creates a closure for the function at the constant index in
	// its argument, followed by two bytes per upvalue.
	CodeClosure

	// CodeConstruct replaces the class in slot zero with a new instance of it,
	// and CodeForeignConstruct does the same for a foreign class.
	CodeConstruct
	CodeForeignConstruct

	// CodeClass creates a class; the argument is its field count.
	CodeClass
	// CodeEndClass ends a class, taking its ClassAttributes.
	CodeEndClass
	CodeForeignClass

	CodeMethodInstance
	CodeMethodStatic

	// CodeEndModule runs at the end of a module body.
	CodeEndModule

	CodeImportModule
	CodeImportVariable

	// CodeEnd marks the end of the bytecode; it is never executed.
	CodeEnd

	numCodes
)

// buildStackEffects returns the stack effect of each opcode, in the same order
// as the constants above.
func buildStackEffects() [numCodes]int {
	var effects [numCodes]int
	effects[CodeConstant] = 1
	effects[CodeNull] = 1
	effects[CodeFalse] = 1
	effects[CodeTrue] = 1
	for i := 0; i <= 8; i++ {
		effects[int(CodeLoadLocal0)+i] = 1
	}
	effects[CodeLoadLocal] = 1
	effects[CodeStoreLocal] = 0
	effects[CodeLoadUpvalue] = 1
	effects[CodeStoreUpvalue] = 0
	effects[CodeLoadModuleVar] = 1
	effects[CodeStoreModuleVar] = 0
	effects[CodeLoadFieldThis] = 1
	effects[CodeStoreFieldThis] = 0
	effects[CodeLoadField] = 0
	effects[CodeStoreField] = -1
	effects[CodePop] = -1
	for i := 0; i <= 16; i++ {
		effects[int(CodeCall0)+i] = -i
		effects[int(CodeSuper0)+i] = -i
	}
	effects[CodeJump] = 0
	effects[CodeLoop] = 0
	effects[CodeJumpIf] = -1
	effects[CodeAnd] = -1
	effects[CodeOr] = -1
	effects[CodeCloseUpvalue] = -1
	effects[CodeReturn] = 0
	effects[CodeClosure] = 1
	effects[CodeConstruct] = 0
	effects[CodeForeignConstruct] = 0
	effects[CodeClass] = -1
	effects[CodeEndClass] = -2
	effects[CodeForeignClass] = -1
	effects[CodeMethodInstance] = -2
	effects[CodeMethodStatic] = -2
	effects[CodeEndModule] = 1
	effects[CodeImportModule] = 1
	effects[CodeImportVariable] = 1
	effects[CodeEnd] = 0
	return effects
}
