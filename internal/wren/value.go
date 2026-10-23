// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren VM's src/vm/wren_value.c and src/vm/wren_value.h.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import (
	"math"
	"strings"
)

// MinCapacity is the initial (and minimum) capacity of a non-empty list or
// map object, and GrowFactor the rate at which a collection's capacity grows
// once the size exceeds the current capacity. MapLoadPercent is the maximum
// percentage of map entries that may be filled before the map is grown. The
// three together fix the physical layout of every map, which is what decides
// the order its entries are iterated in.
const (
	MinCapacity    = 16
	GrowFactor     = 2
	MapLoadPercent = 75
)

// ObjType identifies which specific type a heap-allocated object is.
type ObjType int

const (
	ObjTClass ObjType = iota
	ObjTClosure
	ObjTFiber
	ObjTFn
	ObjTForeign
	ObjTInstance
	ObjTList
	ObjTMap
	ObjTModule
	ObjTRange
	ObjTString
	ObjTUpvalue
)

// objBase is the header every heap object carries: its type and its class.
// Unlike the C implementation there is no mark bit or allocation list, since
// Go's own collector owns these objects.
type objBase struct {
	typ      ObjType
	classObj *ObjClass
}

// Obj is any heap-allocated Wren object.
type Obj interface {
	base() *objBase
}

func (o *objBase) base() *objBase { return o }

// Value is a Wren value. Numbers, booleans, null and the internal "undefined"
// singleton are stored in [bits] exactly as the C implementation's NaN-tagged
// 64-bit words, because map hashing and identity comparison are defined on
// those raw bits. Heap values keep the object itself in [o].
type Value struct {
	bits uint64
	o    Obj
}

// The NaN-tagging bit patterns of the C implementation. They are reproduced
// here because hashValue hashes the raw word of an unboxed value, so the
// iteration order of a map with null, boolean or numeric keys depends on them.
const (
	signBit      uint64 = 1 << 63
	qNaN         uint64 = 0x7ffc000000000000
	maskTag      uint64 = 7
	tagNaN       uint64 = 0
	tagNull      uint64 = 1
	tagFalse     uint64 = 2
	tagTrue      uint64 = 3
	tagUndefined uint64 = 4

	doubleQNaNPosMinBits uint64 = 0x7FF8000000000000
)

var (
	// NullVal, FalseVal, TrueVal and UndefinedVal are the singleton values.
	NullVal      = Value{bits: qNaN | tagNull}
	FalseVal     = Value{bits: qNaN | tagFalse}
	TrueVal      = Value{bits: qNaN | tagTrue}
	UndefinedVal = Value{bits: qNaN | tagUndefined}
)

// WrenDoubleNaN is the canonical quiet NaN the VM hands out for `Num.nan`.
var WrenDoubleNaN = math.Float64frombits(doubleQNaNPosMinBits)

// NumVal boxes a double.
func NumVal(d float64) Value { return Value{bits: math.Float64bits(d)} }

// BoolVal boxes a boolean.
func BoolVal(b bool) Value {
	if b {
		return TrueVal
	}
	return FalseVal
}

// ObjVal boxes a heap object.
func ObjVal(o Obj) Value { return Value{bits: signBit | qNaN, o: o} }

// IsNum reports whether the value is a number.
func (v Value) IsNum() bool { return v.o == nil && v.bits&qNaN != qNaN }

// IsObj reports whether the value is a heap object.
func (v Value) IsObj() bool { return v.o != nil }

// IsBool reports whether the value is a boolean.
func (v Value) IsBool() bool { return v == TrueVal || v == FalseVal }

// IsFalse, IsNull and IsUndefined test for the individual singletons.
func (v Value) IsFalse() bool     { return v == FalseVal }
func (v Value) IsNull() bool      { return v == NullVal }
func (v Value) IsUndefined() bool { return v == UndefinedVal }

// AsNum interprets the value as a double.
func (v Value) AsNum() float64 { return math.Float64frombits(v.bits) }

// AsBool interprets the value as a boolean.
func (v Value) AsBool() bool { return v == TrueVal }

// IsFalsy reports whether the value is false or null, the two values Wren
// treats as false in a condition.
func (v Value) IsFalsy() bool { return v.IsFalse() || v.IsNull() }

func (v Value) isObjType(t ObjType) bool { return v.o != nil && v.o.base().typ == t }

