package cache

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// ErrKeyNotFound is returned when a cache key cannot be found.
var ErrKeyNotFound = errors.New("key not found")

// Source indicates the source of the cached content.
type Source string

// Sources that indicate where a cache is being served from:
// - SourceDisk (local disk cache)
// - SourceInflight (inflight cache)
// - SourceFresh (non-cached, fresh data)
const (
	SourceDisk     Source = "disk"
	SourceInflight Source = "inflight"
	SourceFresh    Source = "fresh"
)

// Subdirectories for storing objects.
const (
	DirObjects = "objects"
	DirTemp    = "tmp"
)

// FilesystemCache caches files to disk.
type FilesystemCache struct {
	lock         sync.RWMutex
	singleflight map[string]fileConcurrentReadWriter
	directory    string

	Filenamer func(key string) string
}

type fileConcurrentReadWriter struct {
	f    *os.File
	crw  *ConcurrentReadWriter
	dest string
}

// DefaultFilenamer is the default filenamer used when naming a cached file on
// disk.
func DefaultFilenamer(key string) string {
	if len(key) < 4 {
		return key
	}

	return filepath.Join(key[0:2], key[2:4], key)
}

// NewFilesystemCache returns a new FilesystemCache.
func NewFilesystemCache(directory string) (*FilesystemCache, error) {
	if err := os.MkdirAll(filepath.Join(directory, DirObjects), 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(directory, DirTemp), 0700); err != nil {
		return nil, err
	}

	return &FilesystemCache{
		singleflight: make(map[string]fileConcurrentReadWriter),
		directory:    directory,
		Filenamer:    DefaultFilenamer,
	}, nil
}

// Directory returns the cache directory.
func (fc *FilesystemCache) Directory() string {
	return fc.directory
}

// Get returns:
// - A reader so that data can be read from the cache.
// - A writer if the cache doesn't yet exist so that it can be populated.
// - The source of the cache (disk, inflight, or fresh)
//
// A reader can be read from whilst the writer is being written to. The readers
// will only EOF if the writer or reader is closed.
//
// A writer can only be closed if all readers have been closed.
func (fc *FilesystemCache) Get(key string) (ReadAtReadCloser, io.WriteCloser, Source, error) {
	filename := filepath.Join(fc.directory, DirObjects, fc.Filenamer(key))
	f, err := os.Open(filename)
	if err == nil {
		return f, nil, SourceDisk, nil
	}

	fc.lock.Lock()
	defer fc.lock.Unlock()

	singleflight, ok := fc.singleflight[key]
	if ok {
		return singleflight.crw.Reader(), nil, SourceInflight, nil
	}

	f, err = os.Create(filepath.Join(fc.directory, DirTemp, key))
	if err != nil {
		return nil, nil, SourceFresh, err
	}

	crw := NewConcurrentReadWriter(f)
	fc.singleflight[key] = fileConcurrentReadWriter{
		f:    f,
		crw:  crw,
		dest: filename,
	}

	return crw.Reader(), crw, SourceFresh, nil
}

// Done indicates that we're done with a certain cache key.
//
// If an error is passed, the cache is deleted, otherwise the cache file is
// moved to the cache directory.
func (fc *FilesystemCache) Done(key string, err error) error {
	fc.lock.Lock()
	defer fc.lock.Unlock()

	singleflight, ok := fc.singleflight[key]

	if !ok {
		return ErrKeyNotFound
	}
	delete(fc.singleflight, key)

	// ensure crw is closed
	if err := singleflight.crw.Close(); err != nil {
		return err
	}

	// remove backing file if there was an error
	if err != nil {
		return os.Remove(singleflight.f.Name())
	}

	// rename backing file on success
	if err := os.MkdirAll(filepath.Dir(singleflight.dest), 0700); err != nil {
		return err
	}
	return os.Rename(singleflight.f.Name(), singleflight.dest)
}
