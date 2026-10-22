package middleware

import (
	"errors"
	"testing"
)

type closeRecorder struct {
	order *[]string
	name  string
	err   error
}

func (w *closeRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (w *closeRecorder) Close() error                { *w.order = append(*w.order, w.name); return w.err }

func TestWrapWriterFinalizesBeforeClosingBackend(t *testing.T) {
	var order []string
	finalizeErr, backendErr := errors.New("finalize failed"), errors.New("backend close failed")
	writer := &WrapWriter{
		FileWriter: &closeRecorder{order: &order, name: "backend", err: backendErr},
		w:          &closeRecorder{order: &order, name: "gzip", err: finalizeErr},
	}
	err := writer.Close()
	if len(order) != 2 || order[0] != "gzip" || order[1] != "backend" {
		t.Fatalf("close order=%v", order)
	}
	if !errors.Is(err, finalizeErr) || !errors.Is(err, backendErr) {
		t.Fatalf("error=%v", err)
	}
}
