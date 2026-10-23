// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren VM's src/vm/wren_core.c and src/vm/wren_primitive.c.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import (
	"math"
	"strconv"
	"strings"
)

// Argument validation ---------------------------------------------------------

// validateIndexValue checks that value is an integer in [0, count), allowing a
// negative index to count back from the end. It returns the positive index, or
// -1 after reporting an error.
func validateIndexValue(vm *VM, count int, value float64, argName string) int {
	if !validateIntValue(vm, value, argName) {
		return -1
	}
	if value < 0 {
		value = float64(count) + value
	}
	if value >= 0 && value < float64(count) {
		return int(value)
	}
	vm.fiber.err = vm.StringFormat("$ out of bounds.", argName)
	return -1
}

func validateFn(vm *VM, arg Value, argName string) bool {
	if arg.IsClosure() {
		return true
	}
	vm.fiber.err = vm.StringFormat("$ must be a function.", argName)
	return false
}

func validateNum(vm *VM, arg Value, argName string) bool {
	if arg.IsNum() {
		return true
	}
	vm.fiber.err = vm.StringFormat("$ must be a number.", argName)
	return false
}

func validateIntValue(vm *VM, value float64, argName string) bool {
	if math.Trunc(value) == value {
		return true
	}
	vm.fiber.err = vm.StringFormat("$ must be an integer.", argName)
	return false
}

func validateInt(vm *VM, arg Value, argName string) bool {
	if !validateNum(vm, arg, argName) {
		return false
	}
	return validateIntValue(vm, arg.AsNum(), argName)
}

func validateKey(vm *VM, arg Value) bool {
	if MapIsValidKey(arg) {
		return true
	}
	vm.fiber.err = vm.NewString("Key must be a value type.")
	return false
}

func validateIndex(vm *VM, arg Value, count int, argName string) int {
	if !validateNum(vm, arg, argName) {
		return -1
	}
	return validateIndexValue(vm, count, arg.AsNum(), argName)
}

func validateString(vm *VM, arg Value, argName string) bool {
	if arg.IsString() {
		return true
	}
	vm.fiber.err = vm.StringFormat("$ must be a string.", argName)
	return false
}

// calculateRange works out which elements of a sequence of the given length a
// range picks out. It returns the index to start at (or -1 on error), the
// number of elements, and the direction to step in.
func calculateRange(vm *VM, r *ObjRange, length int) (start, newLength, step int) {
	step = 0

	// An empty range is allowed at the very end of a sequence, so that
	// list[0..-1] and list[0...list.count] both copy a list even when empty.
	end := float64(length)
	if r.IsInclusive {
		end = -1.0
	}
	if r.From == float64(length) && r.To == end {
		return 0, 0, 0
	}

	from := validateIndexValue(vm, length, r.From, "Range start")
	if from == -1 {
		return -1, length, 0
	}

	// The end is bounds-checked by hand so that exclusive ranges work.
	value := r.To
	if !validateIntValue(vm, value, "Range end") {
		return -1, length, 0
	}

	if value < 0 {
		value = float64(length) + value
	}

	if !r.IsInclusive {
		// An exclusive range with the same start and end is empty.
		if value == float64(from) {
			return from, 0, 0
		}
		// Shift the end point to make the range inclusive, in whichever
		// direction the range runs.
		if value >= float64(from) {
			value += -1
		} else {
			value += 1
		}
	}

	if value < 0 || value >= float64(length) {
		vm.fiber.err = vm.NewString("Range end out of bounds.")
		return -1, length, 0
	}

	to := int(value)
	newLength = abs32(from-to) + 1
	if from < to {
		step = 1
	} else {
		step = -1
	}
	return from, newLength, step
}

