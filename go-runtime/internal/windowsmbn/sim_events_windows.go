//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/foundation"
	mbn "github.com/deploymenttheory/go-bindings-win32/bindings/win32/networkmanagement/mobilebroadband"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/com"
)

type simEventEpochs struct {
	mu        sync.RWMutex
	epochs    map[string]uint64
	available atomic.Bool
	close     chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newSIMEventEpochs() *simEventEpochs {
	events := &simEventEpochs{epochs: map[string]uint64{}, close: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan bool, 1)
	go events.run(ready)
	select {
	case available := <-ready:
		events.available.Store(available)
	case <-time.After(5 * time.Second):
	}
	return events
}

func (events *simEventEpochs) Epoch(interfaceID string) (uint64, bool) {
	if events == nil || !events.available.Load() {
		return 0, false
	}
	events.mu.RLock()
	epoch := events.epochs[strings.ToLower(strings.TrimSpace(interfaceID))]
	events.mu.RUnlock()
	return epoch, true
}

func (events *simEventEpochs) mark(current *mbn.IMbnInterface) {
	if events == nil || current == nil {
		return
	}
	var value foundation.BSTR
	if err := current.Get_InterfaceID(&value); err != nil {
		if value != nil {
			takeBSTR(value)
		}
		return
	}
	interfaceID := strings.ToLower(strings.TrimSpace(takeBSTR(value)))
	events.record(interfaceID)
}

func (events *simEventEpochs) record(interfaceID string) {
	interfaceID = strings.ToLower(strings.TrimSpace(interfaceID))
	if interfaceID == "" {
		return
	}
	events.mu.Lock()
	events.epochs[interfaceID]++
	events.mu.Unlock()
}

func (events *simEventEpochs) Close() error {
	if events == nil {
		return nil
	}
	events.closeOnce.Do(func() { close(events.close) })
	select {
	case <-events.done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("Windows MBN SIM event watcher did not stop")
	}
}

type interfaceEventSink struct {
	com.IUnknown
	refs   atomic.Int32
	events *simEventEpochs
}

var interfaceEventVTable = [11]uintptr{
	syscall.NewCallback(interfaceEventQueryInterface), syscall.NewCallback(interfaceEventAddRef),
	syscall.NewCallback(interfaceEventRelease), syscall.NewCallback(interfaceEventIgnored),
	syscall.NewCallback(interfaceEventChanged), syscall.NewCallback(interfaceEventChanged),
	syscall.NewCallback(interfaceEventIgnored), syscall.NewCallback(interfaceEventIgnored),
	syscall.NewCallback(interfaceEventIgnored), syscall.NewCallback(interfaceEventIgnoredComplete),
	syscall.NewCallback(interfaceEventIgnoredComplete),
}

func newInterfaceEventSink(events *simEventEpochs) *interfaceEventSink {
	sink := &interfaceEventSink{events: events}
	sink.IUnknown.LpVtbl = (*[1024]uintptr)(unsafe.Pointer(&interfaceEventVTable))
	sink.refs.Store(1)
	return sink
}

func interfaceEventQueryInterface(this *com.IUnknown, iid *win32.GUID, output **com.IUnknown) uintptr {
	if output == nil || iid == nil {
		return interfaceEventHRESULT(win32.E_POINTER)
	}
	*output = nil
	if *iid != com.IID_IUnknown && *iid != mbn.IID_IMbnInterfaceEvents {
		return interfaceEventHRESULT(win32.E_NOINTERFACE)
	}
	*output = this
	interfaceEventAddRef(this)
	return interfaceEventHRESULT(win32.S_OK)
}

func interfaceEventAddRef(this *com.IUnknown) uintptr {
	return uintptr((*interfaceEventSink)(unsafe.Pointer(this)).refs.Add(1))
}

func interfaceEventRelease(this *com.IUnknown) uintptr {
	return uintptr((*interfaceEventSink)(unsafe.Pointer(this)).refs.Add(-1))
}

func interfaceEventIgnored(*com.IUnknown, *mbn.IMbnInterface) uintptr {
	return interfaceEventHRESULT(win32.S_OK)
}

func interfaceEventIgnoredComplete(*com.IUnknown, *mbn.IMbnInterface, uint32, foundation.HRESULT) uintptr {
	return interfaceEventHRESULT(win32.S_OK)
}

func interfaceEventChanged(this *com.IUnknown, current *mbn.IMbnInterface) uintptr {
	(*interfaceEventSink)(unsafe.Pointer(this)).events.mark(current)
	return interfaceEventHRESULT(win32.S_OK)
}

func interfaceEventHRESULT(value win32.HRESULT) uintptr { return uintptr(uint32(int32(value))) }

func (events *simEventEpochs) run(ready chan<- bool) {
	defer close(events.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := com.CoInitializeEx(uint32(com.COINIT_MULTITHREADED)); err != nil {
		ready <- false
		return
	}
	defer com.CoUninitialize()
	var root *win32.IUnknown
	if err := com.CoCreateInstance(&clsidMbnInterfaceManager, nil, com.CLSCTX_INPROC_SERVER,
		&mbn.IID_IMbnInterfaceManager, &root); err != nil || root == nil {
		if root != nil {
			root.Release()
		}
		ready <- false
		return
	}
	defer root.Release()
	container, err := win32.QueryInterface[com.IConnectionPointContainer](root, &com.IID_IConnectionPointContainer)
	if err != nil {
		ready <- false
		return
	}
	defer container.Release()
	var point *com.IConnectionPoint
	if err := container.FindConnectionPoint(&mbn.IID_IMbnInterfaceEvents, &point); err != nil || point == nil {
		if point != nil {
			point.Release()
		}
		ready <- false
		return
	}
	defer point.Release()
	sink := newInterfaceEventSink(events)
	var cookie uint32
	if err := point.Advise(&sink.IUnknown, &cookie); err != nil || cookie == 0 {
		ready <- false
		return
	}
	ready <- true
	<-events.close
	_ = point.Unadvise(cookie)
	runtime.KeepAlive(sink)
}
