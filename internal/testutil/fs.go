package testutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"testing/fstest"
	"time"
)

type TestFS struct {
	mu    sync.RWMutex
	mapFS fstest.MapFS
}

func NewFS(mapFS fstest.MapFS) *TestFS {
	testFS := &TestFS{
		mapFS: make(fstest.MapFS),
	}
	for name, mapFile := range mapFS {
		testFS.mapFS[name] = &fstest.MapFile{
			Data:    slices.Clone(mapFile.Data),
			Mode:    mapFile.Mode,
			ModTime: mapFile.ModTime,
			Sys:     mapFile.Sys,
		}
	}
	return testFS
}

func (testFS *TestFS) Open(name string) (fs.File, error) {
	testFS.mu.RLock()
	defer testFS.mu.RUnlock()
	return testFS.mapFS.Open(name)
}

func (testFS *TestFS) OpenReaderFrom(name string, perm fs.FileMode) (io.ReaderFrom, error) {
	return &testFile{
		testFS: testFS,
		name:   name,
		perm:   perm,
		buffer: &bytes.Buffer{},
	}, nil
}

func (testFS *TestFS) ReadDir(name string) ([]fs.DirEntry, error) {
	testFS.mu.RLock()
	defer testFS.mu.RUnlock()
	return testFS.mapFS.ReadDir(name)
}

func (testFS *TestFS) Mkdir(name string, perm fs.FileMode) error {
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrInvalid}
	}
	_, err := fs.Stat(testFS, path.Dir(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("parent directory does not exist")
		}
		return err
	}
	testFS.mu.Lock()
	defer testFS.mu.Unlock()
	testFS.mapFS[name] = &fstest.MapFile{
		Mode:    perm | fs.ModeDir,
		ModTime: time.Now(),
	}
	return nil
}

func (testFS *TestFS) Remove(name string) error {
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrInvalid}
	}
	fileInfo, err := fs.Stat(testFS, name)
	if err != nil {
		return err
	}
	if fileInfo.IsDir() {
		dirEntries, err := testFS.ReadDir(name)
		if err != nil {
			return err
		}
		if len(dirEntries) > 0 {
			return fmt.Errorf("directory not empty")
		}
	}
	testFS.mu.Lock()
	defer testFS.mu.Unlock()
	delete(testFS.mapFS, name)
	return nil
}

func (testFS *TestFS) Rename(oldname, newname string) error {
	if !fs.ValidPath(oldname) {
		return &fs.PathError{Op: "rename", Path: oldname, Err: fs.ErrInvalid}
	}
	if !fs.ValidPath(newname) {
		return &fs.PathError{Op: "rename", Path: newname, Err: fs.ErrInvalid}
	}
	oldFileInfo, err := fs.Stat(testFS, oldname)
	if err != nil {
		return err
	}
	newFileInfo, err := fs.Stat(testFS, newname)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if newFileInfo != nil && newFileInfo.IsDir() {
		return fmt.Errorf("cannot rename to %[1]q to %[2]q: %[2]q already exists and is a directory", oldname, newname)
	}
	testFS.mu.Lock()
	defer testFS.mu.Unlock()
	testFS.mapFS[newname] = testFS.mapFS[oldname]
	if !oldFileInfo.IsDir() {
		return nil
	}
	dirPrefix := oldname + "/"
	for name, file := range testFS.mapFS {
		if strings.HasPrefix(name, dirPrefix) {
			testFS.mapFS[path.Join(newname, strings.Trim(name, dirPrefix))] = file
		}
	}
	return nil
}

type testFile struct {
	testFS *TestFS
	name   string
	perm   fs.FileMode
	buffer *bytes.Buffer
}

func (testFile *testFile) ReadFrom(r io.Reader) (n int64, err error) {
	testFile.buffer.Reset()
	n, err = testFile.buffer.ReadFrom(r)
	if err != nil {
		return 0, err
	}
	testFile.testFS.mu.Lock()
	defer testFile.testFS.mu.Unlock()
	testFile.testFS.mapFS[testFile.name] = &fstest.MapFile{
		Data:    testFile.buffer.Bytes(),
		ModTime: time.Now(),
		Mode:    testFile.perm &^ fs.ModeDir,
	}
	return n, nil
}