func abs32(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// toUint32 converts a double the way the C implementation's `(uint32_t)`
// conversion does on the platforms the original is built for: truncate toward
// zero and keep the low 32 bits. Values outside the range of a 64-bit signed
// integer are undefined in C and give zero here.
func toUint32(d float64) uint32 {
	if math.IsNaN(d) || d >= 9223372036854775808.0 || d < -9223372036854775808.0 {
		return 0
	}
	return uint32(int64(d))
}

// Bool ------------------------------------------------------------------------

func primBoolNot(vm *VM, args []Value) bool {
	args[0] = BoolVal(!args[0].AsBool())
	return true
}

func primBoolToString(vm *VM, args []Value) bool {
	if args[0].AsBool() {
		args[0] = vm.NewString("true")
	} else {
		args[0] = vm.NewString("false")
	}
	return true
}

// Class -----------------------------------------------------------------------

func primClassName(vm *VM, args []Value) bool {
	args[0] = ObjVal(args[0].AsClass().Name)
	return true
}

func primClassSupertype(vm *VM, args []Value) bool {
	classObj := args[0].AsClass()
	// Object has no superclass.
	if classObj.Superclass == nil {
		args[0] = NullVal
		return true
	}
	args[0] = ObjVal(classObj.Superclass)
	return true
}

func primClassToString(vm *VM, args []Value) bool {
	args[0] = ObjVal(args[0].AsClass().Name)
	return true
}

func primClassAttributes(vm *VM, args []Value) bool {
	args[0] = args[0].AsClass().Attributes
	return true
}

// Fiber -----------------------------------------------------------------------

func primFiberNew(vm *VM, args []Value) bool {
	if !validateFn(vm, args[1], "Argument") {
		return false
	}
	closure := args[1].AsClosure()
	if closure.Fn.Arity > 1 {
		vm.fiber.err = vm.NewString("Function cannot take more than one parameter.")
		return false
	}
	args[0] = ObjVal(vm.NewFiber(closure))
	return true
}

func primFiberAbort(vm *VM, args []Value) bool {
	vm.fiber.err = args[1]
	// An explicitly null error is not really an abort.
	return args[1].IsNull()
}

// runFiber transfers execution to a fiber. isCall is true when the fiber is
// being called rather than transferred to, and hasValue is true when a value
// from args is being passed to it rather than null.
func runFiber(vm *VM, fiber *ObjFiber, args []Value, isCall, hasValue bool, verb string) bool {
	if fiber.hasError() {
		vm.fiber.err = vm.StringFormat("Cannot $ an aborted fiber.", verb)
		return false
	}

	if isCall {
		// A called fiber cannot be called again, but it can be transferred to
		// directly, which is why this is gated on isCall: after resuming a
		// suspended fiber it runs and then returns to its caller.
		if fiber.caller != nil {
			vm.fiber.err = vm.NewString("Fiber has already been called.")
			return false
		}
		if fiber.state == FiberRoot {
			vm.fiber.err = vm.NewString("Cannot call root fiber.")
			return false
		}
		fiber.caller = vm.fiber
	}

	if len(fiber.frames) == 0 {
		vm.fiber.err = vm.StringFormat("Cannot $ a finished fiber.", verb)
		return false
	}

	// The calling fiber needs only one slot for the result of the call, so
	// with two arguments (the fiber and the value) discard the other now.
	if hasValue {
		vm.fiber.stackTop--
	}

	if len(fiber.frames) == 1 && fiber.frames[0].ip == 0 {
		// The fiber is being started for the first time; bind an argument to
		// its parameter if it takes one.
		if fiber.frames[0].closure.Fn.Arity == 1 {
			value := NullVal
			if hasValue {
				value = args[1]
			}
			fiber.stack[fiber.stackTop] = value
			fiber.stackTop++
		}
	} else {
		// The fiber is being resumed: make yield() or transfer() return the
		// value.
		value := NullVal
		if hasValue {
			value = args[1]
		}
		fiber.stack[fiber.stackTop-1] = value
	}

	vm.fiber = fiber
	return false
}

func primFiberCall(vm *VM, args []Value) bool {
	return runFiber(vm, args[0].AsFiber(), args, true, false, "call")
}

func primFiberCall1(vm *VM, args []Value) bool {
	return runFiber(vm, args[0].AsFiber(), args, true, true, "call")
}

func primFiberCurrent(vm *VM, args []Value) bool {
	args[0] = ObjVal(vm.fiber)
	return true
}

func primFiberError(vm *VM, args []Value) bool {
	args[0] = args[0].AsFiber().err
	return true
}

func primFiberIsDone(vm *VM, args []Value) bool {
	f := args[0].AsFiber()
	args[0] = BoolVal(len(f.frames) == 0 || f.hasError())
	return true
}

func primFiberSuspend(vm *VM, args []Value) bool {
	// Switching to no fiber at all tells the interpreter to stop and exit.
	vm.fiber = nil
	vm.apiStackValid = false
	return false
}

func primFiberTransfer(vm *VM, args []Value) bool {
	return runFiber(vm, args[0].AsFiber(), args, false, false, "transfer to")
}

func primFiberTransfer1(vm *VM, args []Value) bool {
	return runFiber(vm, args[0].AsFiber(), args, false, true, "transfer to")
}

func primFiberTransferError(vm *VM, args []Value) bool {
	runFiber(vm, args[0].AsFiber(), args, false, true, "transfer to")
	vm.fiber.err = args[1]
	return false
}

func primFiberTry(vm *VM, args []Value) bool {
	runFiber(vm, args[0].AsFiber(), args, true, false, "try")
	// Having switched to a valid fiber, remember that it is being tried.
	if !vm.fiber.hasError() {
		vm.fiber.state = FiberTry
	}
	return false
}

func primFiberTry1(vm *VM, args []Value) bool {
	runFiber(vm, args[0].AsFiber(), args, true, true, "try")
	if !vm.fiber.hasError() {
		vm.fiber.state = FiberTry
	}
	return false
}

func primFiberYield(vm *VM, args []Value) bool {
	current := vm.fiber
	vm.fiber = current.caller

	current.caller = nil
	current.state = FiberOther

	if vm.fiber != nil {
		// Make the caller's run method return null.
		vm.fiber.stack[vm.fiber.stackTop-1] = NullVal
	}
	return false
}

func primFiberYield1(vm *VM, args []Value) bool {
	current := vm.fiber
	vm.fiber = current.caller

	current.caller = nil
	current.state = FiberOther

	if vm.fiber != nil {
		// Make the caller's run method return the value passed to yield.
		vm.fiber.stack[vm.fiber.stackTop-1] = args[1]

		// Fiber.yield(value) has two arguments and only one slot is needed
		// for the result of the yield, so discard the other now.
		current.stackTop--
	}
	return false
}

// Fn --------------------------------------------------------------------------

func primFnNew(vm *VM, args []Value) bool {
	if !validateFn(vm, args[1], "Argument") {
		return false
	}
	// The block argument is already a function, so just return it.
	args[0] = args[1]
	return true
}

func primFnArity(vm *VM, args []Value) bool {
	args[0] = NumVal(float64(args[0].AsClosure().Fn.Arity))
	return true
}

func makeFnCall(numArgs int) Primitive {
	return func(vm *VM, args []Value) bool {
		// +1 to include the function itself.
		vm.callFunction(vm.fiber, args[0].AsClosure(), numArgs+1)
		return false
	}
}

func primFnToString(vm *VM, args []Value) bool {
	args[0] = vm.NewString("<fn>")
	return true
}

// List ------------------------------------------------------------------------

func primListFilled(vm *VM, args []Value) bool {
	if !validateInt(vm, args[1], "Size") {
		return false
	}
	if args[1].AsNum() < 0 {
		vm.fiber.err = vm.NewString("Size cannot be negative.")
		return false
	}
	size := int(uint32(args[1].AsNum()))
	list := vm.NewList(size)
	for i := 0; i < size; i++ {
		list.Elements[i] = args[2]
	}
	args[0] = ObjVal(list)
	return true
}

func primListNew(vm *VM, args []Value) bool {
	args[0] = ObjVal(vm.NewList(0))
	return true
}

func primListAdd(vm *VM, args []Value) bool {
	args[0].AsList().listWrite(args[1])
	args[0] = args[1]
	return true
}

// primListAddCore adds an element and returns the list itself. The compiler
// uses it for list literals to keep stack churn down.
func primListAddCore(vm *VM, args []Value) bool {
	args[0].AsList().listWrite(args[1])
	return true
}

func primListClear(vm *VM, args []Value) bool {
	list := args[0].AsList()
	list.Elements = nil
	list.capacity = 0
	args[0] = NullVal
	return true
}

func primListCount(vm *VM, args []Value) bool {
	args[0] = NumVal(float64(len(args[0].AsList().Elements)))
	return true
}

func primListInsert(vm *VM, args []Value) bool {
	list := args[0].AsList()
	// count + 1 so that inserting at the very end works.
	index := validateIndex(vm, args[1], len(list.Elements)+1, "Index")
	if index == -1 {
		return false
	}
	list.ListInsert(args[2], index)
	args[0] = args[2]
	return true
}

func primListIterate(vm *VM, args []Value) bool {
	list := args[0].AsList()

	// Starting the iteration returns the first index.
	if args[1].IsNull() {
		if len(list.Elements) == 0 {
			args[0] = FalseVal
			return true
		}
		args[0] = NumVal(0)
		return true
	}

	if !validateInt(vm, args[1], "Iterator") {
		return false
	}

	index := args[1].AsNum()
	if index < 0 || index >= float64(len(list.Elements)-1) {
		args[0] = FalseVal
		return true
	}

	args[0] = NumVal(index + 1)
	return true
}

func primListIteratorValue(vm *VM, args []Value) bool {
	list := args[0].AsList()
	index := validateIndex(vm, args[1], len(list.Elements), "Iterator")
	if index == -1 {
		return false
	}
	args[0] = list.Elements[index]
	return true
}

func primListRemoveAt(vm *VM, args []Value) bool {
	list := args[0].AsList()
	index := validateIndex(vm, args[1], len(list.Elements), "Index")
	if index == -1 {
		return false
	}
	args[0] = list.ListRemoveAt(index)
	return true
}

func primListRemoveValue(vm *VM, args []Value) bool {
	list := args[0].AsList()
	index := list.ListIndexOf(args[1])
	if index == -1 {
		args[0] = NullVal
		return true
	}
	args[0] = list.ListRemoveAt(index)
	return true
}

func primListIndexOf(vm *VM, args []Value) bool {
	list := args[0].AsList()
	args[0] = NumVal(float64(list.ListIndexOf(args[1])))
	return true
}

func primListSwap(vm *VM, args []Value) bool {
	list := args[0].AsList()
	indexA := validateIndex(vm, args[1], len(list.Elements), "Index 0")
	if indexA == -1 {
		return false
	}
	indexB := validateIndex(vm, args[2], len(list.Elements), "Index 1")
	if indexB == -1 {
		return false
	}
	list.Elements[indexA], list.Elements[indexB] = list.Elements[indexB], list.Elements[indexA]
	args[0] = NullVal
	return true
}

func primListSubscript(vm *VM, args []Value) bool {
	list := args[0].AsList()

	if args[1].IsNum() {
		index := validateIndex(vm, args[1], len(list.Elements), "Subscript")
		if index == -1 {
			return false
		}
		args[0] = list.Elements[index]
		return true
	}

	if !args[1].IsRange() {
		vm.fiber.err = vm.NewString("Subscript must be a number or a range.")
		return false
	}

	start, count, step := calculateRange(vm, args[1].AsRange(), len(list.Elements))
	if start == -1 {
		return false
	}

	result := vm.NewList(count)
	for i := 0; i < count; i++ {
		result.Elements[i] = list.Elements[start+i*step]
	}
	args[0] = ObjVal(result)
	return true
}

func primListSubscriptSetter(vm *VM, args []Value) bool {
	list := args[0].AsList()
	index := validateIndex(vm, args[1], len(list.Elements), "Subscript")
	if index == -1 {
		return false
	}
	list.Elements[index] = args[2]
	args[0] = args[2]
	return true
}

// Map -------------------------------------------------------------------------

func primMapNew(vm *VM, args []Value) bool {
	args[0] = ObjVal(vm.NewMap())
	return true
}

func primMapSubscript(vm *VM, args []Value) bool {
	if !validateKey(vm, args[1]) {
		return false
	}
	value := args[0].AsMap().MapGet(args[1])
	if value.IsUndefined() {
		args[0] = NullVal
		return true
	}
	args[0] = value
	return true
}

func primMapSubscriptSetter(vm *VM, args []Value) bool {
	if !validateKey(vm, args[1]) {
		return false
	}
	args[0].AsMap().MapSet(args[1], args[2])
	args[0] = args[2]
	return true
}

// primMapAddCore adds an entry and returns the map itself. The compiler uses
// it for map literals to keep stack churn down.
func primMapAddCore(vm *VM, args []Value) bool {
	if !validateKey(vm, args[1]) {
		return false
	}
	args[0].AsMap().MapSet(args[1], args[2])
	return true
}

func primMapClear(vm *VM, args []Value) bool {
	args[0].AsMap().MapClear()
	args[0] = NullVal
	return true
}

func primMapContainsKey(vm *VM, args []Value) bool {
	if !validateKey(vm, args[1]) {
		return false
	}
	args[0] = BoolVal(!args[0].AsMap().MapGet(args[1]).IsUndefined())
	return true
}

func primMapCount(vm *VM, args []Value) bool {
	args[0] = NumVal(float64(args[0].AsMap().Count))
	return true
}

func primMapIterate(vm *VM, args []Value) bool {
	m := args[0].AsMap()

	if m.Count == 0 {
		args[0] = FalseVal
		return true
	}

	// Starting the iteration starts at the first used entry.
	index := uint32(0)

	// Otherwise start one past the entry we stopped at.
	if !args[1].IsNull() {
		if !validateInt(vm, args[1], "Iterator") {
			return false
		}
		if args[1].AsNum() < 0 {
			args[0] = FalseVal
			return true
		}
		index = toUint32(args[1].AsNum())
		if index >= m.Capacity() {
			args[0] = FalseVal
			return true
		}
		index++
	}

	for ; index < m.Capacity(); index++ {
		if !m.Entries[index].Key.IsUndefined() {
			args[0] = NumVal(float64(index))
			return true
		}
	}

	args[0] = FalseVal
	return true
}

func primMapRemove(vm *VM, args []Value) bool {
	if !validateKey(vm, args[1]) {
		return false
	}
	args[0] = args[0].AsMap().MapRemoveKey(args[1])
	return true
}

func primMapKeyIteratorValue(vm *VM, args []Value) bool {
	m := args[0].AsMap()
	index := validateIndex(vm, args[1], int(m.Capacity()), "Iterator")
	if index == -1 {
		return false
	}
	entry := &m.Entries[index]
	if entry.Key.IsUndefined() {
		vm.fiber.err = vm.NewString("Invalid map iterator.")
		return false
	}
	args[0] = entry.Key
	return true
}

func primMapValueIteratorValue(vm *VM, args []Value) bool {
	m := args[0].AsMap()
	index := validateIndex(vm, args[1], int(m.Capacity()), "Iterator")
	if index == -1 {
		return false
	}
	entry := &m.Entries[index]
	if entry.Key.IsUndefined() {
		vm.fiber.err = vm.NewString("Invalid map iterator.")
		return false
	}
	args[0] = entry.Value
	return true
}

// Null ------------------------------------------------------------------------

func primNullNot(vm *VM, args []Value) bool {
	args[0] = TrueVal
	return true
}

func primNullToString(vm *VM, args []Value) bool {
	args[0] = vm.NewString("null")
	return true
}

// Num -------------------------------------------------------------------------

func primNumFromString(vm *VM, args []Value) bool {
	if !validateString(vm, args[1], "Argument") {
		return false
	}
	s := args[1].AsString()

	// An empty string cannot be parsed.
	if len(s.Value) == 0 {
		args[0] = NullVal
		return true
	}

	number, consumed, rangeErr := cStrtod(s.Value)

	// Skip past any trailing whitespace.
	for consumed < len(s.Value) && isCSpace(s.Value[consumed]) {
		consumed++
	}

	if rangeErr {
		vm.fiber.err = vm.NewString("Number literal is too large.")
		return false
	}

	// The whole string must have been consumed; otherwise it holds characters
	// that are not part of a number.
	if consumed < len(s.Value) {
		args[0] = NullVal
		return true
	}

	args[0] = NumVal(number)
	return true
}

func isCSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

// cStrtod converts the longest numeric prefix of s the way C's strtod does,
// including leading whitespace, a sign, decimal or hexadecimal digits, an
// exponent, and the words "inf", "infinity" and "nan". It returns the value,
// how many bytes it consumed, and whether the result was out of range.
func cStrtod(s string) (float64, int, bool) {
	i := 0
	for i < len(s) && isCSpace(s[i]) {
		i++
	}
	start := i
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}

	lower := strings.ToLower(s[i:])
	if strings.HasPrefix(lower, "infinity") {
		return signed(math.Inf(1), neg), i + 8, false
	}
	if strings.HasPrefix(lower, "inf") {
		return signed(math.Inf(1), neg), i + 3, false
	}
	if strings.HasPrefix(lower, "nan") {
		return signed(WrenDoubleNaN, neg), i + 3, false
	}

	// Hexadecimal floating point.
	if i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') {
		j := i + 2
		digits := 0
		for j < len(s) && isHexDigit(s[j]) {
			j++
			digits++
		}
		if j < len(s) && s[j] == '.' {
			j++
			for j < len(s) && isHexDigit(s[j]) {
				j++
				digits++
			}
		}
		if digits > 0 {
			end := j
			if j < len(s) && (s[j] == 'p' || s[j] == 'P') {
				k := j + 1
				if k < len(s) && (s[k] == '+' || s[k] == '-') {
					k++
				}
				expDigits := 0
				for k < len(s) && isDigit(s[k]) {
					k++
					expDigits++
				}
				if expDigits > 0 {
					end = k
				}
			}
			text := s[start:end]
			if !strings.ContainsAny(text, "pP") {
				text += "p0"
			}
			value, err := strconv.ParseFloat(text, 64)
			return value, end, isRange(err)
		}
	}

	digits := 0
	for i < len(s) && isDigit(s[i]) {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
			digits++
		}
	}
	if digits == 0 {
		return 0, 0, false
	}
	end := i
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		expDigits := 0
		for j < len(s) && isDigit(s[j]) {
			j++
			expDigits++
		}
		if expDigits > 0 {
			end = j
		}
	}

	text := s[start:end]
	value, err := strconv.ParseFloat(text, 64)
	if isRange(err) {
		return value, end, true
	}
	// glibc also reports a range error when a non-zero value is rounded into
	// the subnormal range or all the way to zero.
	if value != 0 && math.Abs(value) < 2.2250738585072014e-308 {
		return value, end, true
	}
	if value == 0 && hasNonZeroMantissa(text) {
		return value, end, true
	}
	return value, end, false
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isRange(err error) bool {
	if ne, ok := err.(*strconv.NumError); ok {
		return ne.Err == strconv.ErrRange
	}
	return false
}

