package main

import (
	"embed"

	_ "modernc.org/sqlite"
)

//go:embed web
var testWeb embed.FS

func testWebFS() embed.FS { return testWeb }
