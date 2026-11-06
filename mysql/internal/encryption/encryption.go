// Package encryption reproduces the PGP helpers that were part of
// terraform-plugin-sdk v1 (helper/encryption) and were removed in v2.
package encryption

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

const keybasePrefix = "keybase:"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// RetrieveGPGKey returns the PGP key specified as the pgpKey parameter, or
// queries the public key from the keybase service if the parameter is a
// keybase username prefixed with "keybase:". Otherwise the key is expected to
// be a base64 encoded binary OpenPGP public key and is returned unchanged.
func RetrieveGPGKey(pgpKey string) (string, error) {
	if !strings.HasPrefix(pgpKey, keybasePrefix) {
		return pgpKey, nil
	}
	key, err := fetchKeybasePubkey(strings.TrimPrefix(pgpKey, keybasePrefix))
	if err != nil {
		return "", fmt.Errorf("error retrieving Public Key for %s: %w", pgpKey, err)
	}
	return key, nil
}

// fetchKeybasePubkey returns the base64 encoded, serialized primary public
// key of the given keybase user.
func fetchKeybasePubkey(username string) (string, error) {
	resp, err := httpClient.Get(fmt.Sprintf(
		"https://keybase.io/_/api/1.0/user/lookup.json?usernames=%s&fields=public_keys",
		username))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Status struct {
			Name string `json:"name"`
		} `json:"status"`
		Them []struct {
			PublicKeys struct {
				Primary struct {
					Bundle string `json:"bundle"`
				} `json:"primary"`
			} `json:"public_keys"`
		} `json:"them"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Status.Name != "OK" {
		return "", fmt.Errorf("got non-OK response: %q", out.Status.Name)
	}
	if len(out.Them) != 1 || out.Them[0].PublicKeys.Primary.Bundle == "" {
		return "", fmt.Errorf("unable to fetch keys for user %q from keybase", username)
	}

	entities, err := openpgp.ReadArmoredKeyRing(strings.NewReader(out.Them[0].PublicKeys.Primary.Bundle))
	if err != nil {
		return "", err
	}
	if len(entities) != 1 || entities[0] == nil {
		return "", fmt.Errorf("primary key could not be parsed for user %q", username)
	}
	var buf bytes.Buffer
	if err := entities[0].Serialize(&buf); err != nil {
		return "", fmt.Errorf("error serializing entity for user %q: %w", username, err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// EncryptValue encrypts value with the base64 encoded OpenPGP public key
// encryptionKey. It returns the key fingerprint (lower case hex) and the
// base64 encoded ciphertext. description is used in error messages.
func EncryptValue(encryptionKey, value, description string) (string, string, error) {
	data, err := base64.StdEncoding.DecodeString(encryptionKey)
	if err != nil {
		return "", "", fmt.Errorf("error encrypting %s: error decoding given PGP key: %w", description, err)
	}
	entity, err := openpgp.ReadEntity(packet.NewReader(bytes.NewReader(data)))
	if err != nil {
		return "", "", fmt.Errorf("error encrypting %s: error parsing given PGP key: %w", description, err)
	}

	var ct bytes.Buffer
	pt, err := openpgp.Encrypt(&ct, []*openpgp.Entity{entity}, nil, nil, nil)
	if err != nil {
		return "", "", fmt.Errorf("error encrypting %s: error setting up encryption for PGP message: %w", description, err)
	}
	if _, err := pt.Write([]byte(value)); err != nil {
		return "", "", fmt.Errorf("error encrypting %s: error encrypting PGP message: %w", description, err)
	}
	if err := pt.Close(); err != nil {
		return "", "", fmt.Errorf("error encrypting %s: error encrypting PGP message: %w", description, err)
	}

	return fmt.Sprintf("%x", entity.PrimaryKey.Fingerprint), base64.StdEncoding.EncodeToString(ct.Bytes()), nil
}