func (v Value) IsString() bool   { return v.isObjType(ObjTString) }
func (v Value) IsClass() bool    { return v.isObjType(ObjTClass) }
func (v Value) IsClosure() bool  { return v.isObjType(ObjTClosure) }
func (v Value) IsFiber() bool    { return v.isObjType(ObjTFiber) }
func (v Value) IsFn() bool       { return v.isObjType(ObjTFn) }
func (v Value) IsForeign() bool  { return v.isObjType(ObjTForeign) }
func (v Value) IsInstance() bool { return v.isObjType(ObjTInstance) }
func (v Value) IsList() bool     { return v.isObjType(ObjTList) }
func (v Value) IsMap() bool      { return v.isObjType(ObjTMap) }
func (v Value) IsRange() bool    { return v.isObjType(ObjTRange) }

func (v Value) AsString() *ObjString   { return v.o.(*ObjString) }
func (v Value) AsCString() string      { return v.o.(*ObjString).Value }
func (v Value) AsClass() *ObjClass     { return v.o.(*ObjClass) }
func (v Value) AsClosure() *ObjClosure { return v.o.(*ObjClosure) }
func (v Value) AsFiber() *ObjFiber     { return v.o.(*ObjFiber) }
func (v Value) AsFn() *ObjFn           { return v.o.(*ObjFn) }
func (v Value) AsForeign() *ObjForeign { return v.o.(*ObjForeign) }
func (v Value) AsInstance() *ObjInstance {
	return v.o.(*ObjInstance)
}
func (v Value) AsList() *ObjList { return v.o.(*ObjList) }
func (v Value) AsMap() *ObjMap   { return v.o.(*ObjMap) }
func (v Value) AsModule() *ObjModule {
	return v.o.(*ObjModule)
}
func (v Value) AsRange() *ObjRange { return v.o.(*ObjRange) }

// ObjString is a heap-allocated string object. Wren strings are byte strings:
// the length and every index are counted in bytes, not code points.
type ObjString struct {
	objBase
	Value string
	hash  uint32
}

// ObjUpvalue is the storage for a variable a closure has captured. While the
// variable is still live on its fiber's stack the upvalue refers to that slot;
// once the slot is popped the value is copied into the upvalue itself.
type ObjUpvalue struct {
	objBase
	fiber  *ObjFiber
	slot   int
	closed Value
	isOpen bool
	next   *ObjUpvalue
}

func (u *ObjUpvalue) get() Value {
	if u.isOpen {
		return u.fiber.stack[u.slot]
	}
	return u.closed
}

func (u *ObjUpvalue) set(v Value) {
	if u.isOpen {
		u.fiber.stack[u.slot] = v
		return
	}
	u.closed = v
}

// Primitive is a method implemented in Go inside the VM. It is given the
// arguments in [args] (a window onto the fiber's stack, with the receiver in
// args[0]). If it produces a value it stores it in args[0] and returns true.
// If it raises a runtime error or changes the running fiber it returns false.
type Primitive func(vm *VM, args []Value) bool

// ForeignMethodFn is a method implemented outside the VM, which reaches the
// stack through the slot API.
type ForeignMethodFn func(vm *VM)

// FnDebug holds the debugging information a function needs for stack traces.
type FnDebug struct {
	Name string
	// SourceLines holds one source line number per byte of bytecode.
	SourceLines []int
}

// ObjModule is a loaded module and the top-level variables it defines. It is
// never visible as a first-class Wren object.
type ObjModule struct {
	objBase
	Variables     []Value
	VariableNames SymbolTable
	Name          *ObjString
}

// ObjFn wraps the bytecode and debug information of a callable chunk of code.
// Functions are always referenced through an ObjClosure.
type ObjFn struct {
	objBase
	Code        []byte
	Constants   []Value
	Module      *ObjModule
	MaxSlots    int
	NumUpvalues int
	Arity       int
	Debug       *FnDebug
}

// ObjClosure is an instance of a function together with the environment it
// has closed over.
type ObjClosure struct {
	objBase
	Fn       *ObjFn
	Upvalues []*ObjUpvalue
}

// CallFrame is one entry of a fiber's call stack.
type CallFrame struct {
	// ip is the offset in the closure's bytecode of the next instruction.
	ip      int
	closure *ObjClosure
	// stackStart is the index of the first stack slot used by this call.
	stackStart int
}

