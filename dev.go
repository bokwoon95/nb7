//go:build dev
// +build dev

package nb7

import (
	"os"
)

func init() {
	rootFS = os.DirFS(".")
}
