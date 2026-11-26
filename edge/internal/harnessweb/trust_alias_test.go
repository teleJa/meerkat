package harnessweb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRetireAliasKeepsDifferentReferences(t *testing.T) {
	full, err := os.ReadFile(filepath.Join("testdata", "legacy-trust-alias", "patch.yml"))
	if err != nil {
		t.Fatal(err)
	}
	split := bytes.LastIndex(full, []byte("\n"+legacyHeading+"\n"))
	if split < 0 {
		t.Fatal("missing managed row")
	}
	block := full[split:]
	base := append([]byte("- id: other\n  config: {days: &other 9}\n"), full[:split]...)
	for _, replacement := range []string{"*other", "9"} {
		manual := bytes.Replace(block, []byte("*days"), []byte(replacement), 1)
		want := append(bytes.Clone(base), manual...)
		if _, err := retireTrustBlock(want, string(block), nil); err == nil {
			t.Fatalf("accepted manual reference %s", replacement)
		}
		after, err := retireTrustBlock(append(bytes.Clone(want), block...), string(block), nil)
		if err != nil || !bytes.Equal(after, want) {
			t.Fatalf("lost manual row %s: %v", replacement, err)
		}
	}
}

func TestSlotAliasCycle(t *testing.T) {
	// Inspect the syntax tree without expanding a recursive reference.
	source := []byte("- config: {value: &loop [*loop]}\n")
	a, err := inspectSlots(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := inspectSlots(source)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSlot(a.rows[0], b.rows[0]) {
		t.Fatal("identical recursive references differed")
	}
}