func signed(v float64, neg bool) float64 {
	if neg {
		return -v
	}
	return v
}

func numConstant(value float64) Primitive {
	return func(vm *VM, args []Value) bool {
		args[0] = NumVal(value)
		return true
	}
}

func numInfix(op func(a, b float64) float64) Primitive {
	return func(vm *VM, args []Value) bool {
		if !validateNum(vm, args[1], "Right operand") {
			return false
		}
		args[0] = NumVal(op(args[0].AsNum(), args[1].AsNum()))
		return true
	}
}

func numCompare(op func(a, b float64) bool) Primitive {
	return func(vm *VM, args []Value) bool {
		if !validateNum(vm, args[1], "Right operand") {
			return false
		}
		args[0] = BoolVal(op(args[0].AsNum(), args[1].AsNum()))
		return true
	}
}

// numBitwise defines an operator that works on 32-bit unsigned integers, as
// every bitwise operator in Wren does.
func numBitwise(op func(a, b uint32) uint32) Primitive {
	return func(vm *VM, args []Value) bool {
		if !validateNum(vm, args[1], "Right operand") {
			return false
		}
		left := toUint32(args[0].AsNum())
		right := toUint32(args[1].AsNum())
		args[0] = NumVal(float64(op(left, right)))
		return true
	}
}

