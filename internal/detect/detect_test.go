package detect

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// hits returns [kind, text] pairs, as the Node tests do.
func hits(t string, kinds []string) [][2]string {
	r := []rune(t)
	out := [][2]string{}
	for _, m := range Find(t, kinds) {
		out = append(out, [2]string{m.Kind, string(r[m.Start:m.End])})
	}
	return out
}

func eq(t *testing.T, got, want [][2]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func first(t *testing.T, text string) string {
	t.Helper()
	h := hits(text, nil)
	if len(h) == 0 {
		t.Fatalf("nothing found in %q", text)
	}
	return h[0][0]
}

func TestFindsEmailsKeysJWTCardsPhones(t *testing.T) {
	eq(t, hits("mail a.b@ex.co now", nil), [][2]string{{"email", "a.b@ex.co"}})
	if first(t, "k=sk-ant-abcdefghijklmnopqrstuvwx") != "api-key" {
		t.Fatal("api key")
	}
	if first(t, "ghp_"+strings.Repeat("a", 36)) != "api-key" {
		t.Fatal("github token")
	}
	if first(t, "eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0NTY.abcdefghijk") != "jwt" {
		t.Fatal("jwt")
	}
	eq(t, hits("4111 1111 1111 1111", nil), [][2]string{{"credit-card", "4111 1111 1111 1111"}})
	eq(t, hits("call 0912 345 678 ok", nil), [][2]string{{"phone", "0912 345 678"}})
}

func TestIgnoresOrdinaryTextAndInvalidCards(t *testing.T) {
	eq(t, hits("Setup guide for the demo project", nil), [][2]string{})
	eq(t, hits("4111 1111 1111 1112", nil), [][2]string{})
	if !LuhnValid("4111111111111111") {
		t.Fatal("luhn")
	}
	eq(t, hits("a-long-hyphenated-slug-for-a-blog-post-title", nil), [][2]string{})
}

func TestPasswordValueOnly(t *testing.T) {
	eq(t, hits("password: hunter2xyz", nil), [][2]string{{"secret-assignment", "hunter2xyz"}})
}

func TestVietnameseLabels(t *testing.T) {
	eq(t, hits("Mật khẩu: Tr0ngDong2026x", nil), [][2]string{{"secret-assignment", "Tr0ngDong2026x"}})
	eq(t, hits("Mat khau: TrOngDong2026x", nil), [][2]string{{"secret-assignment", "TrOngDong2026x"}})
	eq(t, hits("Mã OTP: 482913", nil), [][2]string{{"secret-assignment", "482913"}})
	eq(t, hits("Author: Nguyen Van A", nil), [][2]string{})
	eq(t, hits("xpassword: nope1234", nil), [][2]string{})
}

func TestOCRNoise(t *testing.T) {
	orig := "APT_KEY=sk-proj-AbC123x¥2789QuErTy456Ui0p"
	f := FoldOCR(orig)
	if len([]rune(f)) != len([]rune(orig)) {
		t.Fatal("folding changed the length")
	}
	m := Find(f, nil)[0]
	if m.Kind != "api-key" && m.Kind != "secret-assignment" && m.Kind != "high-entropy" {
		t.Fatal(m.Kind)
	}
	if !strings.HasSuffix(string([]rune(f)[m.Start:m.End]), "sk-proj-AbC123xY2789QuErTy456Ui0p") {
		t.Fatal("key not fully covered")
	}
	eq(t, hits("DB_PASSWORD=hunter2xyz", nil), [][2]string{{"secret-assignment", "hunter2xyz"}})
	eq(t, hits("MONKEY=banana", nil), [][2]string{})
}

func TestOCRMisreads(t *testing.T) {
	eq(t, hits(FoldOCR("Contact: jane.doegexample.com"), nil), [][2]string{{"email", "jane.doegexample.com"}})
	eq(t, hits(FoldOCR("see staging.example.com"), nil), [][2]string{})
	eq(t, hits(FoldOCR("import logging.info"), nil), [][2]string{})
	if first(t, FoldOCR("Card 4111 1l11 1111 1lll")) != "credit-card" {
		t.Fatal("card")
	}
	if first(t, FoldOCR("So dien thoai: 0912 34S 678")) != "phone" {
		t.Fatal("phone")
	}
	eq(t, hits("Contact Jjane.. doe@examp Le. com", nil), [][2]string{{"email", "Contact Jjane.. doe@examp Le. com"}})
}

func TestURLPasswordIsAPassword(t *testing.T) {
	for _, m := range Find("DATABASE_URL=postgres://app:S3cr3tPassw0rd@db.internal:5432/prod", nil) {
		if m.Kind == "email" {
			t.Fatal("classified as email")
		}
	}
}

func TestPhoneNotInsideLongerNumber(t *testing.T) {
	eq(t, hits("order 1234567890123 shipped", []string{"phone"}), [][2]string{})
}

func TestMaskedPreview(t *testing.T) {
	if MaskedPreview("sk-abcdef") != "sk…(9)" || MaskedPreview("abc") != "…(3)" {
		t.Fatal(MaskedPreview("sk-abcdef"))
	}
}

// OCR reads a row of taskbar icons as "F@QAC.CHOEIECES": an address must end with a real
// top-level domain. Real addresses still do, whatever their case or country.
func TestEmailNeedsARealTLD(t *testing.T) {
	email := []string{"email"}
	eq(t, hits("an| =~@ech2aan — F@QAC.CHOEIECES", email), [][2]string{})
	eq(t, hits("xem|fh“SG@QRO.nhat kw trite. CHOICE", email), [][2]string{})
	eq(t, hits("lQĐọc „bảng©điều;ñkhiển mm —pntof, S@QACr.cH TICE", email), [][2]string{})
	for _, addr := range []string{"jane.doe@example.com", "JANE@EXAMPLE.COM", "an@fpt.com.vn", "ops@acme.info", "hi@studio.design", "x@y.de", "dev@corp.internal", "SALES@ACME.VN"} {
		eq(t, hits("Contact: "+addr+" today", email), [][2]string{{"email", addr}})
	}
	// A sentence glued to the address by OCR keeps the address.
	eq(t, hits("mail jane@example.com.Then call", email), [][2]string{{"email", "jane@example.com"}})
}

// The detector finds exactly the expected matches on a corpus of lines (testdata/expected.json,
// from the reference implementation). A Stripe-shaped test key is put together here, not stored,
// so the repository holds nothing a secret scanner would take for a real key.
func TestSameAsNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("@STRIPE_TEST_KEY@"), []byte("sk_"+"live_"+"51HxQ2bLkN8vZ3aPOqR7tY9u"))
	var cases []struct {
		Line, Folded string
		Matches      []Match
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if f := FoldOCR(c.Line); f != c.Folded {
			t.Errorf("fold %q: got %q, Node %q", c.Line, f, c.Folded)
		}
		got := Find(c.Folded, nil)
		if len(got) == 0 && len(c.Matches) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Matches) {
			t.Errorf("%q:\n  go   %v\n  node %v", c.Line, got, c.Matches)
		}
	}
}
