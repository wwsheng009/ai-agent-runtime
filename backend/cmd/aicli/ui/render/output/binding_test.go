package output

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestBindingRebindWaitsForInFlightOldGeneration：rebind 必须等旧 generation
// 的在途提交完成（submission 线性化），且旧 facade 随后被 fence。
func TestBindingRebindWaitsForInFlightOldGeneration(t *testing.T) {
	registry := NewSessionBindingRegistry()
	port := &blockingSubmitPort{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	old := registry.Bind("ses-linearized", port)

	submitDone := make(chan struct{})
	go func() {
		defer close(submitDone)
		old.Port.Submit(context.Background(), RenderIntent{
			Kind:  TransactionFrame,
			Bytes: []byte("old"),
		})
	}()
	select {
	case <-port.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("old submission did not enter underlying port")
	}

	rebindDone := make(chan struct{})
	go func() {
		registry.Bind("ses-linearized", port)
		close(rebindDone)
	}()
	select {
	case <-rebindDone:
		t.Fatal("rebind returned while an old-generation submission was still active")
	case <-time.After(25 * time.Millisecond):
	}

	close(port.release)
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old submission did not finish")
	}
	select {
	case <-rebindDone:
	case <-time.After(2 * time.Second):
		t.Fatal("rebind did not finish after old submission returned")
	}

	late := old.Port.Submit(context.Background(), RenderIntent{
		Kind:  TransactionFrame,
		Bytes: []byte("late"),
	})
	if late.Admission.Decision != AdmissionRejected ||
		late.Admission.ErrorClass != DeliveryErrorClosed {
		t.Fatalf("late old-generation submit was not fenced: %+v", late.Admission)
	}
}

type blockingSubmitPort struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingSubmitPort) Submit(context.Context, RenderIntent) OutputReceipt {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return OutputReceipt{
		Admission: AdmissionReceipt{Decision: AdmissionAccepted},
	}
}