func numFn(fn func(float64) float64) Primitive {
	return func(vm *VM, args []Value) bool {
		args[0] = NumVal(fn(args[0].AsNum()))
		return true
	}
}

func primNumMod(vm *VM, args []Value) bool {
	if !validateNum(vm, args[1], "Right operand") {
		return false
	}
	args[0] = NumVal(math.Mod(args[0].AsNum(), args[1].AsNum()))
	return true
}

func primNumEqEq(vm *VM, args []Value) bool {
	if !args[1].IsNum() {
		args[0] = FalseVal
		return true
	}
	args[0] = BoolVal(args[0].AsNum() == args[1].AsNum())
	return true
}

func primNumBangEq(vm *VM, args []Value) bool {
	if !args[1].IsNum() {
		args[0] = TrueVal
		return true
	}
	args[0] = BoolVal(args[0].AsNum() != args[1].AsNum())
	return true
}

func primNumBitwiseNot(vm *VM, args []Value) bool {
	// Bitwise operators always work on 32-bit unsigned ints.
	args[0] = NumVal(float64(^toUint32(args[0].AsNum())))
	return true
}

func primNumDotDot(vm *VM, args []Value) bool {
	if !validateNum(vm, args[1], "Right hand side of range") {
		return false
	}
	args[0] = vm.NewRange(args[0].AsNum(), args[1].AsNum(), true)
	return true
}

func primNumDotDotDot(vm *VM, args []Value) bool {
	if !validateNum(vm, args[1], "Right hand side of range") {
		return false
	}
	args[0] = vm.NewRange(args[0].AsNum(), args[1].AsNum(), false)
	return true
}

func primNumAtan2(vm *VM, args []Value) bool {
	if !validateNum(vm, args[1], "x value") {
		return false
	}
	args[0] = NumVal(math.Atan2(args[0].AsNum(), args[1].AsNum()))
	return true
}

func primNumMin(vm *VM, args []Value) bool {
	if !validateNum(vm, args[1], "Other value") {
		return false
	}
	value := args[0].AsNum()
	other := args[1].AsNum()
	if value <= other {
		args[0] = NumVal(value)
	} else {
		args[0] = NumVal(other)
	}
	return true
}

func primNumMax(vm *VM, args []Value) bool {
	if !validateNum(vm, args[1], "Other value") {
		return false
	}
	value := args[0].AsNum()
	other := args[1].AsNum()
	if value > other {
		args[0] = NumVal(value)
	} else {
		args[0] = NumVal(other)
	}
	return true
}

func primNumClamp(vm *VM, args []Value) bool {
	if !validateNum(vm, args[1], "Min value") {
		return false
	}
	if !validateNum(vm, args[2], "Max value") {
		return false
	}
	value := args[0].AsNum()
	minValue := args[1].AsNum()
	maxValue := args[2].AsNum()
	result := value
	if value < minValue {
		result = minValue
	} else if value > maxValue {
		result = maxValue
	}
	args[0] = NumVal(result)
	return true
}

func primNumPow(vm *VM, args []Value) bool {
	if !validateNum(vm, args[1], "Power value") {
		return false
	}
	args[0] = NumVal(math.Pow(args[0].AsNum(), args[1].AsNum()))
	return true
}

func primNumFraction(vm *VM, args []Value) bool {
	_, frac := cModf(args[0].AsNum())
	args[0] = NumVal(frac)
	return true
}

