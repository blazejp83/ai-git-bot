package encrypt

import (
	"testing"
)

func TestRoundTrip(t *testing.T) {
	svc := New("test-key")
	original := "my-secret-api-key"

	encrypted, err := svc.Encrypt(original)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if encrypted == original {
		t.Fatal("Encrypted value should differ from original")
	}

	decrypted, err := svc.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != original {
		t.Fatalf("Decrypt got %q, want %q", decrypted, original)
	}
}

func TestEncryptEmpty(t *testing.T) {
	svc := New("test-key")
	result, err := svc.Encrypt("")
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Fatalf("got %q, want empty", result)
	}
}

func TestDecryptEmpty(t *testing.T) {
	svc := New("test-key")
	result, err := svc.Decrypt("")
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Fatalf("got %q, want empty", result)
	}
}

func TestSameInputDifferentOutputs(t *testing.T) {
	svc := New("test-key")
	input := "same-secret"

	enc1, _ := svc.Encrypt(input)
	enc2, _ := svc.Encrypt(input)

	if enc1 == enc2 {
		t.Fatal("Same input should produce different ciphertexts (random IV)")
	}

	dec1, _ := svc.Decrypt(enc1)
	dec2, _ := svc.Decrypt(enc2)
	if dec1 != input || dec2 != input {
		t.Fatal("Both should decrypt to the same value")
	}
}

func TestDifferentInputsDifferentOutputs(t *testing.T) {
	svc := New("test-key")
	enc1, _ := svc.Encrypt("secret-one")
	enc2, _ := svc.Encrypt("secret-two")
	if enc1 == enc2 {
		t.Fatal("Different inputs should produce different ciphertexts")
	}
}

func TestDisabledEncryption(t *testing.T) {
	svc := New("")
	if svc.Enabled() {
		t.Fatal("Should not be enabled with empty key")
	}

	result, _ := svc.Encrypt("plain-api-key")
	if result != "plain-api-key" {
		t.Fatalf("Disabled encrypt should return plaintext, got %q", result)
	}

	result, _ = svc.Decrypt("plain-api-key")
	if result != "plain-api-key" {
		t.Fatalf("Disabled decrypt should return plaintext, got %q", result)
	}
}

func TestDisabledDecryptLegacyPrefix(t *testing.T) {
	svc := New("")
	result, _ := svc.Decrypt("ENC:some-encrypted-value")
	if result != "some-encrypted-value" {
		t.Fatalf("Should strip ENC: prefix, got %q", result)
	}
}

func TestDecryptInvalidBase64(t *testing.T) {
	svc := New("test-key")
	result, _ := svc.Decrypt("this-is-not-base64!@#$%")
	if result != "this-is-not-base64!@#$%" {
		t.Fatalf("Should return original for invalid base64, got %q", result)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	svc1 := New("key-one")
	svc2 := New("key-two")

	encrypted, _ := svc1.Encrypt("secret")
	// Decrypting with wrong key should return original ciphertext (graceful failure)
	result, _ := svc2.Decrypt(encrypted)
	if result != encrypted {
		t.Fatalf("Wrong key should return ciphertext as-is")
	}
}

func TestENCPrefixWithEncryption(t *testing.T) {
	svc := New("test-key")

	encrypted, _ := svc.Encrypt("my-secret")
	// Simulate legacy ENC: prefix
	withPrefix := "ENC:" + encrypted
	decrypted, _ := svc.Decrypt(withPrefix)
	if decrypted != "my-secret" {
		t.Fatalf("Should strip ENC: and decrypt, got %q", decrypted)
	}
}
