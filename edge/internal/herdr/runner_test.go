package herdr

import "testing"

func TestExtractAnswer(t *testing.T) {
	screen := `
 ▐▛███▛█   Claude Code v2.1.319
❯ 请只回复：HERDR_EDGE_OK

⏺ HERDR_EDGE_OK

✻ Worked for 4s · done 1:43 PM
───────────────────────────────────────────────────────
❯
`
	if got := extractAnswer(screen); got != "HERDR_EDGE_OK" {
		t.Fatalf("extractAnswer = %q", got)
	}
	if got := extractAnswer("no assistant marker"); got != "" {
		t.Fatalf("empty marker = %q", got)
	}
}

func TestExtractAnswerMultiline(t *testing.T) {
	screen := `
❯ 请写一段介绍
⏺ 第一行介绍
  第二行介绍
✻ Worked for 2s · done
`
	got := extractAnswer(screen)
	want := "第一行介绍\n第二行介绍"
	if got != want {
		t.Fatalf("extractAnswer = %q, want %q", got, want)
	}
}