// cModf splits a value into its integral and fractional parts the way C's
// modf does. Go's math.Modf differs for an infinity, where C gives a zero of
// the same sign as the fractional part rather than a NaN.
func cModf(value float64) (integer, frac float64) {
	if math.IsInf(value, 0) {
		return value, math.Copysign(0, value)
	}
	return math.Modf(value)
}

func primNumIsInfinity(vm *VM, args []Value) bool {
	args[0] = BoolVal(math.IsInf(args[0].AsNum(), 0))
	return true
}

func primNumIsInteger(vm *VM, args []Value) bool {
	value := args[0].AsNum()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		args[0] = FalseVal
		return true
	}
	args[0] = BoolVal(math.Trunc(value) == value)
	return true
}

func primNumIsNan(vm *VM, args []Value) bool {
	args[0] = BoolVal(math.IsNaN(args[0].AsNum()))
	return true
}

func primNumSign(vm *VM, args []Value) bool {
	value := args[0].AsNum()
	switch {
	case value > 0:
		args[0] = NumVal(1)
	case value < 0:
		args[0] = NumVal(-1)
	default:
		args[0] = NumVal(0)
	}
	return true
}

func primNumToString(vm *VM, args []Value) bool {
	args[0] = vm.NumToString(args[0].AsNum())
	return true
}

func primNumTruncate(vm *VM, args []Value) bool {
	integer, _ := cModf(args[0].AsNum())
	args[0] = NumVal(integer)
	return true
}

// Object ----------------------------------------------------------------------

func primObjectSame(vm *VM, args []Value) bool {
	args[0] = BoolVal(ValuesEqual(args[1], args[2]))
	return true
}

func primObjectNot(vm *VM, args []Value) bool {
	args[0] = FalseVal
	return true
}

func primObjectEqEq(vm *VM, args []Value) bool {
	args[0] = BoolVal(ValuesEqual(args[0], args[1]))
	return true
}

func primObjectBangEq(vm *VM, args []Value) bool {
	args[0] = BoolVal(!ValuesEqual(args[0], args[1]))
	return true
}

func primObjectIs(vm *VM, args []Value) bool {
	if !args[1].IsClass() {
		vm.fiber.err = vm.NewString("Right operand must be a class.")
		return false
	}

	classObj := vm.GetClass(args[0])
	baseClassObj := args[1].AsClass()

	// Walk the superclass chain looking for the class.
	for {
		if baseClassObj == classObj {
			args[0] = TrueVal
			return true
		}
		classObj = classObj.Superclass
		if classObj == nil {
			break
		}
	}

	args[0] = FalseVal
	return true
}

func primObjectToString(vm *VM, args []Value) bool {
	name := ObjVal(args[0].o.base().classObj.Name)
	args[0] = vm.StringFormat("instance of @", name)
	return true
}

func primObjectType(vm *VM, args []Value) bool {
	args[0] = ObjVal(vm.GetClass(args[0]))
	return true
}

// Range -----------------------------------------------------------------------

func primRangeFrom(vm *VM, args []Value) bool {
	args[0] = NumVal(args[0].AsRange().From)
	return true
}

func primRangeTo(vm *VM, args []Value) bool {
	args[0] = NumVal(args[0].AsRange().To)
	return true
}

func primRangeMin(vm *VM, args []Value) bool {
	r := args[0].AsRange()
	args[0] = NumVal(fmin(r.From, r.To))
	return true
}

func primRangeMax(vm *VM, args []Value) bool {
	r := args[0].AsRange()
	args[0] = NumVal(fmax(r.From, r.To))
	return true
}

// fmin and fmax follow C's fmin and fmax: a NaN argument is ignored unless
// both are NaN, and two zeros of different signs give the negative one for
// fmin and the positive one for fmax.
func fmin(a, b float64) float64 {
	if math.IsNaN(a) {
		return b
	}
	if math.IsNaN(b) {
		return a
	}
	if a < b {
		return a
	}
	if b < a {
		return b
	}
	if math.Signbit(a) {
		return a
	}
	return b
}

func fmax(a, b float64) float64 {
	if math.IsNaN(a) {
		return b
	}
	if math.IsNaN(b) {
		return a
	}
	if a > b {
		return a
	}
	if b > a {
		return b
	}
	if math.Signbit(a) {
		return b
	}
	return a
}

func primRangeIsInclusive(vm *VM, args []Value) bool {
	args[0] = BoolVal(args[0].AsRange().IsInclusive)
	return true
}

func primRangeIterate(vm *VM, args []Value) bool {
	r := args[0].AsRange()

	// An empty range stops at once.
	if r.From == r.To && !r.IsInclusive {
		args[0] = FalseVal
		return true
	}

	if args[1].IsNull() {
		args[0] = NumVal(r.From)
		return true
	}

	if !validateNum(vm, args[1], "Iterator") {
		return false
	}

	iterator := args[1].AsNum()

	// Iterate towards `to` from `from`.
	if r.From < r.To {
		iterator++
		if iterator > r.To {
			args[0] = FalseVal
			return true
		}
	} else {
		iterator--
		if iterator < r.To {
			args[0] = FalseVal
			return true
		}
	}

	if !r.IsInclusive && iterator == r.To {
		args[0] = FalseVal
		return true
	}

	args[0] = NumVal(iterator)
	return true
}

func primRangeIteratorValue(vm *VM, args []Value) bool {
	// The iterator is assumed to be a number, which is the range's value.
	args[0] = args[1]
	return true
}

func primRangeToString(vm *VM, args []Value) bool {
	r := args[0].AsRange()
	from := vm.NumToString(r.From)
	to := vm.NumToString(r.To)
	sep := "..."
	if r.IsInclusive {
		sep = ".."
	}
	args[0] = vm.StringFormat("@$@", from, sep, to)
	return true
}

// String ----------------------------------------------------------------------

func primStringFromCodePoint(vm *VM, args []Value) bool {
	if !validateInt(vm, args[1], "Code point") {
		return false
	}
	codePoint := int(int32(toUint32(args[1].AsNum())))
	if codePoint < 0 {
		vm.fiber.err = vm.NewString("Code point cannot be negative.")
		return false
	}
	if codePoint > 0x10ffff {
		vm.fiber.err = vm.NewString("Code point cannot be greater than 0x10ffff.")
		return false
	}
	args[0] = vm.StringFromCodePoint(codePoint)
	return true
}

func primStringFromByte(vm *VM, args []Value) bool {
	if !validateInt(vm, args[1], "Byte") {
		return false
	}
	b := int(int32(toUint32(args[1].AsNum())))
	if b < 0 {
		vm.fiber.err = vm.NewString("Byte cannot be negative.")
		return false
	}
	if b > 0xff {
		vm.fiber.err = vm.NewString("Byte cannot be greater than 0xff.")
		return false
	}
	args[0] = vm.StringFromByte(byte(b))
	return true
}

func primStringByteAt(vm *VM, args []Value) bool {
	s := args[0].AsString()
	index := validateIndex(vm, args[1], len(s.Value), "Index")
	if index == -1 {
		return false
	}
	args[0] = NumVal(float64(s.Value[index]))
	return true
}

