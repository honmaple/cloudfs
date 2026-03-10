package ioutil

import (
	"io"
	"sync"
)

func Pipe() (*reader, *writer) {
	r, w := io.Pipe()

	wg := &sync.WaitGroup{}
	wg.Add(2)

	return &reader{PipeReader: r, wg: wg}, &writer{PipeWriter: w, wg: wg}
}

type writer struct {
	*io.PipeWriter
	wg   *sync.WaitGroup
	once sync.Once
}

func (w *writer) done() {
	w.wg.Done()
	w.wg.Wait()
}

func (w *writer) Close() error {
	err := w.PipeWriter.Close()
	w.once.Do(w.done)
	return err
}

func (w *writer) CloseWithError(err error) error {
	err = w.PipeWriter.CloseWithError(err)
	w.once.Do(w.done)
	return err
}

type reader struct {
	*io.PipeReader
	wg   *sync.WaitGroup
	once sync.Once
}

func (r *reader) done() {
	r.wg.Done()
	r.wg.Wait()
}

func (r *reader) Close() error {
	err := r.PipeReader.Close()
	r.once.Do(r.done)
	return err
}

func (r *reader) CloseWithError(err error) error {
	err = r.PipeReader.CloseWithError(err)
	r.once.Do(r.done)
	return err
}
