// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the parsing and code generation half of the Wren VM's
// src/vm/wren_compiler.c.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import "strings"

// Local is a local variable in scope while a function is being compiled.
type Local struct {
	name string
	// depth is the scope-chain depth the variable was declared at.
	depth int
	// isUpvalue records that an inner function captures this local.
	isUpvalue bool
}

// CompilerUpvalue records where an upvalue of the function being compiled
// comes from: a local of the enclosing function, or one of its upvalues.
type CompilerUpvalue struct {
	isLocal bool
	index   int
}

// Loop is the bookkeeping for the loop currently being compiled.
type Loop struct {
	// start is the bytecode offset the loop jumps back to.
	start int
	// exitJump is the offset of the argument of the CODE_JUMP_IF that leaves
	// the loop, patched once the end of the loop is known.
	exitJump int
	// body is the offset of the first instruction of the loop's body.
	body int
	// scopeDepth is the depth that a break has to unwind to.
	scopeDepth int
	enclosing  *Loop
}

// SignatureType is one of the method signature syntaxes.
type SignatureType int

const (
	// sigMethod is a name and a parenthesised parameter list; also used for
	// binary operators.
	sigMethod SignatureType = iota
	// sigGetter is just a name; also used for unary operators.
	sigGetter
	// sigSetter is a name followed by "=".
	sigSetter
	// sigSubscript is a square-bracketed parameter list.
	sigSubscript
	// sigSubscriptSetter is a square-bracketed parameter list then "=".
	sigSubscriptSetter
	// sigInitializer is a constructor initializer, which gets a distinct
	// signature so it cannot be called directly.
	sigInitializer
)

// Signature is a method signature while it is being parsed.
type Signature struct {
	name  string
	typ   SignatureType
	arity int
}

// ClassInfo is the bookkeeping for the class definition being compiled.
type ClassInfo struct {
	name *ObjString

	classAttributes  *ObjMap
	methodAttributes *ObjMap

	// fields is the symbol table of the class's own fields.
	fields SymbolTable

	// methods and staticMethods record the symbols already defined, so that
	// a duplicate definition can be reported.
	methods       []int
	staticMethods []int

	isForeign bool
	inStatic  bool

	signature *Signature
}

// Compiler compiles one function, method or chunk of top level code.
type Compiler struct {
	parser *Parser
	parent *Compiler

	locals    [maxLocals]Local
	numLocals int

	upvalues [maxUpvalues]CompilerUpvalue

	// scopeDepth is the current block nesting level; -1 means top level code
	// is being compiled, so declarations are module-level.
	scopeDepth int

	// numSlots tracks the stack slots in use so the function's high-water
	// mark can be recorded. Parameters are not counted: the caller pushed
	// them and already accounts for them.
	numSlots int

	loop *Loop

	enclosingClass *ClassInfo

	fn *ObjFn

	// constants maps a constant's value to its index in fn.Constants, so that
	// duplicates are reused.
	constants *ObjMap

	isInitializer bool

	// numAttributes counts the attributes seen so far. It is tracked
	// separately from attributes because compile-time attributes are not
	// stored, yet using one outside a class or method is still an error.
	numAttributes int
	attributes    *ObjMap
}

// Scope says where a variable is declared.
type Scope int

const (
	scopeLocal Scope = iota
	scopeUpvalue
	scopeModule
)

// Variable is a reference to a variable and the scope that defines it.
type Variable struct {
	index int
	scope Scope
}

// stackEffects is the change each opcode makes to the size of the stack.
var stackEffects = buildStackEffects()

// addConstant adds a constant to the function's constant table, reusing an
// existing entry for the same value, and returns its index.
func (c *Compiler) addConstant(constant Value) int {
	if c.parser.hasError {
		return -1
	}
	if c.constants != nil {
		existing := c.constants.MapGet(constant)
		if existing.IsNum() {
			return int(existing.AsNum())
		}
	}
	if len(c.fn.Constants) < maxConstants {
		c.fn.Constants = append(c.fn.Constants, constant)
		if c.constants == nil {
			c.constants = c.parser.vm.NewMap()
		}
		c.constants.MapSet(constant, NumVal(float64(len(c.fn.Constants)-1)))
	} else {
		c.error("A function may only contain %d unique constants.", maxConstants)
	}
	return len(c.fn.Constants) - 1
}

// initCompiler prepares a compiler for a function, method or script body.
func initCompiler(c *Compiler, parser *Parser, parent *Compiler, isMethod bool) {
	c.parser = parser
	c.parent = parent
	c.loop = nil
	c.enclosingClass = nil
	c.isInitializer = false
	c.fn = nil
	c.constants = nil

	parser.vm.compiler = c

	// Reserve a local slot for the closure or the method receiver so it is
	// not reused for a user variable. For a method it is named "this" so that
	// references to "this" resolve like an ordinary variable; a function has
	// no "this", so the slot is left unnamed and a reference walks up the
	// parent chain to the enclosing method.
	c.numLocals = 1
	c.numSlots = c.numLocals

	if isMethod {
		c.locals[0].name = "this"
	} else {
		c.locals[0].name = ""
	}
	c.locals[0].depth = -1
	c.locals[0].isUpvalue = false

	if parent == nil {
		c.scopeDepth = -1
	} else {
		c.scopeDepth = 0
	}

	c.numAttributes = 0
	c.attributes = parser.vm.NewMap()
	c.fn = parser.vm.NewFunction(parser.module, c.numLocals)
}

// Parsing ---------------------------------------------------------------------

func (c *Compiler) peek() TokenType     { return c.parser.current.typ }
func (c *Compiler) peekNext() TokenType { return c.parser.next.typ }

func (c *Compiler) match(expected TokenType) bool {
	if c.peek() != expected {
		return false
	}
	c.parser.nextToken()
	return true
}

// consume consumes the current token, reporting errorMessage if it is not the
// expected one. A matching next token is taken as a sign that the current one
// was spurious, and is discarded to limit cascaded errors.
func (c *Compiler) consume(expected TokenType, errorMessage string) {
	c.parser.nextToken()
	if c.parser.previous.typ != expected {
		c.error("%s", errorMessage)
		if c.parser.current.typ == expected {
			c.parser.nextToken()
		}
	}
}

func (c *Compiler) matchLine() bool {
	if !c.match(tokenLine) {
		return false
	}
	for c.match(tokenLine) {
	}
	return true
}

func (c *Compiler) ignoreNewlines() { c.matchLine() }

func (c *Compiler) consumeLine(errorMessage string) {
	c.consume(tokenLine, errorMessage)
	c.ignoreNewlines()
}

func (c *Compiler) allowLineBeforeDot() {
	if c.peek() == tokenLine && c.peekNext() == tokenDot {
		c.parser.nextToken()
	}
}

// Variables and scopes --------------------------------------------------------

// emitByte writes one byte of bytecode and records the source line it came
// from, and returns its offset.
func (c *Compiler) emitByte(b int) int {
	c.fn.Code = append(c.fn.Code, byte(b))
	c.fn.Debug.SourceLines = append(c.fn.Debug.SourceLines, c.parser.previous.line)
	return len(c.fn.Code) - 1
}

// emitOp writes one instruction and tracks the stack's high-water mark.
func (c *Compiler) emitOp(instruction Code) {
	c.emitByte(int(instruction))
	c.numSlots += stackEffects[instruction]
	if c.numSlots > c.fn.MaxSlots {
		c.fn.MaxSlots = c.numSlots
	}
}

// emitShort writes one 16-bit argument, big endian.
func (c *Compiler) emitShort(arg int) {
	c.emitByte((arg >> 8) & 0xff)
	c.emitByte(arg & 0xff)
}

func (c *Compiler) emitByteArg(instruction Code, arg int) int {
	c.emitOp(instruction)
	return c.emitByte(arg)
}

func (c *Compiler) emitShortArg(instruction Code, arg int) {
	c.emitOp(instruction)
	c.emitShort(arg)
}

// emitJump writes an instruction followed by a placeholder jump offset and
// returns the offset of the placeholder.
func (c *Compiler) emitJump(instruction Code) int {
	c.emitOp(instruction)
	c.emitByte(0xff)
	return c.emitByte(0xff) - 1
}

func (c *Compiler) emitConstant(value Value) {
	constant := c.addConstant(value)
	c.emitShortArg(CodeConstant, constant)
}

// addLocal declares a local with the given name in the current scope.
func (c *Compiler) addLocal(name string) int {
	local := &c.locals[c.numLocals]
	local.name = name
	local.depth = c.scopeDepth
	local.isUpvalue = false
	c.numLocals++
	return c.numLocals - 1
}

