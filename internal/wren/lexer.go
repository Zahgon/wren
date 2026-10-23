// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the lexing half of the Wren VM's src/vm/wren_compiler.c.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import (
	"fmt"
	"math"
	"strings"
)

// Limits that are explicit in the bytecode or in the error messages, and so
// are part of the observable behaviour.
const (
	maxLocals               = 256
	maxUpvalues             = 256
	maxConstants            = 1 << 16
	maxJump                 = 1 << 16
	maxInterpolationNesting = 8
	maxModuleVars           = 65536
	maxParameters           = 16
	maxMethodName           = 64
	maxVariableName         = 64
	maxFields               = 255
)

// TokenType identifies a lexical token. The order matters only in that the
// grammar rule table below is indexed by it.
type TokenType int

const (
	tokenLeftParen TokenType = iota
	tokenRightParen
	tokenLeftBracket
	tokenRightBracket
	tokenLeftBrace
	tokenRightBrace
	tokenColon
	tokenDot
	tokenDotDot
	tokenDotDotDot
	tokenComma
	tokenStar
	tokenSlash
	tokenPercent
	tokenHash
	tokenPlus
	tokenMinus
	tokenLtLt
	tokenGtGt
	tokenPipe
	tokenPipePipe
	tokenCaret
	tokenAmp
	tokenAmpAmp
	tokenBang
	tokenTilde
	tokenQuestion
	tokenEq
	tokenLt
	tokenGt
	tokenLtEq
	tokenGtEq
	tokenEqEq
	tokenBangEq

	tokenBreak
	tokenContinue
	tokenClass
	tokenConstruct
	tokenElse
	tokenFalse
	tokenFor
	tokenForeign
	tokenIf
	tokenImport
	tokenAs
	tokenIn
	tokenIs
	tokenNull
	tokenReturn
	tokenStatic
	tokenSuper
	tokenThis
	tokenTrue
	tokenVar
	tokenWhile

	tokenField
	tokenStaticField
	tokenName
	tokenNumber

	// tokenString is a string literal with no interpolation, or the last
	// section of a string after the final interpolated expression.
	tokenString

	// tokenInterpolation is the part of a string literal before an
	// interpolated expression.
	tokenInterpolation

	tokenLine

	tokenError
	tokenEOF

	numTokenTypes
)

// Token is one lexical token.
type Token struct {
	typ TokenType
	// start is the byte offset of the token in the source, and length its
	// length in bytes.
	start  int
	length int
	// line is the 1-based line the token appears on.
	line int
	// value is the parsed value of a literal token.
	value Value
}

// Parser holds the lexer and parser state for one compilation unit.
type Parser struct {
	vm     *VM
	module *ObjModule
	source string

	// tokenStart is the offset of the token being lexed and currentChar the
	// offset of the character being looked at.
	tokenStart  int
	currentChar int
	currentLine int

	next     Token
	current  Token
	previous Token

	// parens tracks, for each level of string interpolation currently open,
	// how many unmatched "(" are waiting to be closed. Interpolation makes
	// the lexer non-regular: whether a ")" ends an interpolated expression or
	// is an ordinary token depends on this.
	parens    [maxInterpolationNesting]int
	numParens int

	printErrors bool
	hasError    bool

	// synthetic holds the text of a token that was synthesized rather than
	// lexed, which is how an undefined module variable is reported against
	// its first use site.
	synthetic string
}

func (p *Parser) tokenText(t Token) string {
	if t.start < 0 {
		return p.synthetic
	}
	start := t.start
	if start > len(p.source) {
		start = len(p.source)
	}
	end := start + t.length
	if end > len(p.source) {
		end = len(p.source)
	}
	if end < start {
		end = start
	}
	return p.source[start:end]
}

// printError reports a compile error through the host's error function. The
// message handed over is "<label>: <message>".
func (p *Parser) printError(line int, label, format string, args ...any) {
	p.hasError = true
	if !p.printErrors {
		return
	}
	if p.vm.config.ErrorFn == nil {
		return
	}
	message := label + ": " + fmt.Sprintf(format, args...)

	moduleName := "<unknown>"
	if p.module.Name != nil {
		moduleName = p.module.Name.Value
	}
	p.vm.config.ErrorFn(p.vm, ErrorCompile, moduleName, line, message)
}

// lexError reports a lexical error at the current line.
func (p *Parser) lexError(format string, args ...any) {
	p.printError(p.currentLine, "Error", format, args...)
}

