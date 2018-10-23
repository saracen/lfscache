package cache

import (
	"io"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrentReadWriter(t *testing.T) {
	f, err := ioutil.TempFile("", "")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	defer f.Close()

	crw := NewConcurrentReadWriter(f)

	read := func(size int64, expected []byte, offset int64, closed bool) {
		r := crw.Reader()
		defer r.Close()

		if closed {
			r.Close()
		}

		p := make([]byte, size)

		var n int
		var err error
		if offset == 0 {
			n, err = r.Read(p)
		} else {
			n, err = r.ReadAt(p, offset)
		}

		if closed && err == io.EOF {
			return
		}
		if err != io.EOF && assert.NoError(t, err) {
			return
		}

		assert.Equal(t, p[:n], expected)
	}

	go read(10, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 0, false) // read from start
	go read(5, []byte{100, 101, 102, 103, 104}, 100, false)     // read from middle
	go read(5, []byte{100, 101, 102, 103, 104}, 0, true)        // read from middle with reader closed
	go read(5, []byte{254, 255}, 254, false)                    // read at end and request more data than available
	go read(10, []byte{}, 300, false)                           // read past end

	for i := 0; i < 256; i++ {
		time.Sleep(time.Millisecond)
		crw.Write([]byte{byte(i)})
	}
	crw.Close()
}