// declareVariable declares a variable named by token (or by the previously
// consumed token) in the current scope and returns its symbol.
func (c *Compiler) declareVariable(token *Token) int {
	if token == nil {
		t := c.parser.previous
		token = &t
	}

	if token.length > maxVariableName {
		c.error("Variable name cannot be longer than %d characters.", maxVariableName)
	}

	name := c.parser.tokenText(*token)

	// Top-level module scope.
	if c.scopeDepth == -1 {
		line := -1
		symbol := c.parser.vm.DefineVariable(c.parser.module, name, NullVal, &line)
		switch symbol {
		case -1:
			c.error("Module variable is already defined.")
		case -2:
			c.error("Too many module variables defined.")
		case -3:
			c.error("Variable '%s' referenced before this definition (first use at line %d).",
				name, line)
		}
		return symbol
	}

	// A variable with this name already declared in this scope is an error;
	// one in an outer scope is simply shadowed.
	for i := c.numLocals - 1; i >= 0; i-- {
		local := &c.locals[i]
		if local.depth < c.scopeDepth {
			break
		}
		if local.name == name {
			c.error("Variable is already declared in this scope.")
			return i
		}
	}

	if c.numLocals == maxLocals {
		c.error("Cannot declare more than %d variables in one scope.", maxLocals)
		return -1
	}
	return c.addLocal(name)
}

func (c *Compiler) declareNamedVariable() int {
	c.consume(tokenName, "Expect variable name.")
	return c.declareVariable(nil)
}

// defineVariable stores a just-initialised variable. A local is already in the
// right stack slot; a module-level variable has to be moved into its slot.
func (c *Compiler) defineVariable(symbol int) {
	if c.scopeDepth >= 0 {
		return
	}
	c.emitShortArg(CodeStoreModuleVar, symbol)
	c.emitOp(CodePop)
}

func (c *Compiler) pushScope() { c.scopeDepth++ }

// discardLocals emits the code to discard the locals at depth or deeper
// without actually undeclaring them, and returns how many it discarded. A
// break needs this before jumping out of a loop whose locals are still in
// scope after the break.
func (c *Compiler) discardLocals(depth int) int {
	local := c.numLocals - 1
	for local >= 0 && c.locals[local].depth >= depth {
		// Use emitByte, not emitOp: the stack effect must not be tracked
		// because the variables are still in scope past the break.
		if c.locals[local].isUpvalue {
			c.emitByte(int(CodeCloseUpvalue))
		} else {
			c.emitByte(int(CodePop))
		}
		local--
	}
	return c.numLocals - local - 1
}

func (c *Compiler) popScope() {
	popped := c.discardLocals(c.scopeDepth)
	c.numLocals -= popped
	c.numSlots -= popped
	c.scopeDepth--
}

// resolveLocal returns the slot of the innermost local named name, or -1.
func (c *Compiler) resolveLocal(name string) int {
	for i := c.numLocals - 1; i >= 0; i-- {
		if c.locals[i].name == name {
			return i
		}
	}
	return -1
}

func (c *Compiler) addUpvalue(isLocal bool, index int) int {
	for i := 0; i < c.fn.NumUpvalues; i++ {
		upvalue := &c.upvalues[i]
		if upvalue.index == index && upvalue.isLocal == isLocal {
			return i
		}
	}
	// Upvalue operands are one byte wide. The source runtime cannot normally
	// exceed this because locals are subject to the same limit, but keep a
	// malformed or deeply flattened closure from indexing past the fixed table.
	if c.fn.NumUpvalues >= maxUpvalues {
		c.error("Cannot capture more than %d variables in one function.", maxUpvalues)
		return 0
	}
	c.upvalues[c.fn.NumUpvalues].isLocal = isLocal
	c.upvalues[c.fn.NumUpvalues].index = index
	c.fn.NumUpvalues++
	return c.fn.NumUpvalues - 1
}

// findUpvalue looks name up in the enclosing functions, adding upvalues to
// every function in between so the value is walked down to this one. A method
// boundary stops the search, since methods do not close over locals.
func (c *Compiler) findUpvalue(name string) int {
	if c.parent == nil {
		return -1
	}
	// A method boundary stops the search unless the name is a static field.
	if (len(name) == 0 || name[0] != '_') && c.parent.enclosingClass != nil {
		return -1
	}

	local := c.parent.resolveLocal(name)
	if local != -1 {
		c.parent.locals[local].isUpvalue = true
		return c.addUpvalue(true, local)
	}

	upvalue := c.parent.findUpvalue(name)
	if upvalue != -1 {
		return c.addUpvalue(false, upvalue)
	}
	return -1
}

// resolveNonmodule looks a name up in the local scopes and the enclosing
// functions' upvalues, but not at module scope.
func (c *Compiler) resolveNonmodule(name string) Variable {
	var variable Variable
	variable.scope = scopeLocal
	variable.index = c.resolveLocal(name)
	if variable.index != -1 {
		return variable
	}
	variable.scope = scopeUpvalue
	variable.index = c.findUpvalue(name)
	return variable
}

// resolveName looks a name up in local, upvalue and then module scope.
func (c *Compiler) resolveName(name string) Variable {
	variable := c.resolveNonmodule(name)
	if variable.index != -1 {
		return variable
	}
	variable.scope = scopeModule
	variable.index = c.parser.module.VariableNames.Find(name)
	return variable
}

func (c *Compiler) loadLocal(slot int) {
	if slot <= 8 {
		c.emitOp(Code(int(CodeLoadLocal0) + slot))
		return
	}
	c.emitByteArg(CodeLoadLocal, slot)
}

// endCompiler finishes a function and, if there is an enclosing compiler,
// emits the code there to load the resulting function as a closure.
func (c *Compiler) endCompiler(debugName string) *ObjFn {
	// After an error the function is not finished: it is unusable anyway.
	if c.parser.hasError {
		c.parser.vm.compiler = c.parent
		return nil
	}

	// Mark the end of the bytecode. The function may hold several early
	// returns, so CODE_RETURN cannot be relied on to mark the end.
	c.emitOp(CodeEnd)

	c.fn.Debug.Name = debugName

	if c.parent != nil {
		constant := c.parent.addConstant(ObjVal(c.fn))

		// Wrap the function in a closure even when it has no upvalues, so the
		// VM can assume every callable is a closure.
		c.parent.emitShortArg(CodeClosure, constant)

		for i := 0; i < c.fn.NumUpvalues; i++ {
			isLocal := 0
			if c.upvalues[i].isLocal {
				isLocal = 1
			}
			c.parent.emitByte(isLocal)
			c.parent.emitByte(c.upvalues[i].index)
		}
	}

	c.parser.vm.compiler = c.parent
	return c.fn
}

// Grammar ---------------------------------------------------------------------

// Precedence orders the infix expressions of the grammar.
type Precedence int

const (
	precNone Precedence = iota
	precLowest
	precAssignment   // =
	precConditional  // ?:
	precLogicalOr    // ||
	precLogicalAnd   // &&
	precEquality     // == !=
	precIs           // is
	precComparison   // < > <= >=
	precBitwiseOr    // |
	precBitwiseXor   // ^
	precBitwiseAnd   // &
	precBitwiseShift // << >>
	precRange        // .. ...
	precTerm         // + -
	precFactor       // * / %
	precUnary        // unary - ! ~
	precCall         // . () []
	precPrimary
)

// GrammarFn compiles a prefix or infix expression.
type GrammarFn func(c *Compiler, canAssign bool)

// SignatureFn parses the rest of a method signature of one syntactic shape.
type SignatureFn func(c *Compiler, signature *Signature)

// GrammarRule is one row of the Pratt parser's table.
type GrammarRule struct {
	prefix     GrammarFn
	infix      GrammarFn
	method     SignatureFn
	precedence Precedence
	name       string
}

// patchJump replaces the placeholder argument of an earlier jump with the
// offset to the current end of the bytecode.
func (c *Compiler) patchJump(offset int) {
	// -2 to adjust for the two bytes of the jump offset itself.
	jump := len(c.fn.Code) - offset - 2
	if jump > maxJump {
		c.error("Too much code to jump over.")
	}
	c.fn.Code[offset] = byte((jump >> 8) & 0xff)
	c.fn.Code[offset+1] = byte(jump & 0xff)
}