func primStringByteCount(vm *VM, args []Value) bool {
	args[0] = NumVal(float64(len(args[0].AsString().Value)))
	return true
}

func primStringCodePointAt(vm *VM, args []Value) bool {
	s := args[0].AsString()
	index := validateIndex(vm, args[1], len(s.Value), "Index")
	if index == -1 {
		return false
	}

	// In the middle of a UTF-8 sequence, say so.
	if s.Value[index]&0xc0 == 0x80 {
		args[0] = NumVal(-1)
		return true
	}

	args[0] = NumVal(float64(Utf8Decode(s.Value[index:])))
	return true
}

func primStringContains(vm *VM, args []Value) bool {
	if !validateString(vm, args[1], "Argument") {
		return false
	}
	args[0] = BoolVal(StringFind(args[0].AsString(), args[1].AsString(), 0) != -1)
	return true
}

func primStringEndsWith(vm *VM, args []Value) bool {
	if !validateString(vm, args[1], "Argument") {
		return false
	}
	s := args[0].AsString()
	search := args[1].AsString()

	if len(search.Value) > len(s.Value) {
		args[0] = FalseVal
		return true
	}
	args[0] = BoolVal(strings.HasSuffix(s.Value, search.Value))
	return true
}

func primStringIndexOf1(vm *VM, args []Value) bool {
	if !validateString(vm, args[1], "Argument") {
		return false
	}
	index := StringFind(args[0].AsString(), args[1].AsString(), 0)
	args[0] = NumVal(float64(index))
	return true
}

func primStringIndexOf2(vm *VM, args []Value) bool {
	if !validateString(vm, args[1], "Argument") {
		return false
	}
	s := args[0].AsString()
	search := args[1].AsString()
	start := validateIndex(vm, args[2], len(s.Value), "Start")
	if start == -1 {
		return false
	}
	index := StringFind(s, search, start)
	args[0] = NumVal(float64(index))
	return true
}

func primStringIterate(vm *VM, args []Value) bool {
	s := args[0].AsString()

	if args[1].IsNull() {
		if len(s.Value) == 0 {
			args[0] = FalseVal
			return true
		}
		args[0] = NumVal(0)
		return true
	}

	if !validateInt(vm, args[1], "Iterator") {
		return false
	}
	if args[1].AsNum() < 0 {
		args[0] = FalseVal
		return true
	}
	index := int(toUint32(args[1].AsNum()))

	// Advance to the beginning of the next UTF-8 sequence.
	for {
		index++
		if index >= len(s.Value) {
			args[0] = FalseVal
			return true
		}
		if s.Value[index]&0xc0 != 0x80 {
			break
		}
	}

	args[0] = NumVal(float64(index))
	return true
}

func primStringIterateByte(vm *VM, args []Value) bool {
	s := args[0].AsString()

	if args[1].IsNull() {
		if len(s.Value) == 0 {
			args[0] = FalseVal
			return true
		}
		args[0] = NumVal(0)
		return true
	}

	if !validateInt(vm, args[1], "Iterator") {
		return false
	}
	if args[1].AsNum() < 0 {
		args[0] = FalseVal
		return true
	}
	index := int(toUint32(args[1].AsNum()))

	index++
	if index >= len(s.Value) {
		args[0] = FalseVal
		return true
	}

	args[0] = NumVal(float64(index))
	return true
}

func primStringIteratorValue(vm *VM, args []Value) bool {
	s := args[0].AsString()
	index := validateIndex(vm, args[1], len(s.Value), "Iterator")
	if index == -1 {
		return false
	}
	args[0] = vm.StringCodePointAt(s, index)
	return true
}

func primStringStartsWith(vm *VM, args []Value) bool {
	if !validateString(vm, args[1], "Argument") {
		return false
	}
	s := args[0].AsString()
	search := args[1].AsString()
	if len(search.Value) > len(s.Value) {
		args[0] = FalseVal
		return true
	}
	args[0] = BoolVal(strings.HasPrefix(s.Value, search.Value))
	return true
}

func primStringPlus(vm *VM, args []Value) bool {
	if !validateString(vm, args[1], "Right operand") {
		return false
	}
	args[0] = vm.StringFormat("@@", args[0], args[1])
	return true
}

func primStringSubscript(vm *VM, args []Value) bool {
	s := args[0].AsString()

	if args[1].IsNum() {
		index := validateIndex(vm, args[1], len(s.Value), "Subscript")
		if index == -1 {
			return false
		}
		args[0] = vm.StringCodePointAt(s, index)
		return true
	}

	if !args[1].IsRange() {
		vm.fiber.err = vm.NewString("Subscript must be a number or a range.")
		return false
	}

	start, count, step := calculateRange(vm, args[1].AsRange(), len(s.Value))
	if start == -1 {
		return false
	}

	args[0] = vm.NewStringFromRange(s, start, count, step)
	return true
}

func primStringToString(vm *VM, args []Value) bool {
	return true
}

// System ----------------------------------------------------------------------

func primSystemClock(vm *VM, args []Value) bool {
	args[0] = NumVal(processClock())
	return true
}

func primSystemGC(vm *VM, args []Value) bool {
	vm.CollectGarbage()
	args[0] = NullVal
	return true
}

func primSystemWriteString(vm *VM, args []Value) bool {
	vm.Write(args[1].AsCString())
	args[0] = args[1]
	return true
}

// Core setup ------------------------------------------------------------------

func (vm *VM) primitive(classObj *ObjClass, name string, fn Primitive) {
	symbol := vm.symbolTableEnsure(&vm.methodNames, name)
	vm.BindMethod(classObj, symbol, Method{typ: MethodPrimitive, primitive: fn})
}

func (vm *VM) functionCall(classObj *ObjClass, name string, fn Primitive) {
	symbol := vm.symbolTableEnsure(&vm.methodNames, name)
	vm.BindMethod(classObj, symbol, Method{typ: MethodFunctionCall, primitive: fn})
}

// defineClass creates either the Object or the Class class in the core module.
func (vm *VM) defineClass(module *ObjModule, name string) *ObjClass {
	nameString := vm.NewString(name).AsString()
	classObj := vm.NewSingleClass(0, nameString)
	vm.DefineVariable(module, name, ObjVal(classObj), nil)
	return classObj
}

var fnCallSignatures = []string{
	"call()",
	"call(_)",
	"call(_,_)",
	"call(_,_,_)",
	"call(_,_,_,_)",
	"call(_,_,_,_,_)",
	"call(_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_,_,_,_,_,_,_,_)",
	"call(_,_,_,_,_,_,_,_,_,_,_,_,_,_,_,_)",
}