// error reports a compile or syntax error against the most recently consumed
// token. Compilation carries on afterwards so that one pass can report as
// many errors as possible; the resulting code is discarded.
func (c *Compiler) error(format string, args ...any) {
	token := c.parser.previous

	// A parse error caused by an error token was already reported by the lexer.
	if token.typ == tokenError {
		return
	}

	switch token.typ {
	case tokenLine:
		c.parser.printError(token.line, "Error at newline", format, args...)
	case tokenEOF:
		c.parser.printError(token.line, "Error at end of file", format, args...)
	default:
		text := c.parser.tokenText(token)
		var label string
		if token.length <= maxVariableName {
			label = "Error at '" + text + "'"
		} else {
			label = "Error at '" + text[:maxVariableName] + "...'"
		}
		c.parser.printError(token.line, label, format, args...)
	}
}

// Keyword is a reserved word and the token type it lexes to.
type Keyword struct {
	identifier string
	tokenType  TokenType
}

var keywords = []Keyword{
	{"break", tokenBreak},
	{"continue", tokenContinue},
	{"class", tokenClass},
	{"construct", tokenConstruct},
	{"else", tokenElse},
	{"false", tokenFalse},
	{"for", tokenFor},
	{"foreign", tokenForeign},
	{"if", tokenIf},
	{"import", tokenImport},
	{"as", tokenAs},
	{"in", tokenIn},
	{"is", tokenIs},
	{"null", tokenNull},
	{"return", tokenReturn},
	{"static", tokenStatic},
	{"super", tokenSuper},
	{"this", tokenThis},
	{"true", tokenTrue},
	{"var", tokenVar},
	{"while", tokenWhile},
}

// isName reports whether c may appear in an identifier. Only ASCII letters and
// the underscore may: the lexer works on raw bytes.
func isName(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// peekChar returns the byte the parser is sitting on, or 0 at the end of the
// source. An embedded zero byte therefore ends lexing, as it does in C.
func (p *Parser) peekChar() byte {
	if p.currentChar < 0 || p.currentChar >= len(p.source) {
		return 0
	}
	return p.source[p.currentChar]
}

func (p *Parser) peekNextChar() byte {
	if p.peekChar() == 0 {
		return 0
	}
	if p.currentChar+1 >= len(p.source) {
		return 0
	}
	return p.source[p.currentChar+1]
}

func (p *Parser) nextChar() byte {
	c := p.peekChar()
	p.currentChar++
	if c == '\n' {
		p.currentLine++
	}
	return c
}

func (p *Parser) matchChar(c byte) bool {
	if p.peekChar() != c {
		return false
	}
	p.nextChar()
	return true
}

func (p *Parser) makeToken(typ TokenType) {
	p.next.typ = typ
	p.next.start = p.tokenStart
	p.next.length = p.currentChar - p.tokenStart
	p.next.line = p.currentLine

	// Make line tokens appear on the line containing the "\n".
	if typ == tokenLine {
		p.next.line--
	}
}

func (p *Parser) twoCharToken(c byte, two, one TokenType) {
	if p.matchChar(c) {
		p.makeToken(two)
	} else {
		p.makeToken(one)
	}
}

func (p *Parser) skipLineComment() {
	for p.peekChar() != '\n' && p.peekChar() != 0 {
		p.nextChar()
	}
}

func (p *Parser) skipBlockComment() {
	nesting := 1
	for nesting > 0 {
		if p.peekChar() == 0 {
			p.lexError("Unterminated block comment.")
			return
		}
		if p.peekChar() == '/' && p.peekNextChar() == '*' {
			p.nextChar()
			p.nextChar()
			nesting++
			continue
		}
		if p.peekChar() == '*' && p.peekNextChar() == '/' {
			p.nextChar()
			p.nextChar()
			nesting--
			continue
		}
		p.nextChar()
	}
}

// readHexDigit consumes one hexadecimal digit and returns its value, or -1 if
// the next byte is not one (without consuming it).
func (p *Parser) readHexDigit() int {
	c := p.nextChar()
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	p.currentChar--
	return -1
}

// makeNumber parses the numeric value of the token just scanned. A literal
// whose value is out of range is a lexical error and becomes zero; note that
// the original reports the size of a C `long int` in that message.
func (p *Parser) makeNumber(isHex bool) {
	text := p.source[p.tokenStart:min(p.currentChar, len(p.source))]

	var value float64
	var rangeErr bool
	if isHex {
		value, rangeErr = strToLongLongBase16(text)
	} else {
		value, rangeErr = strToDouble(text)
	}
	p.next.value = NumVal(value)

	if rangeErr {
		p.lexError("Number literal was too large (%d).", 8)
		p.next.value = NumVal(0)
	}
	p.makeToken(tokenNumber)
}

// strToLongLongBase16 mirrors C's strtoll(text, NULL, 16) on a token the lexer
// has already scanned: an optional "0x" prefix followed by hex digits, with
// overflow clamped to LLONG_MAX and reported as a range error.
func strToLongLongBase16(text string) (float64, bool) {
	i := 0
	neg := false
	if i < len(text) && (text[i] == '+' || text[i] == '-') {
		neg = text[i] == '-'
		i++
	}
	if i+1 < len(text) && text[i] == '0' && (text[i+1] == 'x' || text[i+1] == 'X') {
		i += 2
	}
	var acc uint64
	var overflow bool
	digits := 0
	for ; i < len(text); i++ {
		var d uint64
		c := text[i]
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint64(c-'A') + 10
		default:
			i = len(text)
		}
		if i == len(text) {
			break
		}
		digits++
		if acc > (math.MaxInt64-d)/16 {
			overflow = true
		}
		acc = acc*16 + d
	}
	if digits == 0 {
		return 0, false
	}
	if overflow {
		if neg {
			return float64(math.MinInt64), true
		}
		return float64(math.MaxInt64), true
	}
	if neg {
		return -float64(acc), false
	}
	return float64(acc), false
}

