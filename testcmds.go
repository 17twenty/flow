//go:build !flowtest

package main

import "github.com/alecthomas/kong"

// testCommands registers nothing in release builds: the shipped CLI surface is
// exactly what spec 02 documents.
func testCommands() []kong.Option { return nil }
