// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import "strconv"

// FormatG14 renders a finite double the way the C implementation's
// `sprintf(buffer, "%.14g", value)` does: fourteen significant digits with
// trailing zeros removed, switching to exponent form when the decimal
// exponent is below -4 or at least 14, and with the exponent itself padded to
// at least two digits.
func FormatG14(value float64) string {
	return strconv.FormatFloat(value, 'g', 14, 64)
}
