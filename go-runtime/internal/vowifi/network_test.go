package vowifi

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"
)

type fakeResource struct {
	name   string
	closed *[]string
}

func (resource *fakeResource) Close() error {
	*resource.closed = append(*resource.closed, resource.name)
	return nil
}

type fakePackets struct{ *fakeResource }

func (*fakePackets) ReadPacket(context.Context) (Packet, error) { return Packet{}, nil }
func (*fakePackets) WritePacket(context.Context, Packet) error  { return nil }
func (*fakePackets) NetworkConfig() NetworkConfig               { return NetworkConfig{} }

type fakeNetwork struct{ *fakeResource }

func (*fakeNetwork) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}
func (*fakeNetwork) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	return nil, errors.New("not used")
}
func (*fakeNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, errors.New("not used")
}

type fakeEPDG struct {
	packets PacketSession
	err     error
}

func (connector fakeEPDG) Connect(context.Context, OuterPacketDialer, SIMAuthenticator) (PacketSession, error) {
	return connector.packets, connector.err
}

type fakeNetworkFactory struct {
	network UserNetwork
	err     error
}

func (factory fakeNetworkFactory) Open(context.Context, PacketSession) (UserNetwork, error) {
	return factory.network, factory.err
}

type fakeIMS struct {
	registration IMSRegistration
	err          error
}

func (registrar fakeIMS) Register(context.Context, UserNetwork) (IMSRegistration, error) {
	return registrar.registration, registrar.err
}

type unusedOuter struct{}

func (unusedOuter) DialPacket(context.Context, netip.AddrPort) (net.PacketConn, error) {
	return nil, errors.New("not used")
}

type unusedSIM struct{}

func (unusedSIM) Authenticate(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("not used")
}

func TestSessionClosesIMSStackAndSWuInOrderExactlyOnce(t *testing.T) {
	closed := []string{}
	packets := &fakePackets{&fakeResource{name: "swu", closed: &closed}}
	network := &fakeNetwork{&fakeResource{name: "stack", closed: &closed}}
	registration := &fakeResource{name: "ims", closed: &closed}
	session, err := Open(context.Background(), Dependencies{
		EPDG: fakeEPDG{packets: packets}, Outer: unusedOuter{}, SIM: unusedSIM{},
		Network: fakeNetworkFactory{network: network}, IMS: fakeIMS{registration: registration},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Network() != network {
		t.Fatal("session did not expose its userspace network")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ims", "stack", "swu"}; !reflect.DeepEqual(closed, want) {
		t.Fatalf("close order = %v, want %v", closed, want)
	}
}

func TestIMSFailureDoesNotRestartOrLeakLowerLayers(t *testing.T) {
	closed := []string{}
	packets := &fakePackets{&fakeResource{name: "swu", closed: &closed}}
	network := &fakeNetwork{&fakeResource{name: "stack", closed: &closed}}
	_, err := Open(context.Background(), Dependencies{
		EPDG: fakeEPDG{packets: packets}, Outer: unusedOuter{}, SIM: unusedSIM{},
		Network: fakeNetworkFactory{network: network}, IMS: fakeIMS{err: errors.New("registration rejected")},
	})
	var stageError *StageError
	if !errors.As(err, &stageError) || stageError.Stage != StageIMS {
		t.Fatalf("error = %v", err)
	}
	if want := []string{"stack", "swu"}; !reflect.DeepEqual(closed, want) {
		t.Fatalf("close order = %v, want %v", closed, want)
	}
}

func TestStackFailureClosesOnlySWu(t *testing.T) {
	closed := []string{}
	packets := &fakePackets{&fakeResource{name: "swu", closed: &closed}}
	_, err := Open(context.Background(), Dependencies{
		EPDG: fakeEPDG{packets: packets}, Outer: unusedOuter{}, SIM: unusedSIM{},
		Network: fakeNetworkFactory{err: errors.New("stack failed")}, IMS: fakeIMS{},
	})
	var stageError *StageError
	if !errors.As(err, &stageError) || stageError.Stage != StageUserStack {
		t.Fatalf("error = %v", err)
	}
	if want := []string{"swu"}; !reflect.DeepEqual(closed, want) {
		t.Fatalf("closed = %v, want %v", closed, want)
	}
}

func TestNilProviderResourceIsRejectedAndLowerLayersClose(t *testing.T) {
	closed := []string{}
	packets := &fakePackets{&fakeResource{name: "swu", closed: &closed}}
	network := &fakeNetwork{&fakeResource{name: "stack", closed: &closed}}
	_, err := Open(context.Background(), Dependencies{
		EPDG: fakeEPDG{packets: packets}, Outer: unusedOuter{}, SIM: unusedSIM{},
		Network: fakeNetworkFactory{network: network}, IMS: fakeIMS{},
	})
	var stageError *StageError
	if !errors.As(err, &stageError) || stageError.Stage != StageIMS {
		t.Fatalf("error = %v", err)
	}
	if want := []string{"stack", "swu"}; !reflect.DeepEqual(closed, want) {
		t.Fatalf("closed = %v, want %v", closed, want)
	}
}