// initializeCore builds the core module: the three bootstrap classes, the
// core library written in Wren, and the primitives of every built-in class.
func (vm *VM) initializeCore() {
	coreModule := vm.NewModule(nil)

	// The core module's key in the module map is null.
	vm.modules.MapSet(NullVal, ObjVal(coreModule))

	// Object has no superclass, so it has to be defined specially.
	vm.objectClass = vm.defineClass(coreModule, "Object")
	vm.primitive(vm.objectClass, "!", primObjectNot)
	vm.primitive(vm.objectClass, "==(_)", primObjectEqEq)
	vm.primitive(vm.objectClass, "!=(_)", primObjectBangEq)
	vm.primitive(vm.objectClass, "is(_)", primObjectIs)
	vm.primitive(vm.objectClass, "toString", primObjectToString)
	vm.primitive(vm.objectClass, "type", primObjectType)

	// Now Class, a subclass of Object.
	vm.classClass = vm.defineClass(coreModule, "Class")
	vm.BindSuperclass(vm.classClass, vm.objectClass)
	vm.primitive(vm.classClass, "name", primClassName)
	vm.primitive(vm.classClass, "supertype", primClassSupertype)
	vm.primitive(vm.classClass, "toString", primClassToString)
	vm.primitive(vm.classClass, "attributes", primClassAttributes)

	// Finally Object's metaclass, a subclass of Class.
	objectMetaclass := vm.defineClass(coreModule, "Object metaclass")

	// Wire up the metaclass relationships now all three classes exist.
	vm.objectClass.classObj = objectMetaclass
	objectMetaclass.classObj = vm.classClass
	vm.classClass.classObj = vm.classClass

	vm.BindSuperclass(objectMetaclass, vm.classClass)

	vm.primitive(objectMetaclass, "same(_,_)", primObjectSame)

	// The rest of the classes can now be defined normally, in Wren.
	vm.interpretCore(coreModuleSource)

	vm.boolClass = vm.FindVariable(coreModule, "Bool").AsClass()
	vm.primitive(vm.boolClass, "toString", primBoolToString)
	vm.primitive(vm.boolClass, "!", primBoolNot)

	vm.fiberClass = vm.FindVariable(coreModule, "Fiber").AsClass()
	vm.primitive(vm.fiberClass.classObj, "new(_)", primFiberNew)
	vm.primitive(vm.fiberClass.classObj, "abort(_)", primFiberAbort)
	vm.primitive(vm.fiberClass.classObj, "current", primFiberCurrent)
	vm.primitive(vm.fiberClass.classObj, "suspend()", primFiberSuspend)
	vm.primitive(vm.fiberClass.classObj, "yield()", primFiberYield)
	vm.primitive(vm.fiberClass.classObj, "yield(_)", primFiberYield1)
	vm.primitive(vm.fiberClass, "call()", primFiberCall)
	vm.primitive(vm.fiberClass, "call(_)", primFiberCall1)
	vm.primitive(vm.fiberClass, "error", primFiberError)
	vm.primitive(vm.fiberClass, "isDone", primFiberIsDone)
	vm.primitive(vm.fiberClass, "transfer()", primFiberTransfer)
	vm.primitive(vm.fiberClass, "transfer(_)", primFiberTransfer1)
	vm.primitive(vm.fiberClass, "transferError(_)", primFiberTransferError)
	vm.primitive(vm.fiberClass, "try()", primFiberTry)
	vm.primitive(vm.fiberClass, "try(_)", primFiberTry1)

	vm.fnClass = vm.FindVariable(coreModule, "Fn").AsClass()
	vm.primitive(vm.fnClass.classObj, "new(_)", primFnNew)
	vm.primitive(vm.fnClass, "arity", primFnArity)
	for i, signature := range fnCallSignatures {
		vm.functionCall(vm.fnClass, signature, makeFnCall(i))
	}
	vm.primitive(vm.fnClass, "toString", primFnToString)

	vm.nullClass = vm.FindVariable(coreModule, "Null").AsClass()
	vm.primitive(vm.nullClass, "!", primNullNot)
	vm.primitive(vm.nullClass, "toString", primNullToString)

	vm.numClass = vm.FindVariable(coreModule, "Num").AsClass()
	vm.primitive(vm.numClass.classObj, "fromString(_)", primNumFromString)
	vm.primitive(vm.numClass.classObj, "infinity", numConstant(math.Inf(1)))
	vm.primitive(vm.numClass.classObj, "nan", numConstant(WrenDoubleNaN))
	vm.primitive(vm.numClass.classObj, "pi", numConstant(3.14159265358979323846264338327950288))
	vm.primitive(vm.numClass.classObj, "tau", numConstant(6.28318530717958647692528676655900577))
	vm.primitive(vm.numClass.classObj, "largest", numConstant(math.MaxFloat64))
	vm.primitive(vm.numClass.classObj, "smallest", numConstant(2.2250738585072014e-308))
	vm.primitive(vm.numClass.classObj, "maxSafeInteger", numConstant(9007199254740991.0))
	vm.primitive(vm.numClass.classObj, "minSafeInteger", numConstant(-9007199254740991.0))
	vm.primitive(vm.numClass, "-(_)", numInfix(func(a, b float64) float64 { return a - b }))
	vm.primitive(vm.numClass, "+(_)", numInfix(func(a, b float64) float64 { return a + b }))
	vm.primitive(vm.numClass, "*(_)", numInfix(func(a, b float64) float64 { return a * b }))
	vm.primitive(vm.numClass, "/(_)", numInfix(func(a, b float64) float64 { return a / b }))
	vm.primitive(vm.numClass, "<(_)", numCompare(func(a, b float64) bool { return a < b }))
	vm.primitive(vm.numClass, ">(_)", numCompare(func(a, b float64) bool { return a > b }))
	vm.primitive(vm.numClass, "<=(_)", numCompare(func(a, b float64) bool { return a <= b }))
	vm.primitive(vm.numClass, ">=(_)", numCompare(func(a, b float64) bool { return a >= b }))
	vm.primitive(vm.numClass, "&(_)", numBitwise(func(a, b uint32) uint32 { return a & b }))
	vm.primitive(vm.numClass, "|(_)", numBitwise(func(a, b uint32) uint32 { return a | b }))
	vm.primitive(vm.numClass, "^(_)", numBitwise(func(a, b uint32) uint32 { return a ^ b }))
	// A shift count is taken modulo 32, as the shift instructions of the
	// platforms the original targets do.
	vm.primitive(vm.numClass, "<<(_)", numBitwise(func(a, b uint32) uint32 { return a << (b & 31) }))
	vm.primitive(vm.numClass, ">>(_)", numBitwise(func(a, b uint32) uint32 { return a >> (b & 31) }))
	vm.primitive(vm.numClass, "abs", numFn(math.Abs))
	vm.primitive(vm.numClass, "acos", numFn(math.Acos))
	vm.primitive(vm.numClass, "asin", numFn(math.Asin))
	vm.primitive(vm.numClass, "atan", numFn(math.Atan))
	vm.primitive(vm.numClass, "cbrt", numFn(math.Cbrt))
	vm.primitive(vm.numClass, "ceil", numFn(math.Ceil))
	vm.primitive(vm.numClass, "cos", numFn(math.Cos))
	vm.primitive(vm.numClass, "floor", numFn(math.Floor))
	vm.primitive(vm.numClass, "-", numFn(func(v float64) float64 { return -v }))
	vm.primitive(vm.numClass, "round", numFn(cRound))
	vm.primitive(vm.numClass, "min(_)", primNumMin)
	vm.primitive(vm.numClass, "max(_)", primNumMax)
	vm.primitive(vm.numClass, "clamp(_,_)", primNumClamp)
	vm.primitive(vm.numClass, "sin", numFn(math.Sin))
	vm.primitive(vm.numClass, "sqrt", numFn(math.Sqrt))
	vm.primitive(vm.numClass, "tan", numFn(math.Tan))
	vm.primitive(vm.numClass, "log", numFn(math.Log))
	vm.primitive(vm.numClass, "log2", numFn(math.Log2))
	vm.primitive(vm.numClass, "exp", numFn(math.Exp))
	vm.primitive(vm.numClass, "%(_)", primNumMod)
	vm.primitive(vm.numClass, "~", primNumBitwiseNot)
	vm.primitive(vm.numClass, "..(_)", primNumDotDot)
	vm.primitive(vm.numClass, "...(_)", primNumDotDotDot)
	vm.primitive(vm.numClass, "atan(_)", primNumAtan2)
	vm.primitive(vm.numClass, "pow(_)", primNumPow)
	vm.primitive(vm.numClass, "fraction", primNumFraction)
	vm.primitive(vm.numClass, "isInfinity", primNumIsInfinity)
	vm.primitive(vm.numClass, "isInteger", primNumIsInteger)
	vm.primitive(vm.numClass, "isNan", primNumIsNan)
	vm.primitive(vm.numClass, "sign", primNumSign)
	vm.primitive(vm.numClass, "toString", primNumToString)
	vm.primitive(vm.numClass, "truncate", primNumTruncate)

	// These are defined only so that 0 and -0 compare equal, which IEEE 754
	// requires even though their bit patterns differ.
	vm.primitive(vm.numClass, "==(_)", primNumEqEq)
	vm.primitive(vm.numClass, "!=(_)", primNumBangEq)

	vm.stringClass = vm.FindVariable(coreModule, "String").AsClass()
	vm.primitive(vm.stringClass.classObj, "fromCodePoint(_)", primStringFromCodePoint)
	vm.primitive(vm.stringClass.classObj, "fromByte(_)", primStringFromByte)
	vm.primitive(vm.stringClass, "+(_)", primStringPlus)
	vm.primitive(vm.stringClass, "[_]", primStringSubscript)
	vm.primitive(vm.stringClass, "byteAt_(_)", primStringByteAt)
	vm.primitive(vm.stringClass, "byteCount_", primStringByteCount)
	vm.primitive(vm.stringClass, "codePointAt_(_)", primStringCodePointAt)
	vm.primitive(vm.stringClass, "contains(_)", primStringContains)
	vm.primitive(vm.stringClass, "endsWith(_)", primStringEndsWith)
	vm.primitive(vm.stringClass, "indexOf(_)", primStringIndexOf1)
	vm.primitive(vm.stringClass, "indexOf(_,_)", primStringIndexOf2)
	vm.primitive(vm.stringClass, "iterate(_)", primStringIterate)
	vm.primitive(vm.stringClass, "iterateByte_(_)", primStringIterateByte)
	vm.primitive(vm.stringClass, "iteratorValue(_)", primStringIteratorValue)
	vm.primitive(vm.stringClass, "startsWith(_)", primStringStartsWith)
	vm.primitive(vm.stringClass, "toString", primStringToString)

	vm.listClass = vm.FindVariable(coreModule, "List").AsClass()
	vm.primitive(vm.listClass.classObj, "filled(_,_)", primListFilled)
	vm.primitive(vm.listClass.classObj, "new()", primListNew)
	vm.primitive(vm.listClass, "[_]", primListSubscript)
	vm.primitive(vm.listClass, "[_]=(_)", primListSubscriptSetter)
	vm.primitive(vm.listClass, "add(_)", primListAdd)
	vm.primitive(vm.listClass, "addCore_(_)", primListAddCore)
	vm.primitive(vm.listClass, "clear()", primListClear)
	vm.primitive(vm.listClass, "count", primListCount)
	vm.primitive(vm.listClass, "insert(_,_)", primListInsert)
	vm.primitive(vm.listClass, "iterate(_)", primListIterate)
	vm.primitive(vm.listClass, "iteratorValue(_)", primListIteratorValue)
	vm.primitive(vm.listClass, "removeAt(_)", primListRemoveAt)
	vm.primitive(vm.listClass, "remove(_)", primListRemoveValue)
	vm.primitive(vm.listClass, "indexOf(_)", primListIndexOf)
	vm.primitive(vm.listClass, "swap(_,_)", primListSwap)

	vm.mapClass = vm.FindVariable(coreModule, "Map").AsClass()
	vm.primitive(vm.mapClass.classObj, "new()", primMapNew)
	vm.primitive(vm.mapClass, "[_]", primMapSubscript)
	vm.primitive(vm.mapClass, "[_]=(_)", primMapSubscriptSetter)
	vm.primitive(vm.mapClass, "addCore_(_,_)", primMapAddCore)
	vm.primitive(vm.mapClass, "clear()", primMapClear)
	vm.primitive(vm.mapClass, "containsKey(_)", primMapContainsKey)
	vm.primitive(vm.mapClass, "count", primMapCount)
	vm.primitive(vm.mapClass, "remove(_)", primMapRemove)
	vm.primitive(vm.mapClass, "iterate(_)", primMapIterate)
	vm.primitive(vm.mapClass, "keyIteratorValue_(_)", primMapKeyIteratorValue)
	vm.primitive(vm.mapClass, "valueIteratorValue_(_)", primMapValueIteratorValue)

	vm.rangeClass = vm.FindVariable(coreModule, "Range").AsClass()
	vm.primitive(vm.rangeClass, "from", primRangeFrom)
	vm.primitive(vm.rangeClass, "to", primRangeTo)
	vm.primitive(vm.rangeClass, "min", primRangeMin)
	vm.primitive(vm.rangeClass, "max", primRangeMax)
	vm.primitive(vm.rangeClass, "isInclusive", primRangeIsInclusive)
	vm.primitive(vm.rangeClass, "iterate(_)", primRangeIterate)
	vm.primitive(vm.rangeClass, "iteratorValue(_)", primRangeIteratorValue)
	vm.primitive(vm.rangeClass, "toString", primRangeToString)

	systemClass := vm.FindVariable(coreModule, "System").AsClass()
	vm.primitive(systemClass.classObj, "clock", primSystemClock)
	vm.primitive(systemClass.classObj, "gc()", primSystemGC)
	vm.primitive(systemClass.classObj, "writeString_(_)", primSystemWriteString)
}

// cRound rounds half way away from zero, as C's round does; Go's math.Round
// does the same, and this wrapper keeps the intent visible.
func cRound(v float64) float64 { return math.Round(v) }
