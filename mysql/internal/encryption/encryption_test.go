package encryption

import (
	"bytes"
	"encoding/base64"
	"io"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func TestEncryptValueRoundTrip(t *testing.T) {
	entity, err := openpgp.NewEntity("test", "", "test@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	var pub bytes.Buffer
	if err := entity.Serialize(&pub); err != nil {
		t.Fatal(err)
	}
	key, err := RetrieveGPGKey(base64.StdEncoding.EncodeToString(pub.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	fp, enc, err := EncryptValue(key, "s3cret", "test")
	if err != nil {
		t.Fatal(err)
	}
	if want := entity.PrimaryKey.Fingerprint; fp == "" || len(fp) != len(want)*2 {
		t.Errorf("unexpected fingerprint %q", fp)
	}

	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	md, err := openpgp.ReadMessage(bytes.NewReader(raw), openpgp.EntityList{entity}, nil, &packet.Config{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(md.UnverifiedBody)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "s3cret" {
		t.Errorf("got %q", got)
	}
}

func TestEncryptValueBadKey(t *testing.T) {
	if _, _, err := EncryptValue("!!!", "x", "test"); err == nil {
		t.Fatal("expected error")
	}
}
