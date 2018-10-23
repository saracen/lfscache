package cache

import (
	"io"
	"sync"
)

// ReadAtWriteCloser is the interface that groups the basic ReadAt, Write and
// Close methods.
type ReadAtWriteCloser interface {
	io.ReaderAt
	io.WriteCloser
}

// ReadAtReadCloser is the interface that groups the basic ReadAt, Read and
// Close methods.
type ReadAtReadCloser interface {
	io.ReaderAt
	io.ReadCloser
}

// ConcurrentReadWriter wraps a ReadAtWriteCloser (such as os.File) and allows
// multiple readers to stream data as it is being written.
type ConcurrentReadWriter struct {
	r ReadAtWriteCloser

	lock   sync.Mutex
	wake   *sync.Cond
	wg     sync.WaitGroup
	closed bool
}

// NewConcurrentReadWriter returns a new ConcurrentReadWriter.
func NewConcurrentReadWriter(r ReadAtWriteCloser) *ConcurrentReadWriter {
	crw := &ConcurrentReadWriter{r: r}
	crw.wake = sync.NewCond(&crw.lock)
	return crw
}

// Close closes the underlying read/writer, but blocks until all readers
// have been closed.
func (crw *ConcurrentReadWriter) Close() error {
	crw.lock.Lock()
	crw.closed = true
	crw.lock.Unlock()

	// wake readers
	crw.wake.Broadcast()

	// wait for all readers to close before closing underlying read/writer.
	crw.wg.Wait()

	return crw.r.Close()
}

// Closed returns whether or not the ConcurrentReadWriter has been closed.
func (crw *ConcurrentReadWriter) Closed() bool {
	crw.lock.Lock()
	defer crw.lock.Unlock()

	return crw.closed
}

// Write implements the standard Write interface.
func (crw *ConcurrentReadWriter) Write(p []byte) (n int, err error) {
	n, err = crw.r.Write(p)
	crw.wake.Broadcast()

	return
}

func (crw *ConcurrentReadWriter) wait() {
	crw.lock.Lock()
	defer crw.lock.Unlock()

	crw.wake.Wait()
}

// Reader returns an io.Reader that can be used to read data as it is being
// written. The Read() method will return EOF only when all data has been read
// and Close() has been called, otherwise it will block.
//
// Nil will be returned if the ConcurrentReadWriter has been closed.
func (crw *ConcurrentReadWriter) Reader() ReadAtReadCloser {
	if crw.Closed() {
		return nil
	}

	crw.wg.Add(1)
	return &reader{crw: crw}
}

type reader struct {
	lock   sync.RWMutex
	crw    *ConcurrentReadWriter
	offset int64
	closed bool
}

func (r *reader) Read(p []byte) (n int, err error) {
	r.lock.RLock()
	offset := r.offset
	r.lock.RUnlock()

	n, err = r.ReadAt(p, offset)

	r.lock.Lock()
	r.offset += int64(n)
	r.lock.Unlock()

	return
}

func (r *reader) ReadAt(p []byte, off int64) (n int, err error) {
	var read int
	for {
		r.lock.RLock()
		readerClosed := r.closed
		r.lock.RUnlock()

		if readerClosed {
			return 0, io.EOF
		}

		read, err = r.crw.r.ReadAt(p, off+int64(n))
		n += read
		s := len(p)
		p = p[read:]

		// fill scratch buffer until we EOF
		if err == nil && len(p) != s {
			continue
		}

		// on EOF wait to for additional data if read/writer hasn't been closed
		if err == io.EOF {
			if !r.crw.Closed() {
				r.crw.wait()
				continue
			}
		}
		return
	}
}

func (r *reader) Close() error {
	r.lock.Lock()
	if r.closed {
		r.lock.Unlock()
		return nil
	}
	r.closed = true
	r.lock.Unlock()

	r.crw.wg.Done()
	return nil
}