// FiberState tracks how a fiber was invoked, where that cannot be worked out
// from the fiber's other fields.
type FiberState int

const (
	// FiberTry means the fiber is being run from another fiber via try().
	FiberTry FiberState = iota
	// FiberRoot means the fiber was invoked directly by the interpreter.
	FiberRoot
	// FiberOther covers every other way of invoking a fiber.
	FiberOther
)

// ObjFiber is a coroutine: its own value stack, call frames and open upvalues.
type ObjFiber struct {
	objBase
	stack        []Value
	stackTop     int
	frames       []CallFrame
	openUpvalues *ObjUpvalue
	caller       *ObjFiber
	err          Value
	state        FiberState
}

func (f *ObjFiber) hasError() bool { return !f.err.IsNull() }

// MethodType says how a method is implemented.
type MethodType int

const (
	// MethodPrimitive is implemented in Go inside the VM and may manipulate
	// the fiber's stack directly.
	MethodPrimitive MethodType = iota
	// MethodFunctionCall is the primitive that handles .call on Fn.
	MethodFunctionCall
	// MethodForeign is defined outside the VM.
	MethodForeign
	// MethodBlock is an ordinary user-defined method.
	MethodBlock
	// MethodNone means there is no method for the symbol.
	MethodNone
)

// Method is one entry of a class's method table.
type Method struct {
	typ       MethodType
	primitive Primitive
	foreign   ForeignMethodFn
	closure   *ObjClosure
}

// ObjClass is a Wren class: its superclass, field count, method table keyed by
// method symbol, name, and the attributes recorded for it at compile time.
type ObjClass struct {
	objBase
	Superclass *ObjClass
	// NumFields is -1 for a foreign class.
	NumFields  int
	Methods    []Method
	Name       *ObjString
	Attributes Value
}

// ObjForeign is an instance of a foreign class; its payload is owned by the
// code that implements the class.
type ObjForeign struct {
	objBase
	Data any
}

// ObjInstance is an instance of a Wren class.
type ObjInstance struct {
	objBase
	Fields []Value
}

// ObjList is a Wren list.
type ObjList struct {
	objBase
	Elements []Value
	// capacity mirrors the C buffer's capacity. It is tracked because
	// List.removeAt shrinks the backing array once it is half empty, and the
	// capacity decides nothing observable on its own but keeps the growth
	// behaviour of the original.
	capacity int
}

// MapEntry is one slot of a map's entry array. A key of UndefinedVal means the
// slot is unused: its value is then false for an empty slot and true for a
// tombstone left behind by a removal.
type MapEntry struct {
	Key   Value
	Value Value
}

// ObjMap is a hash table using open addressing with linear probing, exactly as
// the C implementation does, so that entries are iterated in the same order.
type ObjMap struct {
	objBase
	Entries []MapEntry
	Count   uint32
}

// Capacity returns the number of allocated entries.
func (m *ObjMap) Capacity() uint32 { return uint32(len(m.Entries)) }

// ObjRange is a range of numbers, which may count up or down and may or may
// not include its end point.
type ObjRange struct {
	objBase
	From        float64
	To          float64
	IsInclusive bool
}

// NewSingleClass creates a "raw" class with no metaclass or superclass at all.
// It is only used while bootstrapping Object and Class.
func (vm *VM) NewSingleClass(numFields int, name *ObjString) *ObjClass {
	c := &ObjClass{}
	c.typ = ObjTClass
	c.NumFields = numFields
	c.Name = name
	c.Attributes = NullVal
	return c
}

// BindSuperclass makes superclass the superclass of subclass and copies its
// methods down. It must be called before any method is defined on subclass.
func (vm *VM) BindSuperclass(subclass, superclass *ObjClass) {
	subclass.Superclass = superclass

	// Include the superclass in the total number of fields.
	if subclass.NumFields != -1 {
		subclass.NumFields += superclass.NumFields
	}

	for i := range superclass.Methods {
		vm.BindMethod(subclass, i, superclass.Methods[i])
	}
}

// NewClass creates a class object together with its metaclass.
func (vm *VM) NewClass(superclass *ObjClass, numFields int, name *ObjString) *ObjClass {
	metaclassName := vm.StringFormat("@ metaclass", ObjVal(name))

	metaclass := vm.NewSingleClass(0, metaclassName.AsString())
	metaclass.classObj = vm.classClass

	// Metaclasses always inherit Class and do not parallel the non-metaclass
	// hierarchy.
	vm.BindSuperclass(metaclass, vm.classClass)

	classObj := vm.NewSingleClass(numFields, name)
	classObj.classObj = metaclass
	vm.BindSuperclass(classObj, superclass)

	return classObj
}

