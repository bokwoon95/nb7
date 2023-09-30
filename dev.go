//go:build dev
// +build dev

package nb7

import (
	"os"
)

func init() {
	rootFS = os.DirFS(".")
	readTimeout = 0
	writeTimeout = 0
	idleTimeout = 0
}
