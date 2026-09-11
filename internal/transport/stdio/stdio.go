package stdio

import (
	"io"
	"sync"
)

type Endpoint struct {
	r        io.Reader
	w        io.Writer
	closeFn  func() error
	closeOne sync.Once
	closeErr error
}

func New(r io.Reader, w io.Writer, closeFn func() error) *Endpoint {
	if closeFn == nil {
		closeFn = func() error { return nil }
	}
	return &Endpoint{r: r, w: w, closeFn: closeFn}
}

func (e *Endpoint) Read(p []byte) (int, error)  { return e.r.Read(p) }
func (e *Endpoint) Write(p []byte) (int, error) { return e.w.Write(p) }

func (e *Endpoint) Close() error {
	e.closeOne.Do(func() { e.closeErr = e.closeFn() })
	return e.closeErr
}