// strToDouble mirrors C's strtod on a decimal token: it converts the longest
// valid prefix and reports a range error when the result overflows to
// infinity or underflows to zero or a subnormal, as glibc does.
func strToDouble(text string) (float64, bool) {
	value, consumed, rangeErr := cStrtod(text)
	if consumed != len(text) {
		return 0, false
	}
	return value, rangeErr
}

func hasNonZeroMantissa(text string) bool {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == 'e' || c == 'E' {
			break
		}
		if c >= '1' && c <= '9' {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (p *Parser) readHexNumber() {
	// Skip past the "x" that marks the literal as hexadecimal.
	p.nextChar()
	for p.readHexDigit() != -1 {
	}
	p.makeNumber(true)
}

func (p *Parser) readNumber() {
	for isDigit(p.peekChar()) {
		p.nextChar()
	}

	// A "." only continues the number if a digit follows it, so that method
	// calls on number literals still work.
	if p.peekChar() == '.' && isDigit(p.peekNextChar()) {
		p.nextChar()
		for isDigit(p.peekChar()) {
			p.nextChar()
		}
	}

	if p.matchChar('e') || p.matchChar('E') {
		if !p.matchChar('+') {
			p.matchChar('-')
		}
		if !isDigit(p.peekChar()) {
			p.lexError("Unterminated scientific notation.")
		}
		for isDigit(p.peekChar()) {
			p.nextChar()
		}
	}

	p.makeNumber(false)
}

// readName finishes lexing an identifier, promoting it to a keyword token if
// it is a reserved word.
func (p *Parser) readName(typ TokenType, firstChar byte) {
	var b strings.Builder
	b.WriteByte(firstChar)
	for isName(p.peekChar()) || isDigit(p.peekChar()) {
		b.WriteByte(p.nextChar())
	}

	length := p.currentChar - p.tokenStart
	text := p.source[p.tokenStart:min(p.tokenStart+length, len(p.source))]
	for _, keyword := range keywords {
		if text == keyword.identifier {
			typ = keyword.tokenType
			break
		}
	}

	p.next.value = p.vm.NewString(b.String())
	p.makeToken(typ)
}

// readHexEscape reads the given number of hex digits of an escape sequence.
func (p *Parser) readHexEscape(digits int, description string) int {
	value := 0
	for i := 0; i < digits; i++ {
		if p.peekChar() == '"' || p.peekChar() == 0 {
			p.lexError("Incomplete %s escape sequence.", description)
			// Don't consume it: that would read past an unterminated string.
			p.currentChar--
			break
		}
		digit := p.readHexDigit()
		if digit == -1 {
			p.lexError("Invalid %s escape sequence.", description)
			break
		}
		value = (value * 16) | digit
	}
	return value
}

func (p *Parser) readUnicodeEscape(b *[]byte, length int) {
	value := p.readHexEscape(length, "Unicode")
	numBytes := Utf8EncodeNumBytes(value)
	if numBytes != 0 {
		var buf [4]byte
		Utf8Encode(value, buf[:])
		*b = append(*b, buf[:numBytes]...)
	}
}

// readRawString lexes a triple-quoted string. Leading whitespace on the first
// line and the trailing newline plus whitespace of the last are stripped.
func (p *Parser) readRawString() {
	var str []byte
	typ := tokenString

	// Consume the second and third quote.
	p.nextChar()
	p.nextChar()

	skipStart := 0
	firstNewline := -1
	skipEnd := -1
	lastNewline := -1

	for {
		c := p.nextChar()
		c1 := p.peekChar()
		c2 := p.peekNextChar()

		if c == '\n' {
			lastNewline = len(str)
			skipEnd = lastNewline
			if firstNewline == -1 {
				firstNewline = len(str)
			}
		}

		if c == '"' && c1 == '"' && c2 == '"' {
			break
		}

		isWhitespace := c == ' ' || c == '\t'
		if c != '\n' && !isWhitespace {
			skipEnd = -1
		}

		// Count leading whitespace as skippable until we know otherwise.
		skippable := skipStart != -1 && isWhitespace && firstNewline == -1
		if skippable {
			skipStart = len(str) + 1
		}

		// Leading whitespace was followed by something that is not a newline,
		// so those characters are needed after all.
		if firstNewline == -1 && !isWhitespace && c != '\n' {
			skipStart = -1
		}

		if c == 0 || c1 == 0 || c2 == 0 {
			p.lexError("Unterminated raw string.")
			p.currentChar--
			break
		}

		str = append(str, c)
	}

	// Consume the second and third quote.
	p.nextChar()
	p.nextChar()

	offset := 0
	count := len(str)

	if firstNewline != -1 && skipStart == firstNewline {
		offset = firstNewline + 1
	}
	if lastNewline != -1 && skipEnd == lastNewline {
		count = lastNewline
	}
	if offset > count {
		count -= count
	} else {
		count -= offset
	}
	if offset > len(str) {
		offset = len(str)
	}
	if offset+count > len(str) {
		count = len(str) - offset
	}

	p.next.value = p.vm.NewString(string(str[offset : offset+count]))
	p.makeToken(typ)
}

// readString lexes an ordinary string literal, handling escapes and the start
// of an interpolated expression.
func (p *Parser) readString() {
	var str []byte
	typ := tokenString

	for {
		c := p.nextChar()
		if c == '"' {
			break
		}

		if c == 0 {
			p.lexError("Unterminated string.")
			p.currentChar--
			break
		}

		if c == '%' {
			if p.numParens < maxInterpolationNesting {
				if p.nextChar() != '(' {
					p.lexError("Expect '(' after '%%'.")
				}
				p.parens[p.numParens] = 1
				p.numParens++
				typ = tokenInterpolation
				break
			}
			p.lexError("Interpolation may only nest %d levels deep.",
				maxInterpolationNesting)
		}

		if c == '\\' {
			switch p.nextChar() {
			case '"':
				str = append(str, '"')
			case '\\':
				str = append(str, '\\')
			case '%':
				str = append(str, '%')
			case '0':
				str = append(str, 0)
			case 'a':
				str = append(str, 7)
			case 'b':
				str = append(str, 8)
			case 'e':
				str = append(str, 27)
			case 'f':
				str = append(str, 12)
			case 'n':
				str = append(str, '\n')
			case 'r':
				str = append(str, '\r')
			case 't':
				str = append(str, '\t')
			case 'u':
				p.readUnicodeEscape(&str, 4)
			case 'U':
				p.readUnicodeEscape(&str, 8)
			case 'v':
				str = append(str, 11)
			case 'x':
				str = append(str, byte(p.readHexEscape(2, "byte")))
			default:
				bad := byte(0)
				if p.currentChar-1 >= 0 && p.currentChar-1 < len(p.source) {
					bad = p.source[p.currentChar-1]
				}
				p.lexError("Invalid escape character '%s'.", string([]byte{bad}))
			}
		} else {
			str = append(str, c)
		}
	}

	p.next.value = p.vm.NewString(string(str))
	p.makeToken(typ)
}

// nextToken lexes the next token into parser.next, shifting the previous two
// tokens along.
func (p *Parser) nextToken() {
	p.previous = p.current
	p.current = p.next

	// Once we are out of tokens, keep handing out the end-of-file token.
	if p.next.typ == tokenEOF {
		return
	}
	if p.current.typ == tokenEOF {
		return
	}

	for p.peekChar() != 0 {
		p.tokenStart = p.currentChar

		c := p.nextChar()
		switch c {
		case '(':
			// Inside an interpolated expression, count the unmatched "(".
			if p.numParens > 0 {
				p.parens[p.numParens-1]++
			}
			p.makeToken(tokenLeftParen)
			return

		case ')':
			if p.numParens > 0 {
				p.parens[p.numParens-1]--
				if p.parens[p.numParens-1] == 0 {
					// The final ")" ends the interpolated expression and
					// begins the next section of the template string.
					p.numParens--
					p.readString()
					return
				}
			}
			p.makeToken(tokenRightParen)
			return

		case '[':
			p.makeToken(tokenLeftBracket)
			return
		case ']':
			p.makeToken(tokenRightBracket)
			return
		case '{':
			p.makeToken(tokenLeftBrace)
			return
		case '}':
			p.makeToken(tokenRightBrace)
			return
		case ':':
			p.makeToken(tokenColon)
			return
		case ',':
			p.makeToken(tokenComma)
			return
		case '*':
			p.makeToken(tokenStar)
			return
		case '%':
			p.makeToken(tokenPercent)
			return
		case '#':
			// Ignore a shebang on the first line.
			if p.currentLine == 1 && p.peekChar() == '!' && p.peekNextChar() == '/' {
				p.skipLineComment()
				continue
			}
			p.makeToken(tokenHash)
			return
		case '^':
			p.makeToken(tokenCaret)
			return
		case '+':
			p.makeToken(tokenPlus)
			return
		case '-':
			p.makeToken(tokenMinus)
			return
		case '~':
			p.makeToken(tokenTilde)
			return
		case '?':
			p.makeToken(tokenQuestion)
			return

		case '|':
			p.twoCharToken('|', tokenPipePipe, tokenPipe)
			return
		case '&':
			p.twoCharToken('&', tokenAmpAmp, tokenAmp)
			return
		case '=':
			p.twoCharToken('=', tokenEqEq, tokenEq)
			return
		case '!':
			p.twoCharToken('=', tokenBangEq, tokenBang)
			return

		case '.':
			if p.matchChar('.') {
				p.twoCharToken('.', tokenDotDotDot, tokenDotDot)
				return
			}
			p.makeToken(tokenDot)
			return

		case '/':
			if p.matchChar('/') {
				p.skipLineComment()
				continue
			}
			if p.matchChar('*') {
				p.skipBlockComment()
				continue
			}
			p.makeToken(tokenSlash)
			return

		case '<':
			if p.matchChar('<') {
				p.makeToken(tokenLtLt)
			} else {
				p.twoCharToken('=', tokenLtEq, tokenLt)
			}
			return

		case '>':
			if p.matchChar('>') {
				p.makeToken(tokenGtGt)
			} else {
				p.twoCharToken('=', tokenGtEq, tokenGt)
			}
			return

		case '\n':
			p.makeToken(tokenLine)
			return

		case ' ', '\r', '\t':
			for p.peekChar() == ' ' || p.peekChar() == '\r' || p.peekChar() == '\t' {
				p.nextChar()
			}
			continue

		case '"':
			if p.peekChar() == '"' && p.peekNextChar() == '"' {
				p.readRawString()
				return
			}
			p.readString()
			return

		case '_':
			typ := tokenField
			if p.peekChar() == '_' {
				typ = tokenStaticField
			}
			p.readName(typ, c)
			return

		case '0':
			if p.peekChar() == 'x' {
				p.readHexNumber()
				return
			}
			p.readNumber()
			return

		default:
			if isName(c) {
				p.readName(tokenName, c)
			} else if isDigit(c) {
				p.readNumber()
			} else {
				if c >= 32 && c <= 126 {
					p.lexError("Invalid character '%s'.", string([]byte{c}))
				} else {
					// Non-ASCII bytes are not shown as characters: the lexer
					// works on raw bytes even though the source is UTF-8.
					p.lexError("Invalid byte 0x%x.", c)
				}
				p.next.typ = tokenError
				p.next.length = 0
			}
			return
		}
	}

	// Out of source, so hand out end-of-file tokens.
	p.tokenStart = p.currentChar
	p.makeToken(tokenEOF)
}
