package bucket

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSignTokenRoundTrip(t *testing.T) {
	key := "demo/rec/file.png"
	exp := time.Unix(1_900_000_000, 0)
	token := SignToken("secret", key, exp)
	if err := VerifyToken("secret", key, token, exp.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := VerifyToken("secret", key, token, exp); err != ErrExpiredToken {
		t.Fatalf("expiry: %v", err)
	}
	if err := VerifyToken("other", key, token, exp.Add(-time.Second)); err != ErrInvalidToken {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := VerifyToken("secret", "demo/rec/other.png", token, exp.Add(-time.Second)); err != ErrInvalidToken {
		t.Fatalf("wrong key: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %s", token)
	}
}

func TestLoadIdentityPrintsSecretOnce(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadIdentity(dir, "", "https://files.example", "127.0.0.1:8091")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Generated || first.Secret == "" || !strings.HasPrefix(first.ID, "bkt_") {
		t.Fatalf("identity %+v", first)
	}
	if first.URL != "https://files.example" {
		t.Fatalf("url %s", first.URL)
	}
	var buf bytes.Buffer
	printGeneratedSecret(&buf, first)
	if !strings.Contains(buf.String(), "PB_BUCKET_API_KEY="+first.Secret) {
		t.Fatalf("printed %q", buf.String())
	}

	second, err := LoadIdentity(dir, "", "", "127.0.0.1:8091")
	if err != nil {
		t.Fatal(err)
	}
	if second.Generated || second.Secret != first.Secret || second.ID != first.ID {
		t.Fatalf("second %+v first %+v", second, first)
	}
	buf.Reset()
	printGeneratedSecret(&buf, second)
	if buf.Len() != 0 {
		t.Fatalf("printed again %q", buf.String())
	}
	if second.URL != "http://127.0.0.1:8091" {
		t.Fatalf("derived url %s", second.URL)
	}
}

func TestRejectSQLiteDriver(t *testing.T) {
	_, err := OpenDB(t.Context(), "sqlite", "file:test")
	if err == nil || !strings.Contains(err.Error(), "sqlite") {
		t.Fatalf("err %v", err)
	}
	_, err = normalizeDriver("sqlite3")
	if err == nil {
		t.Fatal("expected sqlite3 to be rejected")
	}
	if _, err := normalizeDriver("postgres"); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeDriver("mysql"); err != nil {
		t.Fatal(err)
	}
}
