//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"testing"

	mbn "github.com/deploymenttheory/go-bindings-win32/bindings/win32/networkmanagement/mobilebroadband"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/com"
)

func TestSIMEventEpochsAreMonotonicAndInterfaceScoped(t *testing.T) {
	events := &simEventEpochs{epochs: map[string]uint64{}}
	events.available.Store(true)
	events.record(" {INTERFACE-A} ")
	events.record("{interface-a}")
	events.record("{interface-b}")
	first, available := events.Epoch("{INTERFACE-A}")
	second, secondAvailable := events.Epoch("{interface-b}")
	if !available || !secondAvailable || first != 2 || second != 1 {
		t.Fatalf("first=%d available=%v second=%d available=%v", first, available, second, secondAvailable)
	}
}

func TestUnavailableSIMEventSourceNeverReturnsStableEpoch(t *testing.T) {
	events := &simEventEpochs{epochs: map[string]uint64{}}
	events.record("{interface-a}")
	if epoch, available := events.Epoch("{interface-a}"); available || epoch != 0 {
		t.Fatalf("epoch=%d available=%v", epoch, available)
	}
}

func TestInterfaceEventSinkExposesOnlyIUnknownAndMbnEvents(t *testing.T) {
	events := &simEventEpochs{epochs: map[string]uint64{}}
	sink := newInterfaceEventSink(events)
	var output *com.IUnknown
	if result := interfaceEventQueryInterface(&sink.IUnknown, &mbn.IID_IMbnInterfaceEvents, &output); result != 0 || output != &sink.IUnknown {
		t.Fatalf("MBN interface result=%x output=%p", result, output)
	}
	unknownIID := mbn.IID_IMbnInterfaceEvents
	unknownIID.Data1++
	output = &sink.IUnknown
	if result := interfaceEventQueryInterface(&sink.IUnknown, &unknownIID, &output); uint32(result) != 0x80004002 || output != nil {
		t.Fatalf("unknown interface result=%x output=%p", result, output)
	}
}