// BindMethod installs method under symbol in the class's method table,
// growing the table with MethodNone entries as needed.
func (vm *VM) BindMethod(classObj *ObjClass, symbol int, method Method) {
	for len(classObj.Methods) <= symbol {
		classObj.Methods = append(classObj.Methods, Method{typ: MethodNone})
	}
	classObj.Methods[symbol] = method
}

// NewClosure creates a closure invoking fn, with room for its upvalues.
func (vm *VM) NewClosure(fn *ObjFn) *ObjClosure {
	c := &ObjClosure{Fn: fn, Upvalues: make([]*ObjUpvalue, fn.NumUpvalues)}
	c.typ = ObjTClosure
	c.classObj = vm.fnClass
	return c
}

// initialCallFrames mirrors the C implementation's INITIAL_CALL_FRAMES.
const initialCallFrames = 4

// NewFiber creates a fiber that will invoke closure. A nil closure makes an
// empty fiber for the slot API.
func (vm *VM) NewFiber(closure *ObjClosure) *ObjFiber {
	stackCapacity := 1
	if closure != nil {
		stackCapacity = PowerOf2Ceil(closure.Fn.MaxSlots + 1)
	}
	f := &ObjFiber{
		stack:  make([]Value, stackCapacity),
		frames: make([]CallFrame, 0, initialCallFrames),
		err:    NullVal,
		state:  FiberOther,
	}
	f.typ = ObjTFiber
	f.classObj = vm.fiberClass

	if closure != nil {
		f.appendCallFrame(closure, 0)
		// The first slot always holds the closure.
		f.stack[0] = ObjVal(closure)
		f.stackTop = 1
	}
	return f
}

func (f *ObjFiber) appendCallFrame(closure *ObjClosure, stackStart int) {
	f.frames = append(f.frames, CallFrame{ip: 0, closure: closure, stackStart: stackStart})
}

// ensureStack makes sure the fiber's stack has at least needed slots.
func (f *ObjFiber) ensureStack(needed int) {
	if len(f.stack) >= needed {
		return
	}
	capacity := PowerOf2Ceil(needed)
	grown := make([]Value, capacity)
	copy(grown, f.stack)
	f.stack = grown
}

// NewForeign creates an instance of a foreign class.
func (vm *VM) NewForeign(classObj *ObjClass) *ObjForeign {
	o := &ObjForeign{}
	o.typ = ObjTForeign
	o.classObj = classObj
	return o
}

// NewFunction creates an empty function; its code and constants are filled in
// by the compiler.
func (vm *VM) NewFunction(module *ObjModule, maxSlots int) *ObjFn {
	fn := &ObjFn{Module: module, MaxSlots: maxSlots, Debug: &FnDebug{}}
	fn.typ = ObjTFn
	fn.classObj = vm.fnClass
	return fn
}

// NewInstance creates an instance of classObj with every field null.
func (vm *VM) NewInstance(classObj *ObjClass) Value {
	inst := &ObjInstance{Fields: make([]Value, classObj.NumFields)}
	inst.typ = ObjTInstance
	inst.classObj = classObj
	for i := range inst.Fields {
		inst.Fields[i] = NullVal
	}
	return ObjVal(inst)
}

// NewList creates a list of numElements elements, left uninitialised.
func (vm *VM) NewList(numElements int) *ObjList {
	l := &ObjList{}
	l.typ = ObjTList
	l.classObj = vm.listClass
	if numElements > 0 {
		l.Elements = make([]Value, numElements)
		l.capacity = numElements
	}
	return l
}

// listWrite appends a value, growing the backing array the way the C
// implementation's buffers do (to the next power of two).
func (l *ObjList) listWrite(v Value) {
	if l.capacity < len(l.Elements)+1 {
		l.capacity = PowerOf2Ceil(len(l.Elements) + 1)
	}
	l.Elements = append(l.Elements, v)
}

// ListInsert inserts value at index, shifting the later elements down.
func (l *ObjList) ListInsert(value Value, index int) {
	l.listWrite(NullVal)
	for i := len(l.Elements) - 1; i > index; i-- {
		l.Elements[i] = l.Elements[i-1]
	}
	l.Elements[index] = value
}

