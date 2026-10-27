package engine

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestHTMLBodyDecodesWindows1251FromHeader(t *testing.T) {
	raw := []byte("<a>\xcc\xe0\xf2\xf0\xe8\xf6\xe0 \xa8\xb8 \xb9</a>")
	got := htmlBody(&Response{Header: http.Header{"Content-Type": {"text/html; charset=windows-1251"}}, Body: raw})
	if want := "<a>Матрица Ёё №</a>"; string(got) != want {
		t.Fatalf("htmlBody = %q, want %q", got, want)
	}
}

func TestHTMLBodyDecodesWindows1251FromMeta(t *testing.T) {
	raw := []byte(`<html><head><meta http-equiv="Content-Type" content="text/html; charset=windows-1251"></head><b>` + "\xcf\xf0\xe8\xe2\xe5\xf2" + `</b>`)
	got := htmlBody(&Response{Header: http.Header{"Content-Type": {"text/html"}}, Body: raw})
	if !strings.Contains(string(got), "<b>Привет</b>") {
		t.Fatalf("htmlBody = %q", got)
	}
}

func TestHTMLBodyKeepsUTF8(t *testing.T) {
	// FlareSolverr returns the page already decoded but keeps the original
	// header: the body must not be decoded a second time.
	raw := []byte("<a>Матрица</a>")
	got := htmlBody(&Response{Header: http.Header{"Content-Type": {"text/html; charset=windows-1251"}}, Body: raw})
	if string(got) != string(raw) {
		t.Fatalf("htmlBody = %q, want unchanged", got)
	}
}

func TestHTMLBodyLatin1(t *testing.T) {
	got := htmlBody(&Response{Header: http.Header{"Content-Type": {"text/html; charset=ISO-8859-1"}}, Body: []byte("Pel\xedculas")})
	if string(got) != "Películas" {
		t.Fatalf("htmlBody = %q", got)
	}
}

func TestCP1251TableMatchesCodePage(t *testing.T) {
	// Code points of 0x80–0xFF in the public Windows-1251 code page, 0x98
	// (undefined) as U+FFFD.
	want := strings.Fields(cp1251Reference)
	if len(want) != 128 {
		t.Fatalf("reference has %d entries", len(want))
	}
	for i, r := range cp1251High {
		if got := fmt.Sprintf("%04x", r); got != want[i] {
			t.Errorf("0x%02x = %s, want %s", 0x80+i, got, want[i])
		}
	}
}

const cp1251Reference = `0402 0403 201a 0453 201e 2026 2020 2021 20ac 2030 0409 2039 040a 040c 040b 040f 0452 2018 2019 201c 201d 2022 2013 2014 fffd 2122 0459 203a 045a 045c 045b 045f 00a0 040e 045e 0408 00a4 0490 00a6 00a7 0401 00a9 0404 00ab 00ac 00ad 00ae 0407 00b0 00b1 0406 0456 0491 00b5 00b6 00b7 0451 2116 0454 00bb 0458 0405 0455 0457 0410 0411 0412 0413 0414 0415 0416 0417 0418 0419 041a 041b 041c 041d 041e 041f 0420 0421 0422 0423 0424 0425 0426 0427 0428 0429 042a 042b 042c 042d 042e 042f 0430 0431 0432 0433 0434 0435 0436 0437 0438 0439 043a 043b 043c 043d 043e 043f 0440 0441 0442 0443 0444 0445 0446 0447 0448 0449 044a 044b 044c 044d 044e 044f`
