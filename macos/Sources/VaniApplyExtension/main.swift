// The process entry point is Foundation's NSExtensionMain (linker flag in
// Package.swift), which never runs this file. The reference below keeps the
// principal class in the binary under dead-code stripping.
_ = SafariWebExtensionHandler.self