// ListIndexOf returns the index of the first element equal to value, or -1.
func (l *ObjList) ListIndexOf(value Value) int {
	for i, item := range l.Elements {
		if ValuesEqual(item, value) {
			return i
		}
	}
	return -1
}

// ListRemoveAt removes and returns the element at index.
func (l *ObjList) ListRemoveAt(index int) Value {
	removed := l.Elements[index]
	for i := index; i < len(l.Elements)-1; i++ {
		l.Elements[i] = l.Elements[i+1]
	}
	if l.capacity/GrowFactor >= len(l.Elements) {
		l.capacity /= GrowFactor
	}
	l.Elements = l.Elements[:len(l.Elements)-1]
	return removed
}

// NewMap creates an empty map.
func (vm *VM) NewMap() *ObjMap {
	m := &ObjMap{}
	m.typ = ObjTMap
	m.classObj = vm.mapClass
	return m
}

// hashBits mixes a 64-bit word down to 30 bits. From v8's ComputeLongHash,
// which in turn cites Thomas Wang's integer hash functions.
func hashBits(hash uint64) uint32 {
	hash = ^hash + (hash << 18)
	hash = hash ^ (hash >> 31)
	hash = hash * 21
	hash = hash ^ (hash >> 11)
	hash = hash + (hash << 6)
	hash = hash ^ (hash >> 22)
	return uint32(hash & 0x3fffffff)
}

func hashNumber(num float64) uint32 { return hashBits(math.Float64bits(num)) }

func hashObject(object Obj) uint32 {
	switch o := object.(type) {
	case *ObjClass:
		// Classes just use their name.
		return hashObject(o.Name)
	case *ObjFn:
		// Bare functions are allowed so the compiler can use a map to find
		// existing constants in a function's constant table.
		return hashNumber(float64(o.Arity)) ^ hashNumber(float64(len(o.Code)))
	case *ObjRange:
		return hashNumber(o.From) ^ hashNumber(o.To)
	case *ObjString:
		return o.hash
	}
	return 0
}

// hashValue hashes one of the built-in immutable types. Unboxed values are
// hashed from their raw NaN-tagged word, so 0 and -0 hash differently.
func hashValue(value Value) uint32 {
	if value.IsObj() {
		return hashObject(value.o)
	}
	return hashBits(value.bits)
}

// findEntry looks for key in entries. If it is there it returns its index and
// true; otherwise it returns the index where the pair should be inserted and
// false.
func findEntry(entries []MapEntry, key Value) (int, bool) {
	capacity := uint32(len(entries))
	if capacity == 0 {
		return -1, false
	}

	startIndex := hashValue(key) % capacity
	index := startIndex

	// If we pass a tombstone and don't end up finding the key, its entry will
	// be re-used for the insert.
	tombstone := -1

	for {
		entry := &entries[index]
		if entry.Key.IsUndefined() {
			if entry.Value.IsFalse() {
				// An empty slot ends the probe sequence.
				if tombstone != -1 {
					return tombstone, false
				}
				return int(index), false
			}
			// A tombstone: keep looking in case the key is after it.
			if tombstone == -1 {
				tombstone = int(index)
			}
		} else if ValuesEqual(entry.Key, key) {
			return int(index), true
		}

		index = (index + 1) % capacity
		if index == startIndex {
			break
		}
	}

	// The table is full of tombstones; re-use the first one.
	return tombstone, false
}

func insertEntry(entries []MapEntry, key, value Value) bool {
	index, found := findEntry(entries, key)
	if found {
		entries[index].Value = value
		return false
	}
	entries[index].Key = key
	entries[index].Value = value
	return true
}

func (m *ObjMap) resize(capacity uint32) {
	entries := make([]MapEntry, capacity)
	for i := range entries {
		entries[i].Key = UndefinedVal
		entries[i].Value = FalseVal
	}
	for i := range m.Entries {
		entry := &m.Entries[i]
		if entry.Key.IsUndefined() {
			continue
		}
		insertEntry(entries, entry.Key, entry.Value)
	}
	m.Entries = entries
}

// MapGet looks up key. If it is absent it returns UndefinedVal.
func (m *ObjMap) MapGet(key Value) Value {
	index, found := findEntry(m.Entries, key)
	if found {
		return m.Entries[index].Value
	}
	return UndefinedVal
}

