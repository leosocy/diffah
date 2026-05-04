package exporter

import (
	"context"

	"github.com/leosocy/diffah/internal/admission"
)

// workerPool is a thin forwarder over admission.WorkerPool kept for
// historical naming. All semantics live in internal/admission.
type workerPool = admission.WorkerPool

func newWorkerPool(ctx context.Context, n int) (*workerPool, context.Context) {
	return admission.NewWorkerPool(ctx, n)
}
