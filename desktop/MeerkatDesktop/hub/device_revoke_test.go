package hub

import (
	"testing"
	"time"
)

func TestRevokedIdentityRequiresCurrentSecret(t *testing.T) {
	h := testHub(t)
	info := DeviceInfo{Name: "lab", Hostname: "lab", OS: "windows", Arch: "amd64", Role: "node"}
	first, err := h.registerDevice(info, "test", ProtocolVersion, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	current, err := h.registerDevice(info, "test", ProtocolVersion, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.Device.ID != current.Device.ID {
		t.Fatal("expected identity rotation")
	}
	if err := h.revokeDevice(current.Device.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id, secret string
		want       error
	}{{current.Device.ID, current.Secret, errDeviceRevoked}, {current.Device.ID, first.Secret, nil}, {current.Device.ID, "incorrect", nil}, {"unknown", current.Secret, nil}} {
		if dev, err := h.getDeviceBySecret(c.id, c.secret); err != c.want || dev != nil {
			t.Fatalf("revoked identity classification: got %v, want %v", err, c.want)
		}
	}
}