// MapSet associates key with value.
func (m *ObjMap) MapSet(key, value Value) {
	if m.Count+1 > m.Capacity()*MapLoadPercent/100 {
		capacity := m.Capacity() * GrowFactor
		if capacity < MinCapacity {
			capacity = MinCapacity
		}
		m.resize(capacity)
	}
	if insertEntry(m.Entries, key, value) {
		m.Count++
	}
}

// MapClear empties the map and releases its entry array.
func (m *ObjMap) MapClear() {
	m.Entries = nil
	m.Count = 0
}

// MapRemoveKey removes key if present and returns the value it had, or null.
// The slot becomes a tombstone so later probe sequences still work.
func (m *ObjMap) MapRemoveKey(key Value) Value {
	index, found := findEntry(m.Entries, key)
	if !found {
		return NullVal
	}
	value := m.Entries[index].Value
	m.Entries[index].Key = UndefinedVal
	m.Entries[index].Value = TrueVal

	m.Count--
	if m.Count == 0 {
		m.MapClear()
	} else if m.Capacity() > MinCapacity &&
		m.Count < m.Capacity()/GrowFactor*MapLoadPercent/100 {
		capacity := m.Capacity() / GrowFactor
		if capacity < MinCapacity {
			capacity = MinCapacity
		}
		m.resize(capacity)
	}
	return value
}

// NewModule creates a module with the given name.
func (vm *VM) NewModule(name *ObjString) *ObjModule {
	m := &ObjModule{Name: name}
	m.typ = ObjTModule
	return m
}

// NewRange creates a range from `from` to `to`.
func (vm *VM) NewRange(from, to float64, isInclusive bool) Value {
	r := &ObjRange{From: from, To: to, IsInclusive: isInclusive}
	r.typ = ObjTRange
	r.classObj = vm.rangeClass
	return ObjVal(r)
}

// hashString computes the FNV-1a hash of a string's bytes, as the original
// does. The original mixes in each byte through C's `char`, whose signedness
// is not fixed by the language; that only changes bits seven and above of the
// result, which no map the VM builds is large enough to probe with.
func hashString(s string) uint32 {
	hash := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		hash ^= uint32(s[i])
		hash *= 16777619
	}
	return hash
}

// NewString creates a string object holding text.
func (vm *VM) NewString(text string) Value {
	s := &ObjString{Value: text, hash: hashString(text)}
	s.typ = ObjTString
	s.classObj = vm.stringClass
	return ObjVal(s)
}

// NewStringFromRange builds a string from count code points of source, taken
// starting at byte offset start and advancing by step bytes each time.
func (vm *VM) NewStringFromRange(source *ObjString, start, count, step int) Value {
	from := source.Value
	var b strings.Builder
	for i := 0; i < count; i++ {
		index := start + i*step
		codePoint := Utf8Decode(from[index:])
		if codePoint != -1 {
			var buf [4]byte
			n := Utf8Encode(codePoint, buf[:])
			b.Write(buf[:n])
		}
	}
	return vm.NewString(b.String())
}

// NumToString renders a number the way the VM does: "nan", "infinity" and
// "-infinity" are spelled out, and every other value uses C's "%.14g".
func (vm *VM) NumToString(value float64) Value {
	if math.IsNaN(value) {
		return vm.NewString("nan")
	}
	if math.IsInf(value, 1) {
		return vm.NewString("infinity")
	}
	if math.IsInf(value, -1) {
		return vm.NewString("-infinity")
	}
	return vm.NewString(FormatG14(value))
}

// StringFormat builds a string from a format holding only two directives: "$"
// takes a Go string and "@" takes a Wren string value. Every other byte is
// copied literally. It is the VM's own internal formatter.
func (vm *VM) StringFormat(format string, args ...any) Value {
	var b strings.Builder
	next := 0
	for i := 0; i < len(format); i++ {
		switch format[i] {
		case '$':
			b.WriteString(args[next].(string))
			next++
		case '@':
			b.WriteString(args[next].(Value).AsString().Value)
			next++
		default:
			b.WriteByte(format[i])
		}
	}
	return vm.NewString(b.String())
}

// StringFromCodePoint creates a string holding the UTF-8 encoding of value.
func (vm *VM) StringFromCodePoint(value int) Value {
	var buf [4]byte
	n := Utf8Encode(value, buf[:])
	return vm.NewString(string(buf[:n]))
}

