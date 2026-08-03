package vt

import (
	"reflect"
	"testing"
)

func TestScreenRecordsRowsPushedIntoNativeScrollback(t *testing.T) {
	s := NewScreen(8, 4)
	s.Feed("\x1b[1;1Hone\x1b[2;1Htwo\x1b[3;1Hthree")
	s.Feed("\x1b[1;3r\x1b[3;1H\r\nfour\r\nfive")

	if got, want := s.ScrollbackLines(), []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scrollback = %q, want %q\n%s", got, want, s.Dump())
	}
	if got, want := s.Line(3), "five"; got != want {
		t.Fatalf("region bottom = %q, want %q\n%s", got, want, s.Dump())
	}
}

func TestScreenScrollUpRecordsCommitOrderButSubregionDoesNot(t *testing.T) {
	s := NewScreen(5, 4)
	s.Feed("\x1b[1;1Ha\x1b[2;1Hb\x1b[3;1Hc\x1b[4;1Hd")
	s.Feed("\x1b[1;3r\x1b[2S")
	if got, want := s.ScrollbackLines(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scrollback = %q, want %q", got, want)
	}

	before := append([]string(nil), s.ScrollbackLines()...)
	s.Feed("\x1b[2;3r\x1b[1S")
	if got := s.ScrollbackLines(); !reflect.DeepEqual(got, before) {
		t.Fatalf("subregion scroll entered native scrollback: got %q want %q", got, before)
	}
}

func TestScreenReverseIndexAndScrollDownDoNotPullFromScrollback(t *testing.T) {
	s := NewScreen(6, 3)
	s.Feed("\x1b[1;1Hone\x1b[2;1Htwo\x1b[3;1Hthree")
	s.Feed("\x1b[1;3r\x1b[1S")
	before := append([]string(nil), s.ScrollbackLines()...)

	s.Feed("\x1b[1;1H\x1bM")
	s.Feed("\x1b[1T")
	if got := s.ScrollbackLines(); !reflect.DeepEqual(got, before) {
		t.Fatalf("reverse scroll mutated scrollback: got %q want %q", got, before)
	}
	if got := s.Line(1); got != "" {
		t.Fatalf("reverse scroll must expose a blank row, got %q", got)
	}
}

func TestScreenScrollbackRowsReturnsDeepCopy(t *testing.T) {
	s := NewScreen(4, 2)
	s.Feed("\x1b[2mold\x1b[2;1H\x1b[S")
	rows := s.ScrollbackRows()
	if len(rows) != 1 || rows[0][0].Text != "o" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
	rows[0][0].Text = "changed"
	rows[0][0].SGR[0] = "1"
	again := s.ScrollbackRows()
	if again[0][0].Text != "o" || again[0][0].SGR[0] != "2" {
		t.Fatalf("ScrollbackRows exposed storage: %#v", again[0][0])
	}
}