// finishBlock parses a block body after the "{". It returns true if the block
// was an expression body, that is if it left a value on the stack.
func (c *Compiler) finishBlock() bool {
	if c.match(tokenRightBrace) {
		return false
	}

	// With no line after the "{" it is a single-expression body.
	if !c.matchLine() {
		c.expression()
		c.consume(tokenRightBrace, "Expect '}' at end of block.")
		return true
	}

	if c.match(tokenRightBrace) {
		return false
	}

	for {
		c.definition()
		c.consumeLine("Expect newline after statement.")
		if c.peek() == tokenRightBrace || c.peek() == tokenEOF {
			break
		}
	}

	c.consume(tokenRightBrace, "Expect '}' at end of block.")
	return false
}

// finishBody parses a method or function body after the "{", adding the code
// that makes a constructor initializer return "this".
func (c *Compiler) finishBody() {
	isExpressionBody := c.finishBlock()

	if c.isInitializer {
		if isExpressionBody {
			c.emitOp(CodePop)
		}
		// The receiver is always in the first local slot.
		c.emitOp(CodeLoadLocal0)
	} else if !isExpressionBody {
		c.emitOp(CodeNull)
	}

	c.emitOp(CodeReturn)
}

// validateNumParameters reports too many parameters exactly once, at one past
// the maximum, so that parsing can carry on.
func (c *Compiler) validateNumParameters(numArgs int) {
	if numArgs == maxParameters+1 {
		c.error("Methods cannot have more than %d parameters.", maxParameters)
	}
}

func (c *Compiler) finishParameterList(signature *Signature) {
	for {
		c.ignoreNewlines()
		signature.arity++
		c.validateNumParameters(signature.arity)
		c.declareNamedVariable()
		if !c.match(tokenComma) {
			break
		}
	}
}

func (c *Compiler) methodSymbol(name string) int {
	return c.parser.vm.symbolTableEnsure(&c.parser.vm.methodNames, name)
}

// signatureParameterList appends numParams "_" placeholders between the given
// brackets.
func signatureParameterList(b *strings.Builder, numParams int, leftBracket, rightBracket byte) {
	b.WriteByte(leftBracket)
	// This may be called with too many parameters; a compile error has
	// already been reported, so just stop at the maximum.
	for i := 0; i < numParams && i < maxParameters; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('_')
	}
	b.WriteByte(rightBracket)
}

// signatureToString renders a signature the way it is keyed in the method
// table and shown in stack traces.
func signatureToString(signature *Signature) string {
	var b strings.Builder
	if signature.typ == sigInitializer {
		b.WriteString("init ")
		b.WriteString(signature.name)
		signatureParameterList(&b, signature.arity, '(', ')')
		return b.String()
	}

	b.WriteString(signature.name)
	switch signature.typ {
	case sigMethod:
		signatureParameterList(&b, signature.arity, '(', ')')
	case sigGetter:
		// The signature is just the name.
	case sigSetter:
		b.WriteByte('=')
		signatureParameterList(&b, 1, '(', ')')
	case sigSubscript:
		signatureParameterList(&b, signature.arity, '[', ']')
	case sigSubscriptSetter:
		signatureParameterList(&b, signature.arity-1, '[', ']')
		b.WriteByte('=')
		signatureParameterList(&b, 1, '(', ')')
	}
	return b.String()
}

func (c *Compiler) signatureSymbol(signature *Signature) int {
	return c.methodSymbol(signatureToString(signature))
}

// signatureFromToken builds a signature of the given type from the most
// recently consumed token.
func (c *Compiler) signatureFromToken(typ SignatureType) Signature {
	token := c.parser.previous
	name := c.parser.tokenText(token)

	if len(name) > maxMethodName {
		c.error("Method names cannot be longer than %d characters.", maxMethodName)
		name = name[:maxMethodName]
	}
	return Signature{name: name, typ: typ, arity: 0}
}

func (c *Compiler) finishArgumentList(signature *Signature) {
	for {
		c.ignoreNewlines()
		signature.arity++
		c.validateNumParameters(signature.arity)
		c.expression()
		if !c.match(tokenComma) {
			break
		}
	}
	// Allow a newline before the closing delimiter.
	c.ignoreNewlines()
}

// callSignature compiles a method call with the given signature.
func (c *Compiler) callSignature(instruction Code, signature *Signature) {
	symbol := c.signatureSymbol(signature)
	c.emitShortArg(Code(int(instruction)+signature.arity), symbol)

	if instruction == CodeSuper0 {
		// A super call has to be statically bound to the class's superclass so
		// the right method is called even when the method is inherited. A
		// constant slot is reserved here and filled in with the superclass
		// when the method is bound.
		c.emitShort(c.addConstant(NullVal))
	}
}

func (c *Compiler) callMethod(numArgs int, name string) {
	symbol := c.methodSymbol(name)
	c.emitShortArg(Code(int(CodeCall0)+numArgs), symbol)
}

// methodCall compiles an optional argument list and block argument and then
// the call itself.
func (c *Compiler) methodCall(instruction Code, signature *Signature) {
	// Build a new signature with the arity and type the arguments imply.
	called := Signature{name: signature.name, typ: sigGetter, arity: 0}

	if c.match(tokenLeftParen) {
		called.typ = sigMethod
		c.ignoreNewlines()
		if c.peek() != tokenRightParen {
			c.finishArgumentList(&called)
		}
		c.consume(tokenRightParen, "Expect ')' after arguments.")
	}

	if c.match(tokenLeftBrace) {
		called.typ = sigMethod
		called.arity++

		var fnCompiler Compiler
		initCompiler(&fnCompiler, c.parser, c, false)

		fnSignature := Signature{name: "", typ: sigMethod, arity: 0}
		if fnCompiler.match(tokenPipe) {
			fnCompiler.finishParameterList(&fnSignature)
			fnCompiler.consume(tokenPipe, "Expect '|' after function parameters.")
		}
		fnCompiler.fn.Arity = fnSignature.arity

		fnCompiler.finishBody()

		// Name the function after the method it is passed to.
		blockName := signatureToString(&called) + " block argument"
		fnCompiler.endCompiler(blockName)
	}

	// A super() call for an initializer needs a real argument list.
	if signature.typ == sigInitializer {
		if called.typ != sigMethod {
			c.error("A superclass constructor must have an argument list.")
		}
		called.typ = sigInitializer
	}

	c.callSignature(instruction, &called)
}

// namedCall compiles a call whose name is the previously consumed token: a
// getter, a method call with arguments, or a setter.
func (c *Compiler) namedCall(canAssign bool, instruction Code) {
	signature := c.signatureFromToken(sigGetter)

	if canAssign && c.match(tokenEq) {
		c.ignoreNewlines()
		signature.typ = sigSetter
		signature.arity = 1
		c.expression()
		c.callSignature(instruction, &signature)
	} else {
		c.methodCall(instruction, &signature)
		c.allowLineBeforeDot()
	}
}

func (c *Compiler) loadVariable(variable Variable) {
	switch variable.scope {
	case scopeLocal:
		c.loadLocal(variable.index)
	case scopeUpvalue:
		c.emitByteArg(CodeLoadUpvalue, variable.index)
	case scopeModule:
		c.emitShortArg(CodeLoadModuleVar, variable.index)
	}
}

// loadThis loads the receiver of the enclosing method, which also works from
// inside a function defined in that method.
func (c *Compiler) loadThis() {
	c.loadVariable(c.resolveNonmodule("this"))
}

// loadCoreVariable pushes a module-level variable implicitly imported from
// the core module.
func (c *Compiler) loadCoreVariable(name string) {
	symbol := c.parser.module.VariableNames.Find(name)
	c.emitShortArg(CodeLoadModuleVar, symbol)
}

func grouping(c *Compiler, canAssign bool) {
	c.expression()
	c.consume(tokenRightParen, "Expect ')' after expression.")
}

// list compiles a list literal, which adds each element with addCore_.
func list(c *Compiler, canAssign bool) {
	c.loadCoreVariable("List")
	c.callMethod(0, "new()")

	for {
		c.ignoreNewlines()
		if c.peek() == tokenRightBracket {
			break
		}
		c.expression()
		c.callMethod(1, "addCore_(_)")
		if !c.match(tokenComma) {
			break
		}
	}

	c.ignoreNewlines()
	c.consume(tokenRightBracket, "Expect ']' after list elements.")
}

// mapLiteral compiles a map literal, which adds each pair with addCore_.
func mapLiteral(c *Compiler, canAssign bool) {
	c.loadCoreVariable("Map")
	c.callMethod(0, "new()")

	for {
		c.ignoreNewlines()
		if c.peek() == tokenRightBrace {
			break
		}
		c.parsePrecedence(precUnary)
		c.consume(tokenColon, "Expect ':' after map key.")
		c.ignoreNewlines()
		c.expression()
		c.callMethod(2, "addCore_(_,_)")
		if !c.match(tokenComma) {
			break
		}
	}

	c.ignoreNewlines()
	c.consume(tokenRightBrace, "Expect '}' after map entries.")
}

