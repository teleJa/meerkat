package cli

import (
	"reflect"
	"testing"
)

func TestEnsureHarnessArg(t *testing.T) {
	got := ensureHarnessArg([]string{"--settings", "x"}, "--dangerously-skip-permissions")
	want := []string{"--settings", "x", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ensureHarnessArg = %v, want %v", got, want)
	}
	got = ensureHarnessArg([]string{"--dangerously-skip-permissions"}, "--dangerously-skip-permissions")
	if !reflect.DeepEqual(got, []string{"--dangerously-skip-permissions"}) {
		t.Fatalf("重复参数未去重: %v", got)
	}
}
