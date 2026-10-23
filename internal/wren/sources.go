// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import _ "embed"

// coreModuleSource is the part of the core library that is written in Wren
// itself: Sequence and its subclasses, String, List, Map, Range, System and
// ClassAttributes. It is interpreted while the VM is being created.
//
//go:embed core.wren
var coreModuleSource string

// metaModuleSource is the source of the optional "meta" module.
//
//go:embed opt_meta.wren
var metaModuleSource string

// randomModuleSource is the source of the optional "random" module.
//
//go:embed opt_random.wren
var randomModuleSource string
