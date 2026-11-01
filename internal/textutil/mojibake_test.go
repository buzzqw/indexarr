package textutil

import "testing"

func TestRepairDoubleEncodedFixesRealTitles(t *testing.T) {
	// The four titles the live index held on 2026-11-01, all from LimeTorrents,
	// which serves its listing with the UTF-8 bytes read with the wrong
	// charset. Two of them carry C1 controls (U+0081, U+008F, U+009D): those
	// pages were read as Latin-1, the others as CP1252.
	cases := []struct{ broken, want string }{
		{
			"Ð‘Ð¾Ð»ÑŒÑˆÐ¾Ð¹ Ð‘Ð°Ðº Ð‘Ð¾Ð»ÑŒÑˆÐ¾Ð¹ Ð—Ð°Ð¹ Big Buck Bunny (2008) BDRip 1080p",
			"Большой Бак Большой Зай Big Buck Bunny (2008) BDRip 1080p",
		},
		{
			"Macio Jalisa 2 â€“ Another Hood Love Story (Macio Jalisa â€“ Another Hood Love Story) by Sierra Nicole [RAL] [BÐ¯] epub",
			"Macio Jalisa 2 – Another Hood Love Story (Macio Jalisa – Another Hood Love Story) by Sierra Nicole [RAL] [BЯ] epub",
		},
		{
			"ÐžÑ‚Ð²ÐµÑ‚Ð½Ñ‹Ð¹ ÑƒÐ´Ð°Ñ€ Ð¢ÐµÐ½ÐµÐ²Ð°Ñ\u008f Ð²Ð¾Ð¹Ð½Ð° Strike back Shadow Warfare [S04] (2013) HDTVRip 720p BaibaKo",
			"Ответный удар Теневая война Strike back Shadow Warfare [S04] (2013) HDTVRip 720p BaibaKo",
		},
		{
			"Ð˜Ð»Ð»ÑŽÐ·Ð¸Ð¾Ð½Ð¸Ñ\u0081Ñ‚ Illusionist The (2006) Ð\u009dDRip Ð¾Ñ‚ Scarabey D",
			"Иллюзионист Illusionist The (2006) НDRip от Scarabey D",
		},
	}
	for _, tc := range cases {
		if got := RepairDoubleEncoded(tc.broken); got != tc.want {
			t.Errorf("RepairDoubleEncoded(%q) = %q, want %q", tc.broken, got, tc.want)
		}
		if !IsDoubleEncoded(tc.broken) {
			t.Errorf("IsDoubleEncoded(%q) = false, want true", tc.broken)
		}
	}
}

func TestRepairDoubleEncodedLeavesGoodTitlesAlone(t *testing.T) {
	for _, title := range []string{
		"",
		"Big Buck Bunny (2008) BDRip 1080p",
		// Correct text of a real alphabet: its characters are above U+00FF and
		// cannot be a byte of a double encoded sequence.
		"Большой Бак Большой Зай Big Buck Bunny",
		"Ελληνικά Γράμματα",
		"日本語のタイトル",
		"Café Ñandú (2020) 1080p",
		"Pokémon – Detektiv Pikachu",
		// A lone byte that is not valid UTF-8 in any reading.
		"Ð",
		"Â",
		// A title that reads as real text whichever way it is decoded.
		"Æon Flux (2005)",
	} {
		if got := RepairDoubleEncoded(title); got != title {
			t.Errorf("RepairDoubleEncoded(%q) = %q, want it unchanged", title, got)
		}
		if IsDoubleEncoded(title) {
			t.Errorf("IsDoubleEncoded(%q) = true, want false", title)
		}
	}
}

func TestRepairDoubleEncodedIsIdempotent(t *testing.T) {
	broken := "Ð‘Ð¾Ð»ÑŒÑˆÐ¾Ð¹ Ð‘Ð°Ðº"
	once := RepairDoubleEncoded(broken)
	if once == broken {
		t.Fatal("the title was not repaired")
	}
	if twice := RepairDoubleEncoded(once); twice != once {
		t.Fatalf("second pass changed %q into %q", once, twice)
	}
}

func TestRepairDoubleEncodedRefusesWhenTheResultKeepsControls(t *testing.T) {
	// "Â" + U+0080 reads back as C2 80, a valid UTF-8 U+0080: still a control,
	// so the text is left alone instead of being turned into a second puzzle.
	const text = "\u00c2\u0080"
	if got := RepairDoubleEncoded(text); got != text {
		t.Fatalf("RepairDoubleEncoded(%q) = %q, want it unchanged", text, got)
	}
}
