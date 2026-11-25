package tsnetedge

import (
	"reflect"
	"testing"
)

func TestIPChangeAfterDelayedLogin(t *testing.T) {
	service := &Service{}
	var changes []string
	service.OnIPChange(func(ip string) {
		changes = append(changes, ip)
		if ip != service.IP() {
			t.Fatal("callback saw stale address")
		}
	})
	service.updateIP("100.64.0.1")
	service.updateIP("100.64.0.1")
	service.updateIP("100.64.0.2")
	if !reflect.DeepEqual(changes, []string{"100.64.0.1", "100.64.0.2"}) {
		t.Fatal(changes)
	}
	service.OnIPChange(func(current string) {
		if current != "100.64.0.2" {
			t.Fatal(current)
		}
	})
}
