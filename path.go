// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Ported from the path helpers of the Wren repository's test/test.c.
// Distributed under the MIT licence; see LICENSE in the repository root.

package main

import (
	"fmt"
	"os"
	"strings"
)

// maxComponents is the largest number of components a path may have. The
// number assumes a maximum path length of 4096, as is common on Linux, and
// that each component is at least two characters: a "/" and one letter.
const maxComponents = 2048

// PathType categorizes what form a path is in.
type PathType int

const (
	// PathTypeAbsolute is an absolute path, which starts with "/".
	PathTypeAbsolute PathType = iota
	// PathTypeRelative is an explicitly relative path, starting with "./" or
	// "../".
	PathTypeRelative
	// PathTypeSimple is a path with no leading prefix, like "foo/bar".
	PathTypeSimple
)

// isSeparator reports whether c separates path components. Only "/" does on
// POSIX systems.
func isSeparator(c byte) bool { return c == '/' }

// absolutePrefixLength returns the length of the prefix of path that makes it
// absolute, which is 1 for a leading "/", or 0 if the path is not absolute.
func absolutePrefixLength(path string) int {
	if len(path) > 0 && isSeparator(path[0]) {
		return 1
	}
	return 0
}

// pathTypeOf categorizes path.
func pathTypeOf(path string) PathType {
	if absolutePrefixLength(path) > 0 {
		return PathTypeAbsolute
	}
	// It may still be explicitly relative.
	if (len(path) >= 2 && path[0] == '.' && isSeparator(path[1])) ||
		(len(path) >= 3 && path[0] == '.' && path[1] == '.' && isSeparator(path[2])) {
		return PathTypeRelative
	}
	// Otherwise we cannot tell.
	return PathTypeSimple
}

// pathDirName strips the last component from path, leaving the directory that
// contains it, or the empty string if there was no separator at all.
func pathDirName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if isSeparator(path[i]) {
			return path[:i]
		}
	}
	return ""
}

// pathRemoveExtension strips every trailing ".<ext>" from the last component
// of path. A separator stops the search, so a directory name containing a dot
// is left alone.
func pathRemoveExtension(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if isSeparator(path[i]) {
			return path
		}
		if path[i] == '.' {
			path = path[:i]
		}
	}
	return path
}

// pathJoin appends a component to path, inserting a separator if needed.
func pathJoin(path, s string) string {
	if len(path) > 0 && !isSeparator(path[len(path)-1]) {
		path += "/"
	}
	return path + s
}

// pathNormalize resolves the "." and ".." components of path while preserving
// whether it is absolute, explicitly relative or simple, because that decides
// how the result is treated as an import string.
func pathNormalize(path string) string {
	type slice struct{ start, end int }
	components := make([]slice, 0, 16)

	start := 0
	end := 0
	leadingDoubles := 0

	byteAt := func(i int) byte {
		if i >= len(path) {
			return 0
		}
		return path[i]
	}

	for {
		if byteAt(end) == 0 || isSeparator(byteAt(end)) {
			if start != end {
				component := path[start:end]
				switch {
				case component == ".":
					// Skip "." components.
				case component == "..":
					// Walk out of a directory on "..".
					if len(components) > 0 {
						components = components[:len(components)-1]
					} else {
						// We cannot back out any further, so keep the "..".
						leadingDoubles++
					}
				default:
					if len(components) >= maxComponents {
						fmt.Fprintf(os.Stderr,
							"Path cannot have more than %d path components.\n", maxComponents)
						os.Exit(1)
					}
					components = append(components, slice{start, end})
				}
			}

			for byteAt(end) != 0 && isSeparator(byteAt(end)) {
				end++
			}
			start = end
			if byteAt(end) == 0 {
				break
			}
			continue
		}
		end++
	}

	var result strings.Builder
	needsSeparator := false

	prefixLength := absolutePrefixLength(path)
	switch {
	case prefixLength > 0:
		// Preserve the absolute prefix.
		result.WriteString(path[:prefixLength])
	case leadingDoubles > 0:
		for i := 0; i < leadingDoubles; i++ {
			if needsSeparator {
				result.WriteByte('/')
			}
			result.WriteString("..")
			needsSeparator = true
		}
	case len(path) >= 2 && path[0] == '.' && isSeparator(path[1]):
		// Preserve a leading "./", which is what distinguishes a relative
		// import from a logical one.
		result.WriteByte('.')
		needsSeparator = true
	}

	for _, component := range components {
		if needsSeparator {
			result.WriteByte('/')
		}
		result.WriteString(path[component.start:component.end])
		needsSeparator = true
	}

	if result.Len() == 0 {
		result.WriteByte('.')
	}
	return result.String()
}