func unaryOp(c *Compiler, canAssign bool) {
	rule := getRule(c.parser.previous.typ)
	c.ignoreNewlines()
	c.parsePrecedence(precUnary + 1)
	c.callMethod(0, rule.name)
}

func boolean(c *Compiler, canAssign bool) {
	if c.parser.previous.typ == tokenFalse {
		c.emitOp(CodeFalse)
	} else {
		c.emitOp(CodeTrue)
	}
}

func getEnclosingClassCompiler(c *Compiler) *Compiler {
	for c != nil {
		if c.enclosingClass != nil {
			return c
		}
		c = c.parent
	}
	return nil
}

func getEnclosingClass(c *Compiler) *ClassInfo {
	c = getEnclosingClassCompiler(c)
	if c == nil {
		return nil
	}
	return c.enclosingClass
}

func field(c *Compiler, canAssign bool) {
	// Start from a fake value so parsing can carry on after an error.
	fieldIndex := maxFields

	enclosingClass := getEnclosingClass(c)

	switch {
	case enclosingClass == nil:
		c.error("Cannot reference a field outside of a class definition.")
	case enclosingClass.isForeign:
		c.error("Cannot define fields in a foreign class.")
	case enclosingClass.inStatic:
		c.error("Cannot use an instance field in a static method.")
	default:
		fieldIndex = c.parser.vm.symbolTableEnsure(&enclosingClass.fields,
			c.parser.tokenText(c.parser.previous))
		if fieldIndex >= maxFields {
			c.error("A class can only have %d fields.", maxFields)
		}
	}

	isLoad := true
	if canAssign && c.match(tokenEq) {
		c.expression()
		isLoad = false
	}

	// Directly inside a method a more direct instruction can be used.
	if c.parent != nil && c.parent.enclosingClass == enclosingClass {
		op := CodeStoreFieldThis
		if isLoad {
			op = CodeLoadFieldThis
		}
		c.emitByteArg(op, fieldIndex)
	} else {
		c.loadThis()
		op := CodeStoreField
		if isLoad {
			op = CodeLoadField
		}
		c.emitByteArg(op, fieldIndex)
	}

	c.allowLineBeforeDot()
}

// bareName compiles a read of, or an assignment to, a resolved variable.
func (c *Compiler) bareName(canAssign bool, variable Variable) {
	if canAssign && c.match(tokenEq) {
		c.expression()
		switch variable.scope {
		case scopeLocal:
			c.emitByteArg(CodeStoreLocal, variable.index)
		case scopeUpvalue:
			c.emitByteArg(CodeStoreUpvalue, variable.index)
		case scopeModule:
			c.emitShortArg(CodeStoreModuleVar, variable.index)
		}
		return
	}

	c.loadVariable(variable)
	c.allowLineBeforeDot()
}

func staticField(c *Compiler, canAssign bool) {
	classCompiler := getEnclosingClassCompiler(c)
	if classCompiler == nil {
		c.error("Cannot use a static field outside of a class definition.")
		return
	}

	token := c.parser.previous
	name := c.parser.tokenText(token)

	// The first time a static field is seen it is implicitly defined as a
	// variable in the scope surrounding the class definition.
	if classCompiler.resolveLocal(name) == -1 {
		symbol := classCompiler.declareVariable(&token)
		classCompiler.emitOp(CodeNull)
		classCompiler.defineVariable(symbol)
	}

	// Resolve it properly now: it may already have been captured as an
	// upvalue, which the resolveLocal above would not see.
	variable := c.resolveName(name)
	c.bareName(canAssign, variable)
}

// name compiles a variable name or a method call with an implicit receiver.
func name(c *Compiler, canAssign bool) {
	token := c.parser.previous
	text := c.parser.tokenText(token)

	variable := c.resolveNonmodule(text)
	if variable.index != -1 {
		c.bareName(canAssign, variable)
		return
	}

	// Inside a method a lowercase name is treated as a method on this.
	if IsLocalName(text) && getEnclosingClass(c) != nil {
		c.loadThis()
		c.namedCall(canAssign, CodeCall0)
		return
	}

	variable.scope = scopeModule
	variable.index = c.parser.module.VariableNames.Find(text)
	if variable.index == -1 {
		// Implicitly declare a module-level variable in the hope that a real
		// definition turns up later.
		variable.index = c.parser.vm.DeclareVariable(c.parser.module, text, token.line)
		if variable.index == -2 {
			c.error("Too many module variables defined.")
		}
	}

	c.bareName(canAssign, variable)
}

func nullLiteral(c *Compiler, canAssign bool) { c.emitOp(CodeNull) }

func literal(c *Compiler, canAssign bool) { c.emitConstant(c.parser.previous.value) }

// stringInterpolation compiles an interpolated string, which is sugar for
// building a list of the parts and calling join() on it.
func stringInterpolation(c *Compiler, canAssign bool) {
	c.loadCoreVariable("List")
	c.callMethod(0, "new()")

	for {
		literal(c, false)
		c.callMethod(1, "addCore_(_)")

		c.ignoreNewlines()
		c.expression()
		c.callMethod(1, "addCore_(_)")

		c.ignoreNewlines()
		if !c.match(tokenInterpolation) {
			break
		}
	}

	c.consume(tokenString, "Expect end of string interpolation.")
	literal(c, false)
	c.callMethod(1, "addCore_(_)")

	c.callMethod(0, "join()")
}

func superExpr(c *Compiler, canAssign bool) {
	enclosingClass := getEnclosingClass(c)
	if enclosingClass == nil {
		c.error("Cannot use 'super' outside of a method.")
	}

	c.loadThis()

	if c.match(tokenDot) {
		c.consume(tokenName, "Expect method name after 'super.'.")
		c.namedCall(canAssign, CodeSuper0)
	} else if enclosingClass != nil {
		// With no explicit name, use the name of the enclosing method.
		c.methodCall(CodeSuper0, enclosingClass.signature)
	}
}

func thisExpr(c *Compiler, canAssign bool) {
	if getEnclosingClass(c) == nil {
		c.error("Cannot use 'this' outside of a method.")
		return
	}
	c.loadThis()
}

func subscript(c *Compiler, canAssign bool) {
	signature := Signature{name: "", typ: sigSubscript, arity: 0}

	c.finishArgumentList(&signature)
	c.consume(tokenRightBracket, "Expect ']' after arguments.")

	c.allowLineBeforeDot()

	if canAssign && c.match(tokenEq) {
		signature.typ = sigSubscriptSetter
		signature.arity++
		c.validateNumParameters(signature.arity)
		c.expression()
	}

	c.callSignature(CodeCall0, &signature)
}

func call(c *Compiler, canAssign bool) {
	c.ignoreNewlines()
	c.consume(tokenName, "Expect method name after '.'.")
	c.namedCall(canAssign, CodeCall0)
}

func andExpr(c *Compiler, canAssign bool) {
	c.ignoreNewlines()
	jump := c.emitJump(CodeAnd)
	c.parsePrecedence(precLogicalAnd)
	c.patchJump(jump)
}

func orExpr(c *Compiler, canAssign bool) {
	c.ignoreNewlines()
	jump := c.emitJump(CodeOr)
	c.parsePrecedence(precLogicalOr)
	c.patchJump(jump)
}

func conditional(c *Compiler, canAssign bool) {
	c.ignoreNewlines()

	ifJump := c.emitJump(CodeJumpIf)
	c.parsePrecedence(precConditional)

	c.consume(tokenColon, "Expect ':' after then branch of conditional operator.")
	c.ignoreNewlines()

	elseJump := c.emitJump(CodeJump)
	c.patchJump(ifJump)

	c.parsePrecedence(precAssignment)
	c.patchJump(elseJump)
}

func infixOp(c *Compiler, canAssign bool) {
	rule := getRule(c.parser.previous.typ)

	// An infix operator cannot end an expression.
	c.ignoreNewlines()
	c.parsePrecedence(rule.precedence + 1)

	signature := Signature{name: rule.name, typ: sigMethod, arity: 1}
	c.callSignature(CodeCall0, &signature)
}

func infixSignature(c *Compiler, signature *Signature) {
	signature.typ = sigMethod
	signature.arity = 1
	c.consume(tokenLeftParen, "Expect '(' after operator name.")
	c.declareNamedVariable()
	c.consume(tokenRightParen, "Expect ')' after parameter name.")
}

