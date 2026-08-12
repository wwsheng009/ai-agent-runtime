package ui

import (
	"bytes"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

func TestTerminalSessionPresenter_AttachesGeometryAndFlushesFrame(t *testing.T) {
	var output bytes.Buffer
	controller := NewUIController(UIControllerConfig{}, ReducerFunc(func(_ uint64, action UIAction) []Effect {
		if _, ok := action.(DrawRequested); ok {
			return []Effect{FlushEffect{}}
		}
		return nil
	}), nil)
	presenter := NewTerminalSessionPresenter(controller, &output, func() (int, int, bool) {
		return 80, 24, true
	})
	if !presenter.Attach() {
		t.Fatal("presenter attach failed")
	}
	go controller.Run()
	defer func() {
		presenter.Close()
		controller.Close()
		controller.WaitIdle()
	}()

	if !controller.Post(DrawRequested{Key: "initial"}) {
		t.Fatal("failed to post initial draw")
	}
	controller.WaitIdle()
	presenter.WaitIdle()

	state := controller.AppState()
	if state.Geometry.Width != 80 || state.Geometry.Height != 24 || state.LayoutGeneration == 0 {
		t.Fatalf("geometry was not published before flush: %+v", state.Geometry)
	}
	projection := presenter.Session().ProjectionState()
	if projection.Validity != renderengine.ProjectionKnown {
		t.Fatalf("projection validity = %v, want known", projection.Validity)
	}
	if output.Len() == 0 {
		t.Fatal("presenter did not write the initial frame")
	}
}

func TestTerminalSessionPresenter_CloseDetachesConsumer(t *testing.T) {
	controller := NewUIController(UIControllerConfig{}, nil, nil)
	presenter := NewTerminalSessionPresenter(controller, &bytes.Buffer{}, nil)
	if !presenter.Attach() {
		t.Fatal("presenter attach failed")
	}
	presenter.Close()
	if presenter.Attach() {
		t.Fatal("closed presenter reattached its consumer")
	}
	controller.Close()
}

func TestTerminalSessionPresenter_RetriesDeferredGeometryPublication(t *testing.T) {
	controller := NewUIController(UIControllerConfig{MailboxSize: 1}, nil, nil)
	presenter := NewTerminalSessionPresenter(controller, &bytes.Buffer{}, func() (int, int, bool) {
		return 91, 33, true
	})
	if !controller.TryPost(DrawRequested{Key: "mailbox-full"}) {
		t.Fatal("failed to fill controller mailbox")
	}

	// The first probe cannot enqueue Resize and leaves a retry marker. A later
	// presenter request with the same dimensions must retry rather than treating
	// the cached dimensions as already published.
	presenter.publishGeometry()
	go controller.Run()
	controller.WaitIdle()
	presenter.publishGeometry()
	controller.WaitIdle()

	state := controller.AppState()
	if state.Geometry.Width != 91 || state.Geometry.Height != 33 || state.LayoutGeneration == 0 {
		t.Fatalf("deferred geometry was not retried: %+v", state.Geometry)
	}
	presenter.Close()
	controller.Close()
	controller.WaitIdle()
}

func TestTerminalSessionPresenterCloseTimeoutAbortsBlockedWrite(t *testing.T) {
	controller := NewUIController(UIControllerConfig{}, ReducerFunc(func(_ uint64, action UIAction) []Effect {
		if _, ok := action.(DrawRequested); ok {
			return []Effect{FlushEffect{Dirty: renderengine.DirtyContent}}
		}
		return nil
	}), nil)
	writer := newTerminalSessionBlockingWriter()
	presenter := NewTerminalSessionPresenter(controller, writer, func() (int, int, bool) {
		return 80, 24, true
	})
	if !presenter.Attach() {
		t.Fatal("presenter attach failed")
	}
	go controller.Run()
	defer func() {
		writer.unblock()
		presenter.AbortTerminalWrite()
		presenter.CloseTimeout(2 * time.Second)
		controller.Close()
		controller.WaitIdle()
	}()

	if !controller.Post(DrawRequested{Key: "blocked"}) {
		t.Fatal("failed to post blocked draw")
	}
	writer.waitStarted(t, 1)

	// Presenter.CloseTimeout self-aborts the physical writer after its bounded
	// wait, so a blocked write must not turn shutdown into an unbounded wait.
	if !presenter.CloseTimeout(50 * time.Millisecond) {
		t.Fatal("presenter CloseTimeout did not self-abort the blocked writer")
	}
	if got := writer.writeCount(); got != 1 {
		t.Fatalf("write count = %d, want only the abandoned write", got)
	}
}
