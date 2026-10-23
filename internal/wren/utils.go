// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the Wren VM's src/vm/wren_utils.c and src/vm/wren_utils.h.
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

// SymbolTable maps names to small integer symbols by position. Method names
// and module variable names both live in one of these; a symbol is just the
// index at which the name was first added.
type SymbolTable []*ObjString

// Add appends name and returns its index, without checking for duplicates.
func (vm *VM) symbolTableAdd(symbols *SymbolTable, name string) int {
	*symbols = append(*symbols, vm.NewString(name).AsString())
	return len(*symbols) - 1
}

// Ensure returns the index of name, adding it if it is not already there.
func (vm *VM) symbolTableEnsure(symbols *SymbolTable, name string) int {
	if existing := symbols.Find(name); existing != -1 {
		return existing
	}
	return vm.symbolTableAdd(symbols, name)
}

// Find returns the index of name, or -1 if it is not in the table.
func (s *SymbolTable) Find(name string) int {
	for i, symbol := range *s {
		if symbol.Value == name {
			return i
		}
	}
	return -1
}

// Utf8EncodeNumBytes returns the number of bytes needed to encode value in
// UTF-8, or 0 if it is too large.
func Utf8EncodeNumBytes(value int) int {
	switch {
	case value <= 0x7f:
		return 1
	case value <= 0x7ff:
		return 2
	case value <= 0xffff:
		return 3
	case value <= 0x10ffff:
		return 4
	}
	return 0
}

// Utf8Encode writes the UTF-8 encoding of value into bytes and returns how
// many bytes it wrote. Surrogates and other reserved code points are encoded
// as-is, exactly as the original does.
func Utf8Encode(value int, bytes []byte) int {
	switch {
	case value <= 0x7f:
		bytes[0] = byte(value & 0x7f)
		return 1
	case value <= 0x7ff:
		bytes[0] = byte(0xc0 | ((value & 0x7c0) >> 6))
		bytes[1] = byte(0x80 | (value & 0x3f))
		return 2
	case value <= 0xffff:
		bytes[0] = byte(0xe0 | ((value & 0xf000) >> 12))
		bytes[1] = byte(0x80 | ((value & 0xfc0) >> 6))
		bytes[2] = byte(0x80 | (value & 0x3f))
		return 3
	case value <= 0x10ffff:
		bytes[0] = byte(0xf0 | ((value & 0x1c0000) >> 18))
		bytes[1] = byte(0x80 | ((value & 0x3f000) >> 12))
		bytes[2] = byte(0x80 | ((value & 0xfc0) >> 6))
		bytes[3] = byte(0x80 | (value & 0x3f))
		return 4
	}
	return 0
}

// Utf8Decode decodes the UTF-8 sequence starting at the front of s and
// returns its code point, or -1 if the bytes are not a valid sequence. Over-
// long encodings and surrogates are accepted, as in the original.
func Utf8Decode(s string) int {
	if len(s) == 0 {
		return -1
	}
	first := s[0]
	if first <= 0x7f {
		return int(first)
	}

	var value int
	var remainingBytes int
	switch {
	case first&0xe0 == 0xc0:
		value = int(first & 0x1f)
		remainingBytes = 1
	case first&0xf0 == 0xe0:
		value = int(first & 0x0f)
		remainingBytes = 2
	case first&0xf8 == 0xf0:
		value = int(first & 0x07)
		remainingBytes = 3
	default:
		return -1
	}

	// Don't read past the end of the buffer on truncated UTF-8.
	if remainingBytes > len(s)-1 {
		return -1
	}

	for i := 1; i <= remainingBytes; i++ {
		b := s[i]
		if b&0xc0 != 0x80 {
			return -1
		}
		value = value<<6 | int(b&0x3f)
	}
	return value
}

// Utf8DecodeNumBytes returns the length of the UTF-8 sequence that starts with
// b, or 0 if b is a continuation byte.
func Utf8DecodeNumBytes(b byte) int {
	if b&0xc0 == 0x80 {
		return 0
	}
	switch {
	case b&0xf8 == 0xf0:
		return 4
	case b&0xf0 == 0xe0:
		return 3
	case b&0xe0 == 0xc0:
		return 2
	}
	return 1
}

// PowerOf2Ceil returns the smallest power of two that is at least n.
func PowerOf2Ceil(n int) int {
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n++
	return n
}

// ValidateIndex checks that value is in [0, count), allowing negative indices
// that count back from the end. It returns the resulting positive index, or
// -1 if the index is out of bounds.
func ValidateIndex(count int, value int64) int {
	if value < 0 {
		value = int64(count) + value
	}
	if value >= 0 && value < int64(count) {
		return int(value)
	}
	return -1
}