func unarySignature(c *Compiler, signature *Signature) {
	signature.typ = sigGetter
}

// mixedSignature handles an operator that may be unary or infix, like "-".
func mixedSignature(c *Compiler, signature *Signature) {
	signature.typ = sigGetter
	if c.match(tokenLeftParen) {
		signature.typ = sigMethod
		signature.arity = 1
		c.declareNamedVariable()
		c.consume(tokenRightParen, "Expect ')' after parameter name.")
	}
}

// maybeSetter parses the optional setter part of a signature and reports
// whether it found one.
func (c *Compiler) maybeSetter(signature *Signature) bool {
	if !c.match(tokenEq) {
		return false
	}
	if signature.typ == sigSubscript {
		signature.typ = sigSubscriptSetter
	} else {
		signature.typ = sigSetter
	}
	c.consume(tokenLeftParen, "Expect '(' after '='.")
	c.declareNamedVariable()
	c.consume(tokenRightParen, "Expect ')' after parameter name.")
	signature.arity++
	return true
}

func subscriptSignature(c *Compiler, signature *Signature) {
	signature.typ = sigSubscript
	// The signature's name is currently "[", the token that matched; clear it.
	signature.name = ""
	c.finishParameterList(signature)
	c.consume(tokenRightBracket, "Expect ']' after parameters.")
	c.maybeSetter(signature)
}

func (c *Compiler) parameterList(signature *Signature) {
	if !c.match(tokenLeftParen) {
		return
	}
	signature.typ = sigMethod
	c.ignoreNewlines()
	if c.match(tokenRightParen) {
		return
	}
	c.finishParameterList(signature)
	c.consume(tokenRightParen, "Expect ')' after parameters.")
}

func namedSignature(c *Compiler, signature *Signature) {
	signature.typ = sigGetter
	// A setter cannot also have a parameter list.
	if c.maybeSetter(signature) {
		return
	}
	c.parameterList(signature)
}

func constructorSignature(c *Compiler, signature *Signature) {
	c.consume(tokenName, "Expect constructor name after 'construct'.")
	*signature = c.signatureFromToken(sigInitializer)

	if c.match(tokenEq) {
		c.error("A constructor cannot be a setter.")
	}
	if !c.match(tokenLeftParen) {
		c.error("A constructor cannot be a getter.")
		return
	}
	if c.match(tokenRightParen) {
		return
	}
	c.finishParameterList(signature)
	c.consume(tokenRightParen, "Expect ')' after parameters.")
}

// rules is the Pratt parser's table, indexed by token type.
var rules [numTokenTypes]GrammarRule

func init() {
	unused := GrammarRule{}
	prefix := func(fn GrammarFn) GrammarRule { return GrammarRule{prefix: fn} }
	infix := func(prec Precedence, fn GrammarFn) GrammarRule {
		return GrammarRule{infix: fn, precedence: prec}
	}
	infixOperator := func(prec Precedence, name string) GrammarRule {
		return GrammarRule{infix: infixOp, method: infixSignature, precedence: prec, name: name}
	}
	prefixOperator := func(name string) GrammarRule {
		return GrammarRule{prefix: unaryOp, method: unarySignature, precedence: precNone, name: name}
	}
	operator := func(name string) GrammarRule {
		return GrammarRule{prefix: unaryOp, infix: infixOp, method: mixedSignature,
			precedence: precTerm, name: name}
	}

	rules[tokenLeftParen] = prefix(grouping)
	rules[tokenRightParen] = unused
	rules[tokenLeftBracket] = GrammarRule{prefix: list, infix: subscript,
		method: subscriptSignature, precedence: precCall}
	rules[tokenRightBracket] = unused
	rules[tokenLeftBrace] = prefix(mapLiteral)
	rules[tokenRightBrace] = unused
	rules[tokenColon] = unused
	rules[tokenDot] = infix(precCall, call)
	rules[tokenDotDot] = infixOperator(precRange, "..")
	rules[tokenDotDotDot] = infixOperator(precRange, "...")
	rules[tokenComma] = unused
	rules[tokenStar] = infixOperator(precFactor, "*")
	rules[tokenSlash] = infixOperator(precFactor, "/")
	rules[tokenPercent] = infixOperator(precFactor, "%")
	rules[tokenHash] = unused
	rules[tokenPlus] = infixOperator(precTerm, "+")
	rules[tokenMinus] = operator("-")
	rules[tokenLtLt] = infixOperator(precBitwiseShift, "<<")
	rules[tokenGtGt] = infixOperator(precBitwiseShift, ">>")
	rules[tokenPipe] = infixOperator(precBitwiseOr, "|")
	rules[tokenPipePipe] = infix(precLogicalOr, orExpr)
	rules[tokenCaret] = infixOperator(precBitwiseXor, "^")
	rules[tokenAmp] = infixOperator(precBitwiseAnd, "&")
	rules[tokenAmpAmp] = infix(precLogicalAnd, andExpr)
	rules[tokenBang] = prefixOperator("!")
	rules[tokenTilde] = prefixOperator("~")
	rules[tokenQuestion] = infix(precAssignment, conditional)
	rules[tokenEq] = unused
	rules[tokenLt] = infixOperator(precComparison, "<")
	rules[tokenGt] = infixOperator(precComparison, ">")
	rules[tokenLtEq] = infixOperator(precComparison, "<=")
	rules[tokenGtEq] = infixOperator(precComparison, ">=")
	rules[tokenEqEq] = infixOperator(precEquality, "==")
	rules[tokenBangEq] = infixOperator(precEquality, "!=")
	rules[tokenBreak] = unused
	rules[tokenContinue] = unused
	rules[tokenClass] = unused
	rules[tokenConstruct] = GrammarRule{method: constructorSignature}
	rules[tokenElse] = unused
	rules[tokenFalse] = prefix(boolean)
	rules[tokenFor] = unused
	rules[tokenForeign] = unused
	rules[tokenIf] = unused
	rules[tokenImport] = unused
	rules[tokenAs] = unused
	rules[tokenIn] = unused
	rules[tokenIs] = infixOperator(precIs, "is")
	rules[tokenNull] = prefix(nullLiteral)
	rules[tokenReturn] = unused
	rules[tokenStatic] = unused
	rules[tokenSuper] = prefix(superExpr)
	rules[tokenThis] = prefix(thisExpr)
	rules[tokenTrue] = prefix(boolean)
	rules[tokenVar] = unused
	rules[tokenWhile] = unused
	rules[tokenField] = prefix(field)
	rules[tokenStaticField] = prefix(staticField)
	rules[tokenName] = GrammarRule{prefix: name, method: namedSignature}
	rules[tokenNumber] = prefix(literal)
	rules[tokenString] = prefix(literal)
	rules[tokenInterpolation] = prefix(stringInterpolation)
	rules[tokenLine] = unused
	rules[tokenError] = unused
	rules[tokenEOF] = unused
}

func getRule(typ TokenType) *GrammarRule { return &rules[typ] }

// parsePrecedence is the entry point of the top-down operator precedence
// parser.
func (c *Compiler) parsePrecedence(precedence Precedence) {
	c.parser.nextToken()
	prefix := rules[c.parser.previous.typ].prefix

	if prefix == nil {
		c.error("Expected expression.")
		return
	}

	// Track whether the surrounding expression is loose enough to allow an
	// assignment inside it. An assignment cannot be compiled like an ordinary
	// expression because its left side is an lvalue, so each kind of
	// expression that can be an lvalue parses the "=" itself.
	canAssign := precedence <= precConditional
	prefix(c, canAssign)

	for precedence <= rules[c.parser.current.typ].precedence {
		c.parser.nextToken()
		infix := rules[c.parser.previous.typ].infix
		infix(c, canAssign)
	}
}

// expression parses an expression, which leaves its value on the stack.
func (c *Compiler) expression() { c.parsePrecedence(precLowest) }

