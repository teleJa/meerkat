package androidclient

import "testing"

func TestHubDraftSurvivesRestartWithoutRebindingToken(t *testing.T) {
	dir, store := t.TempDir(), newFakeSecureStore()
	active := Auth{Kind: AuthKindAdmin, Token: "test-token", HubURL: "http://a.example:7443"}
	if err := SaveAuth(store, active); err != nil {
		t.Fatal(err)
	}
	client, err := New(dir, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.logFile.Close()
	if _, err = client.SaveHubDraft("b.example:7443"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.SaveHubDraft("https://c.example:7443/path"); err == nil {
		t.Fatal("accepted malformed address")
	}
	restored, err := New(dir, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.logFile.Close()
	auth, err := restored.Auth()
	if err != nil || auth.HubURL != active.HubURL || auth.Token != active.Token || restored.Config().HubURL != active.HubURL || restored.Snapshot().DraftHubAddr != "b.example:7443" {
		t.Fatal("draft or binding was not restored independently")
	}
}
