package cellulario

import "testing"

func TestAttachmentIdentitySeparatesStableDeviceAndUSBGeneration(t *testing.T) {
	first := Attachment{VID: 0x2c7c, PID: 0x0125, Bus: 1, Address: 4, Serial: "EC20-A"}
	replugged := first
	replugged.Address = 9
	if first.PhysicalID() != replugged.PhysicalID() {
		t.Fatalf("stable serial identity changed across replug: %q != %q", first.PhysicalID(), replugged.PhysicalID())
	}
	if first.Generation() == replugged.Generation() || first.ID() == replugged.ID() {
		t.Fatal("USB replug did not create a new attachment generation")
	}
	withoutSerial := Attachment{VID: 0x2c7c, PID: 0x0125, Bus: 1, Address: 4}
	if withoutSerial.PhysicalID() == first.PhysicalID() || withoutSerial.ID() == "" {
		t.Fatalf("serial-less attachment identity=%q id=%q", withoutSerial.PhysicalID(), withoutSerial.ID())
	}
}