// getByteCountForArguments returns how many argument bytes the instruction at
// ip has.
func getByteCountForArguments(bytecode []byte, constants []Value, ip int) int {
	instruction := Code(bytecode[ip])
	switch instruction {
	case CodeNull, CodeFalse, CodeTrue, CodePop, CodeCloseUpvalue, CodeReturn,
		CodeEnd, CodeLoadLocal0, CodeLoadLocal1, CodeLoadLocal2, CodeLoadLocal3,
		CodeLoadLocal4, CodeLoadLocal5, CodeLoadLocal6, CodeLoadLocal7,
		CodeLoadLocal8, CodeConstruct, CodeForeignConstruct, CodeForeignClass,
		CodeEndModule, CodeEndClass:
		return 0

	case CodeLoadLocal, CodeStoreLocal, CodeLoadUpvalue, CodeStoreUpvalue,
		CodeLoadFieldThis, CodeStoreFieldThis, CodeLoadField, CodeStoreField,
		CodeClass:
		return 1

	case CodeClosure:
		constant := (int(bytecode[ip+1]) << 8) | int(bytecode[ip+2])
		loadedFn := constants[constant].AsFn()
		// Two bytes for the constant, then two for each upvalue.
		return 2 + loadedFn.NumUpvalues*2
	}

	if instruction >= CodeSuper0 && instruction <= CodeSuper16 {
		return 4
	}
	return 2
}

func (c *Compiler) startLoop(loop *Loop) {
	loop.enclosing = c.loop
	loop.start = len(c.fn.Code) - 1
	loop.scopeDepth = c.scopeDepth
	c.loop = loop
}

func (c *Compiler) testExitLoop() {
	c.loop.exitJump = c.emitJump(CodeJumpIf)
}

func (c *Compiler) loopBody() {
	c.loop.body = len(c.fn.Code)
	c.statement()
}

// endLoop closes the innermost loop, patching the exit jump and turning the
// placeholder instructions left by break statements into real jumps.
func (c *Compiler) endLoop() {
	// Overflow is not checked here: the forward jump over the body reports
	// the same problem.
	loopOffset := len(c.fn.Code) - c.loop.start + 2
	c.emitShortArg(CodeLoop, loopOffset)

	c.patchJump(c.loop.exitJump)

	i := c.loop.body
	for i < len(c.fn.Code) {
		if Code(c.fn.Code[i]) == CodeEnd {
			c.fn.Code[i] = byte(CodeJump)
			c.patchJump(i + 1)
			i += 3
		} else {
			i += 1 + getByteCountForArguments(c.fn.Code, c.fn.Constants, i)
		}
	}

	c.loop = c.loop.enclosing
}

// forStatement compiles a for-in loop, which is desugared into a while loop
// over the sequence's iterate() and iteratorValue() methods using two hidden
// local variables whose names cannot collide with a user's.
func (c *Compiler) forStatement() {
	c.pushScope()

	c.consume(tokenLeftParen, "Expect '(' after 'for'.")
	c.consume(tokenName, "Expect for loop variable name.")

	loopVarName := c.parser.tokenText(c.parser.previous)

	c.consume(tokenIn, "Expect 'in' after loop variable.")
	c.ignoreNewlines()

	c.expression()

	// Make sure there is room for the two hidden locals.
	if c.numLocals+2 > maxLocals {
		c.error("Cannot declare more than %d variables in one scope. (Not enough space for for-loops internal variables)",
			maxLocals)
		return
	}
	seqSlot := c.addLocal("seq ")

	nullLiteral(c, false)
	iterSlot := c.addLocal("iter ")

	c.consume(tokenRightParen, "Expect ')' after loop expression.")

	var loop Loop
	c.startLoop(&loop)

	c.loadLocal(seqSlot)
	c.loadLocal(iterSlot)

	c.callMethod(1, "iterate(_)")
	c.emitByteArg(CodeStoreLocal, iterSlot)
	c.testExitLoop()

	c.loadLocal(seqSlot)
	c.loadLocal(iterSlot)
	c.callMethod(1, "iteratorValue(_)")

	// Bind the loop variable in its own scope so each iteration gets a fresh
	// one and closures over it do not all see the same variable.
	c.pushScope()
	c.addLocal(loopVarName)

	c.loopBody()

	c.popScope()
	c.endLoop()
	c.popScope()
}

func (c *Compiler) ifStatement() {
	c.consume(tokenLeftParen, "Expect '(' after 'if'.")
	c.expression()
	c.consume(tokenRightParen, "Expect ')' after if condition.")

	ifJump := c.emitJump(CodeJumpIf)
	c.statement()

	if c.match(tokenElse) {
		elseJump := c.emitJump(CodeJump)
		c.patchJump(ifJump)
		c.statement()
		c.patchJump(elseJump)
	} else {
		c.patchJump(ifJump)
	}
}

func (c *Compiler) whileStatement() {
	var loop Loop
	c.startLoop(&loop)

	c.consume(tokenLeftParen, "Expect '(' after 'while'.")
	c.expression()
	c.consume(tokenRightParen, "Expect ')' after while condition.")

	c.testExitLoop()
	c.loopBody()
	c.endLoop()
}

// statement compiles a simple statement. These leave no value on the stack
// and exclude the binding statements like "var" and "class".
func (c *Compiler) statement() {
	switch {
	case c.match(tokenBreak):
		if c.loop == nil {
			c.error("Cannot use 'break' outside of a loop.")
			return
		}
		// Discard any locals in the scope being jumped out of.
		c.discardLocals(c.loop.scopeDepth + 1)
		// Emit a placeholder for the jump to the end of the body. CODE_END is
		// used because it cannot occur in the middle of bytecode; endLoop
		// rewrites these into real jumps.
		c.emitJump(CodeEnd)

	case c.match(tokenContinue):
		if c.loop == nil {
			c.error("Cannot use 'continue' outside of a loop.")
			return
		}
		c.discardLocals(c.loop.scopeDepth + 1)
		loopOffset := len(c.fn.Code) - c.loop.start + 2
		c.emitShortArg(CodeLoop, loopOffset)

	case c.match(tokenFor):
		c.forStatement()

	case c.match(tokenIf):
		c.ifStatement()

	case c.match(tokenReturn):
		if c.peek() == tokenLine {
			// With no expression after return, an initializer returns "this"
			// and an ordinary method returns null.
			result := CodeNull
			if c.isInitializer {
				result = CodeLoadLocal0
			}
			c.emitOp(result)
		} else {
			if c.isInitializer {
				c.error("A constructor cannot return a value.")
			}
			c.expression()
		}
		c.emitOp(CodeReturn)

	case c.match(tokenWhile):
		c.whileStatement()

	case c.match(tokenLeftBrace):
		c.pushScope()
		if c.finishBlock() {
			// The block was an expression, so discard its value.
			c.emitOp(CodePop)
		}
		c.popScope()

	default:
		c.expression()
		c.emitOp(CodePop)
	}
}

// createConstructor creates the metaclass method that allocates an instance
// and runs its initializer. Construction is two methods in Wren: a static one
// that allocates, which then forwards its arguments to the initializer.
func (c *Compiler) createConstructor(signature *Signature, initializerSymbol int) {
	var methodCompiler Compiler
	initCompiler(&methodCompiler, c.parser, c, true)

	if c.enclosingClass.isForeign {
		methodCompiler.emitOp(CodeForeignConstruct)
	} else {
		methodCompiler.emitOp(CodeConstruct)
	}

	methodCompiler.emitShortArg(Code(int(CodeCall0)+signature.arity), initializerSymbol)
	methodCompiler.emitOp(CodeReturn)

	methodCompiler.endCompiler("")
}

// defineMethod loads the enclosing class and binds the function on the stack
// to it as a method. The class is loaded again for each method because static
// fields are locals above its slot.
func (c *Compiler) defineMethod(classVariable Variable, isStatic bool, methodSymbol int) {
	c.loadVariable(classVariable)
	instruction := CodeMethodInstance
	if isStatic {
		instruction = CodeMethodStatic
	}
	c.emitShortArg(instruction, methodSymbol)
}

// declareMethod records a method in the enclosing class, reporting a
// duplicate definition, and returns its symbol.
func (c *Compiler) declareMethod(signature *Signature, name string) int {
	symbol := c.signatureSymbol(signature)

	classInfo := c.enclosingClass
	methods := &classInfo.methods
	if classInfo.inStatic {
		methods = &classInfo.staticMethods
	}
	for _, existing := range *methods {
		if existing == symbol {
			staticPrefix := ""
			if classInfo.inStatic {
				staticPrefix = "static "
			}
			c.error("Class %s already defines a %smethod '%s'.",
				c.enclosingClass.name.Value, staticPrefix, name)
			break
		}
	}

	*methods = append(*methods, symbol)
	return symbol
}

func (c *Compiler) consumeLiteral(message string) Value {
	if c.match(tokenFalse) {
		return FalseVal
	}
	if c.match(tokenTrue) {
		return TrueVal
	}
	if c.match(tokenNumber) {
		return c.parser.previous.value
	}
	if c.match(tokenString) {
		return c.parser.previous.value
	}
	if c.match(tokenName) {
		return c.parser.previous.value
	}
	c.error("%s", message)
	c.parser.nextToken()
	return NullVal
}

