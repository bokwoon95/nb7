package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/blugelabs/bluge/index"
	"github.com/bokwoon95/nb7"
)

type FilesystemDirectory struct {
	fs nb7.FS
}

func (dir *FilesystemDirectory) Setup(readOnly bool) error {
	return nil
}

func (dir *FilesystemDirectory) List(kind string) ([]uint64, error) {
	dirEntries, err := dir.fs.ReadDir(".")
	if err != nil {
		return nil, err
	}

	var rv []uint64
	for _, dirEntry := range dirEntries {
		if filepath.Ext(dirEntry.Name()) != kind {
			continue
		}
		base := dirEntry.Name()
		base = base[:len(base)-len(kind)]
		var epoch uint64
		epoch, err = strconv.ParseUint(base, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("error parsing identifier '%s': %w", base, err)
		}
		rv = append(rv, epoch)
	}

	slices.SortFunc(rv, func(a, b uint64) int {
		if a == b {
			return 0
		}
		if a < b {
			return -1
		}
		return 1
	})

	return rv, nil
}

func (dir *FilesystemDirectory) Persist(kind string, id uint64, w index.WriterTo, closeCh chan struct{}) error {
	// path := filepath.Join(dir.path, fmt.Sprintf("%012x", id) + kind)
	// f, err := dir.openExclusive(path, os.O_CREATE|os.O_RDWR, dir.newFilePerm)
	// if err != nil {
	// 	return err
	// }

	// cleanup := func() {
	// 	_ = f.Close()
	// 	_ = os.Remove(path)
	// }

	// _, err = w.WriteTo(f.File(), closeCh)
	// if err != nil {
	// 	cleanup()
	// 	return err
	// }

	// err = f.File().Sync()
	// if err != nil {
	// 	cleanup()
	// 	return err
	// }

	// err = f.Close()
	// if err != nil {
	// 	cleanup()
	// 	return err
	// }

	return nil
}