// StringFromByte creates a one-byte string.
func (vm *VM) StringFromByte(value byte) Value {
	return vm.NewString(string([]byte{value}))
}

// StringCodePointAt returns the code point of string starting at byte index.
// A byte that does not begin a valid sequence is returned on its own.
func (vm *VM) StringCodePointAt(s *ObjString, index int) Value {
	codePoint := Utf8Decode(s.Value[index:])
	if codePoint == -1 {
		return vm.NewString(s.Value[index : index+1])
	}
	return vm.StringFromCodePoint(codePoint)
}

// StringFind returns the byte offset of the first occurrence of needle in
// haystack at or after start, or -1. It uses the Boyer-Moore-Horspool
// algorithm, as the original does, which matters for an empty needle and for
// a start index past the end.
func StringFind(haystack, needle *ObjString, start int) int {
	// An empty needle is always found.
	if len(needle.Value) == 0 {
		return start
	}
	if start+len(needle.Value) > len(haystack.Value) {
		return -1
	}
	if start >= len(haystack.Value) {
		return -1
	}

	// The original sizes this table at UINT8_MAX rather than 256; a needle or
	// haystack byte of 0xff reads past its end there, which is undefined. Use
	// a full table so the search is always well defined here.
	var shift [256]int
	needleEnd := len(needle.Value) - 1
	for i := range shift {
		shift[i] = len(needle.Value)
	}
	for index := 0; index < needleEnd; index++ {
		shift[needle.Value[index]] = needleEnd - index
	}

	lastChar := needle.Value[needleEnd]
	searchRange := len(haystack.Value) - len(needle.Value)

	for index := start; index <= searchRange; {
		// Compare the last byte of the window to the last byte of the needle;
		// if they match, check the whole needle.
		c := haystack.Value[index+needleEnd]
		if lastChar == c && haystack.Value[index:index+needleEnd] == needle.Value[:needleEnd] {
			return index
		}
		index += shift[c]
	}
	return -1
}

// NewUpvalue creates an open upvalue pointing at a slot of a fiber's stack.
func (vm *VM) NewUpvalue(fiber *ObjFiber, slot int) *ObjUpvalue {
	u := &ObjUpvalue{fiber: fiber, slot: slot, closed: NullVal, isOpen: true}
	u.typ = ObjTUpvalue
	return u
}

// ValuesSame reports whether two values are strictly the same: identity for
// objects, and raw-bit equality for unboxed values. That makes 0 and -0
// different and makes a NaN the same as itself.
func ValuesSame(a, b Value) bool {
	if a.o != nil || b.o != nil {
		return a.o == b.o
	}
	return a.bits == b.bits
}

// ValuesEqual reports whether two values are equivalent. The immutable types
// (null, bools, numbers, ranges and strings) compare by data; everything else
// compares by identity.
func ValuesEqual(a, b Value) bool {
	if ValuesSame(a, b) {
		return true
	}
	if !a.IsObj() || !b.IsObj() {
		return false
	}
	if a.o.base().typ != b.o.base().typ {
		return false
	}
	switch ao := a.o.(type) {
	case *ObjRange:
		bo := b.o.(*ObjRange)
		return ao.From == bo.From && ao.To == bo.To && ao.IsInclusive == bo.IsInclusive
	case *ObjString:
		bo := b.o.(*ObjString)
		return ao.hash == bo.hash && ao.Value == bo.Value
	}
	return false
}

// MapIsValidKey reports whether a value may be used as a map key.
func MapIsValidKey(arg Value) bool {
	return arg.IsBool() || arg.IsClass() || arg.IsNull() ||
		arg.IsNum() || arg.IsRange() || arg.IsString()
}

// GetClass returns the class of a value.
func (vm *VM) GetClass(value Value) *ObjClass {
	if value.IsNum() {
		return vm.numClass
	}
	if value.IsObj() {
		classObj := value.o.base().classObj
		if classObj == nil && value.o.base().typ == ObjTString {
			// While the core types are bootstrapped a number of strings are
			// created before the String class exists. The original walks its
			// object list afterwards and fills their class in; here they are
			// resolved on demand instead.
			return vm.stringClass
		}
		return classObj
	}
	switch value.bits & maskTag {
	case tagFalse, tagTrue:
		return vm.boolClass
	case tagNaN:
		return vm.numClass
	case tagNull:
		return vm.nullClass
	}
	return nil
}