// matchAttribute parses one "#" attribute line if there is one.
func (c *Compiler) matchAttribute() bool {
	if !c.match(tokenHash) {
		return false
	}

	c.numAttributes++
	runtimeAccess := c.match(tokenBang)
	if c.match(tokenName) {
		group := c.parser.previous.value
		ahead := c.peek()
		if ahead == tokenEq || ahead == tokenLine {
			key := group
			value := NullVal
			if c.match(tokenEq) {
				value = c.consumeLiteral("Expect a Bool, Num, String or Identifier literal for an attribute value.")
			}
			if runtimeAccess {
				c.addToAttributeGroup(NullVal, key, value)
			}
		} else if c.match(tokenLeftParen) {
			c.ignoreNewlines()
			if c.match(tokenRightParen) {
				c.error("Expected attributes in group, group cannot be empty.")
			} else {
				for c.peek() != tokenRightParen {
					c.consume(tokenName, "Expect name for attribute key.")
					key := c.parser.previous.value
					value := NullVal
					if c.match(tokenEq) {
						value = c.consumeLiteral("Expect a Bool, Num, String or Identifier literal for an attribute value.")
					}
					if runtimeAccess {
						c.addToAttributeGroup(group, key, value)
					}
					c.ignoreNewlines()
					if !c.match(tokenComma) {
						break
					}
					c.ignoreNewlines()
				}
				c.ignoreNewlines()
				c.consume(tokenRightParen, "Expected ')' after grouped attributes.")
			}
		} else {
			c.error("Expect an equal, newline or grouping after an attribute key.")
		}
	} else {
		c.error("Expect an attribute definition after #.")
	}

	c.consumeLine("Expect newline after attribute.")
	return true
}

// method compiles one method definition inside a class body. It returns false
// if the method could not be parsed.
func (c *Compiler) method(classVariable Variable) bool {
	// Parse and store any attributes written before the method.
	if c.matchAttribute() {
		return c.method(classVariable)
	}

	isForeign := c.match(tokenForeign)
	isStatic := c.match(tokenStatic)
	c.enclosingClass.inStatic = isStatic

	signatureFn := rules[c.parser.current.typ].method
	c.parser.nextToken()

	if signatureFn == nil {
		c.error("Expect method definition.")
		return false
	}

	signature := c.signatureFromToken(sigGetter)
	c.enclosingClass.signature = &signature

	var methodCompiler Compiler
	initCompiler(&methodCompiler, c.parser, c, true)

	signatureFn(&methodCompiler, &signature)

	methodCompiler.isInitializer = signature.typ == sigInitializer

	if isStatic && signature.typ == sigInitializer {
		c.error("A constructor cannot be static.")
	}

	// The full signature appears in stack traces.
	fullSignature := signatureToString(&signature)

	c.copyMethodAttributes(isForeign, isStatic, fullSignature)

	// Check for a duplicate method. It does not matter that the method has
	// already been defined: the error discards the bytecode anyway.
	methodSymbol := c.declareMethod(&signature, fullSignature)

	if isForeign {
		// Define a constant for the signature.
		c.emitConstant(c.parser.vm.NewString(fullSignature))
		// The function started for the parameter list is not needed.
		methodCompiler.parser.vm.compiler = methodCompiler.parent
	} else {
		c.consume(tokenLeftBrace, "Expect '{' to begin method body.")
		methodCompiler.finishBody()
		methodCompiler.endCompiler(fullSignature)
	}

	c.defineMethod(classVariable, isStatic, methodSymbol)

	if signature.typ == sigInitializer {
		// Also define the matching constructor on the metaclass.
		signature.typ = sigMethod
		constructorSymbol := c.signatureSymbol(&signature)
		c.createConstructor(&signature, methodSymbol)
		c.defineMethod(classVariable, true, constructorSymbol)
	}

	return true
}

// classDefinition compiles a class definition; the "class" token (and any
// preceding "foreign") has already been consumed.
func (c *Compiler) classDefinition(isForeign bool) {
	var classVariable Variable
	classVariable.scope = scopeLocal
	if c.scopeDepth == -1 {
		classVariable.scope = scopeModule
	}
	classVariable.index = c.declareNamedVariable()

	classNameString := c.parser.vm.NewString(c.parser.tokenText(c.parser.previous))
	className := classNameString.AsString()

	c.emitConstant(classNameString)

	if c.match(tokenIs) {
		c.parsePrecedence(precCall)
	} else {
		// Implicitly inherit from Object.
		c.loadCoreVariable("Object")
	}

	// Leave a placeholder for the field count: it is not known until every
	// method has been compiled and the fields it uses are known.
	numFieldsInstruction := -1
	if isForeign {
		c.emitOp(CodeForeignClass)
	} else {
		numFieldsInstruction = c.emitByteArg(CodeClass, 255)
	}

	c.defineVariable(classVariable.index)

	// Static fields in a class body are hoisted into locals in this scope, and
	// methods that use them get upvalues referring to them.
	c.pushScope()

	var classInfo ClassInfo
	classInfo.isForeign = isForeign
	classInfo.name = className

	if c.attributes.Count > 0 {
		classInfo.classAttributes = c.parser.vm.NewMap()
	}
	classInfo.methodAttributes = nil
	c.copyAttributes(classInfo.classAttributes)

	// The fields are first compiled to slots starting at zero; binding the
	// method to the class shifts them past the inherited fields.
	classInfo.fields = nil
	classInfo.methods = nil
	classInfo.staticMethods = nil
	c.enclosingClass = &classInfo

	c.consume(tokenLeftBrace, "Expect '{' after class declaration.")
	c.matchLine()

	for !c.match(tokenRightBrace) {
		if !c.method(classVariable) {
			break
		}
		// A newline is not required after the last definition.
		if c.match(tokenRightBrace) {
			break
		}
		c.consumeLine("Expect newline after definition in class.")
	}

	// With any attributes present, build a ClassAttributes instance for the
	// class and hand it to CODE_END_CLASS.
	hasAttr := classInfo.classAttributes != nil || classInfo.methodAttributes != nil
	if hasAttr {
		c.emitClassAttributes(&classInfo)
		c.loadVariable(classVariable)
		c.emitOp(CodeEndClass)
	}

	if !isForeign {
		c.fn.Code[numFieldsInstruction] = byte(len(classInfo.fields))
	}

	c.enclosingClass = nil
	c.popScope()
}

// importStatement compiles an "import" statement. The module itself is loaded
// by one IMPORT_MODULE instruction, and each imported variable by an
// IMPORT_VARIABLE instruction that reads it out of the module just loaded.
func (c *Compiler) importStatement() {
	c.ignoreNewlines()
	c.consume(tokenString, "Expect a string after 'import'.")
	moduleConstant := c.addConstant(c.parser.previous.value)

	c.emitShortArg(CodeImportModule, moduleConstant)

	// Discard the unused result of calling the module body's closure.
	c.emitOp(CodePop)

	if !c.match(tokenFor) {
		return
	}

	for {
		c.ignoreNewlines()
		c.consume(tokenName, "Expect variable name.")

		sourceVariableToken := c.parser.previous
		sourceVariableConstant := c.addConstant(
			c.parser.vm.NewString(c.parser.tokenText(sourceVariableToken)))

		slot := -1
		if c.match(tokenAs) {
			// import "module" for Source as Dest: declare Dest here.
			slot = c.declareNamedVariable()
		} else {
			slot = c.declareVariable(&sourceVariableToken)
		}

		c.emitShortArg(CodeImportVariable, sourceVariableConstant)
		c.defineVariable(slot)

		if !c.match(tokenComma) {
			break
		}
	}
}

func (c *Compiler) variableDefinition() {
	// Grab the name but do not declare it yet: a local variable must not be
	// in scope inside its own initializer.
	c.consume(tokenName, "Expect variable name.")
	nameToken := c.parser.previous

	if c.match(tokenEq) {
		c.ignoreNewlines()
		c.expression()
	} else {
		nullLiteral(c, false)
	}

	symbol := c.declareVariable(&nameToken)
	c.defineVariable(symbol)
}

// definition compiles a definition: a statement that binds a new name. These
// may only appear at the top level of a block.
func (c *Compiler) definition() {
	if c.matchAttribute() {
		c.definition()
		return
	}

	if c.match(tokenClass) {
		c.classDefinition(false)
		return
	} else if c.match(tokenForeign) {
		c.consume(tokenClass, "Expect 'class' after 'foreign'.")
		c.classDefinition(true)
		return
	}

	c.disallowAttributes()

	switch {
	case c.match(tokenImport):
		c.importStatement()
	case c.match(tokenVar):
		c.variableDefinition()
	default:
		c.statement()
	}
}

