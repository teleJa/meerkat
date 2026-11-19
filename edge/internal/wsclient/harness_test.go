package wsclient

import (
	"encoding/json"
	"testing"

	"meerkat/edge/internal/harnessweb"
)

func TestHarnessUpdateMessageCarriesState(t *testing.T) {
	message := harnessUpdateMessage(harnessweb.State{
		Profile:   "bot",
		Mode:      "connect",
		Status:    "running",
		Owner:     "external",
		URL:       "http://127.0.0.1:51901/?token=test",
		Installed: true,
		Profiles:  []harnessweb.Profile{{Name: "bot", Available: true, Running: true}},
	})
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Type string `json:"type"`
		URL  string `json:"url"`
		DSH  struct {
			Profile  string `json:"profile"`
			Mode     string `json:"mode"`
			Status   string `json:"status"`
			Owner    string `json:"owner"`
			Profiles []struct {
				Name    string `json:"name"`
				Running bool   `json:"running"`
			} `json:"profiles"`
		} `json:"dsh"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != "harness.update" || decoded.URL != "http://127.0.0.1:51901/?token=test" {
		t.Fatalf("message = %s", data)
	}
	if decoded.DSH.Profile != "bot" || decoded.DSH.Owner != "external" || len(decoded.DSH.Profiles) != 1 || !decoded.DSH.Profiles[0].Running {
		t.Fatalf("dsh = %+v", decoded.DSH)
	}
}
