package service

import (
	"bytes"
	"net/url"
	"testing"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

func TestWeb115UploadSignatureMatchesP115Client(t *testing.T) {
	got := generateWeb115UploadSignature(
		"sample-user-key",
		"123456789",
		"ABCDEF1234567890ABCDEF1234567890ABCDEF12",
		"U_1_98765",
	)
	const want = "A2B16FAED489618E638645F3976C85429351706B"
	if got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

func TestWeb115UploadTokenMatchesP115Client(t *testing.T) {
	client := &driver.Pan115Client{UserID: 123456789}
	got := generateWeb115UploadToken(
		client,
		"37.0.4",
		"ABCDEF1234567890ABCDEF1234567890ABCDEF12",
		"",
		"1710000000",
		"987654321",
		"",
		"",
	)
	const want = "08a4b93c437b096de82b527939ea271d"
	if got != want {
		t.Fatalf("token = %s, want %s", got, want)
	}
}

func TestEncodeWeb115UploadFormFiltersEmptyValues(t *testing.T) {
	form := url.Values{}
	form.Set("filename", "A B.mkv")
	form.Set("sign_key", "")
	form.Set("sign_val", "")
	form.Set("target", "U_1_98765")
	form.Set("token", "abc")

	got := encodeWeb115UploadForm(form)
	const want = "filename=A+B.mkv&target=U_1_98765&token=abc"
	if got != want {
		t.Fatalf("encoded form = %s, want %s", got, want)
	}
}

func TestHashWeb115RangeSHA1(t *testing.T) {
	reader := bytes.NewReader([]byte("0123456789abcdef"))
	got, err := hashWeb115RangeSHA1(reader, "2-5")
	if err != nil {
		t.Fatalf("hashWeb115RangeSHA1 returned error: %v", err)
	}
	const want = "D2F75E8204FEDF2EACD261E2461B2964E3BFD5BE"
	if got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}

func TestEnsureWeb115UploadUserIDFromCookieHeader(t *testing.T) {
	client := driver.New()
	client.Client.SetHeader("Cookie", `UID="123456789"; CID=cid; SEID=seid`)
	if err := ensureWeb115UploadUserID(client); err != nil {
		t.Fatalf("ensureWeb115UploadUserID returned error: %v", err)
	}
	if client.UserID != 123456789 {
		t.Fatalf("UserID = %d, want 123456789", client.UserID)
	}
}

func TestParse115UIDRejectsInvalidValue(t *testing.T) {
	if _, err := parse115UID("abc"); err == nil {
		t.Fatalf("parse115UID expected error for invalid uid")
	}
}

func TestParse115UIDWithCookieSuffix(t *testing.T) {
	got, err := parse115UID("80009404_A1_1781141933")
	if err != nil {
		t.Fatalf("parse115UID returned error: %v", err)
	}
	if got != 80009404 {
		t.Fatalf("uid = %d, want 80009404", got)
	}
}
