package wsclient

import (
	"errors"
	"github.com/gorilla/websocket"
	"io"
	"meerkat/edge/internal/protocol"
	"testing"
)

func TestRevokedOnlyOnExactClose(t *testing.T) {
	c := Client{}
	for _, v := range []struct {
		err     error
		revoked bool
	}{{c.readError(&websocket.CloseError{Code: 4401, Text: "device_revoked"}), true}, {c.readError(&websocket.CloseError{Code: 4401, Text: "auth_failed"}), false}, {c.readError(&websocket.CloseError{Code: 4400, Text: "device_revoked"}), false}, {c.readError(io.EOF), false}, {c.serverError(&protocol.Error{Code: "device_revoked"}), false}} {
		var f *FatalError
		got := errors.As(v.err, &f) && f.DeviceRevoked
		if got != v.revoked {
			t.Fatalf("%v: revoked=%v", v.err, got)
		}
	}
}