// Compile compiles source in the context of module and returns the resulting
// function, or nil if there was a compile error.
func (vm *VM) Compile(module *ObjModule, source string, isExpression, printErrors bool) *ObjFn {
	// Skip the UTF-8 byte order mark if there is one.
	source = strings.TrimPrefix(source, "\xEF\xBB\xBF")

	parser := &Parser{
		vm:          vm,
		module:      module,
		source:      source,
		tokenStart:  0,
		currentChar: 0,
		currentLine: 1,
		numParens:   0,
		printErrors: printErrors,
	}
	parser.next = Token{typ: tokenError, start: 0, length: 0, line: 0, value: UndefinedVal}

	// Read the first token into next, then copy next into current.
	parser.nextToken()
	parser.nextToken()

	numExistingVariables := len(module.Variables)

	var compiler Compiler
	initCompiler(&compiler, parser, nil, false)
	compiler.ignoreNewlines()

	if isExpression {
		compiler.expression()
		compiler.consume(tokenEOF, "Expect end of expression.")
	} else {
		for !compiler.match(tokenEOF) {
			compiler.definition()

			// With no newline it must be the end of the file on the same line.
			if !compiler.matchLine() {
				compiler.consume(tokenEOF, "Expect end of file.")
				break
			}
		}
		compiler.emitOp(CodeEndModule)
	}

	compiler.emitOp(CodeReturn)

	// Any module-level variable that was implicitly declared but never
	// defined still holds the number of the line it was first used on.
	for i := numExistingVariables; i < len(parser.module.Variables); i++ {
		if parser.module.Variables[i].IsNum() {
			// Synthesize a token for the original use site.
			parser.previous.typ = tokenName
			parser.previous.start = -1
			parser.previous.length = len(parser.module.VariableNames[i].Value)
			parser.previous.line = int(parser.module.Variables[i].AsNum())
			parser.synthetic = parser.module.VariableNames[i].Value
			compiler.error("Variable is used but not defined.")
			parser.synthetic = ""
		}
	}

	return compiler.endCompiler("(script)")
}

// BindMethodCode patches a method's bytecode once its class is known: field
// indices are shifted past the inherited fields, and the constant slot of
// each super call is filled in with the superclass.
func BindMethodCode(classObj *ObjClass, fn *ObjFn) {
	ip := 0
	for {
		instruction := Code(fn.Code[ip])
		switch {
		case instruction == CodeLoadField || instruction == CodeStoreField ||
			instruction == CodeLoadFieldThis || instruction == CodeStoreFieldThis:
			// Overflow is not checked here: creating the subclass reports it.
			fn.Code[ip+1] += byte(classObj.Superclass.NumFields)

		case instruction >= CodeSuper0 && instruction <= CodeSuper16:
			constant := (int(fn.Code[ip+3]) << 8) | int(fn.Code[ip+4])
			fn.Constants[constant] = ObjVal(classObj.Superclass)

		case instruction == CodeClosure:
			constant := (int(fn.Code[ip+1]) << 8) | int(fn.Code[ip+2])
			BindMethodCode(classObj, fn.Constants[constant].AsFn())

		case instruction == CodeEnd:
			return
		}
		ip += 1 + getByteCountForArguments(fn.Code, fn.Constants, ip)
	}
}

// Helpers for attributes ------------------------------------------------------

// disallowAttributes reports attributes written anywhere other than before a
// class or method, and clears them so the error is not repeated.
func (c *Compiler) disallowAttributes() {
	if c.numAttributes > 0 {
		c.error("Attributes can only specified before a class or a method")
		c.attributes.MapClear()
		c.numAttributes = 0
	}
}

// addToAttributeGroup records one attribute. Attributes are stored as
// group = { key: [value, ...] } so that duplicate keys are kept.
func (c *Compiler) addToAttributeGroup(group, key, value Value) {
	vm := c.parser.vm

	groupMapValue := c.attributes.MapGet(group)
	if groupMapValue.IsUndefined() {
		groupMapValue = ObjVal(vm.NewMap())
		c.attributes.MapSet(group, groupMapValue)
	}
	groupMap := groupMapValue.AsMap()

	keyItemsValue := groupMap.MapGet(key)
	if keyItemsValue.IsUndefined() {
		keyItemsValue = ObjVal(vm.NewList(0))
		groupMap.MapSet(key, keyItemsValue)
	}

	keyItemsValue.AsList().listWrite(value)
}

// emitAttributes emits the code that builds the map for one attribute set.
func (c *Compiler) emitAttributes(attributes *ObjMap) {
	c.loadCoreVariable("Map")
	c.callMethod(0, "new()")

	// Attributes are stored as group = { key: [value, ...] }, so the first
	// level is the group map.
	for groupIdx := range attributes.Entries {
		groupEntry := &attributes.Entries[groupIdx]
		if groupEntry.Key.IsUndefined() {
			continue
		}
		c.emitConstant(groupEntry.Key)

		c.loadCoreVariable("Map")
		c.callMethod(0, "new()")

		groupItems := groupEntry.Value.AsMap()
		for itemIdx := range groupItems.Entries {
			itemEntry := &groupItems.Entries[itemIdx]
			if itemEntry.Key.IsUndefined() {
				continue
			}
			c.emitConstant(itemEntry.Key)

			c.loadCoreVariable("List")
			c.callMethod(0, "new()")

			items := itemEntry.Value.AsList()
			for _, item := range items.Elements {
				c.emitConstant(item)
				c.callMethod(1, "addCore_(_)")
			}
			c.callMethod(2, "addCore_(_,_)")
		}
		c.callMethod(2, "addCore_(_,_)")
	}
}

// emitAttributeMethods emits the map from a method's signature to its
// attributes.
func (c *Compiler) emitAttributeMethods(attributes *ObjMap) {
	c.loadCoreVariable("Map")
	c.callMethod(0, "new()")

	for methodIdx := range attributes.Entries {
		methodEntry := &attributes.Entries[methodIdx]
		if methodEntry.Key.IsUndefined() {
			continue
		}
		c.emitConstant(methodEntry.Key)
		c.emitAttributes(methodEntry.Value.AsMap())
		c.callMethod(2, "addCore_(_,_)")
	}
}

// emitClassAttributes emits the ClassAttributes instance that exists at
// runtime.
func (c *Compiler) emitClassAttributes(classInfo *ClassInfo) {
	c.loadCoreVariable("ClassAttributes")

	if classInfo.classAttributes != nil {
		c.emitAttributes(classInfo.classAttributes)
	} else {
		nullLiteral(c, false)
	}

	if classInfo.methodAttributes != nil {
		c.emitAttributeMethods(classInfo.methodAttributes)
	} else {
		nullLiteral(c, false)
	}

	c.callMethod(2, "new(_,_)")
}

// copyAttributes moves the attributes collected so far into a destination map
// and resets the counter, since the attributes are being consumed.
func (c *Compiler) copyAttributes(into *ObjMap) {
	c.numAttributes = 0

	if c.attributes.Count == 0 {
		return
	}
	if into == nil {
		return
	}

	// The values are copied as they are: ownership moves to the destination
	// and the original map is cleared.
	for attrIdx := range c.attributes.Entries {
		attrEntry := &c.attributes.Entries[attrIdx]
		if attrEntry.Key.IsUndefined() {
			continue
		}
		into.MapSet(attrEntry.Key, attrEntry.Value)
	}

	c.attributes.MapClear()
}

// copyMethodAttributes moves the attributes collected so far into the
// enclosing class's per-method attribute map, keyed by the method's full
// signature with any "foreign " and "static " prefix.
func (c *Compiler) copyMethodAttributes(isForeign, isStatic bool, fullSignature string) {
	c.numAttributes = 0

	if c.attributes.Count == 0 {
		return
	}

	vm := c.parser.vm

	methodAttr := vm.NewMap()
	c.copyAttributes(methodAttr)

	foreignPrefix := ""
	if isForeign {
		foreignPrefix = "foreign "
	}
	staticPrefix := ""
	if isStatic {
		staticPrefix = "static "
	}
	fullSignatureWithPrefix := foreignPrefix + staticPrefix + fullSignature

	if c.enclosingClass.methodAttributes == nil {
		c.enclosingClass.methodAttributes = vm.NewMap()
	}

	key := vm.NewString(fullSignatureWithPrefix)
	c.enclosingClass.methodAttributes.MapSet(key, ObjVal(methodAttr))
}
