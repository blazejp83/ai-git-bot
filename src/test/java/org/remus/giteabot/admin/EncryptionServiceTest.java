package org.remus.giteabot.admin;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class EncryptionServiceTest {

    private EncryptionService encryptionService;

    @BeforeEach
    void setUp() {
        encryptionService = new EncryptionService("test-key");
    }

    @Test
    void encryptAndDecrypt_roundTrip_succeeds() {
        String original = "my-secret-api-key";

        String encrypted = encryptionService.encrypt(original);
        String decrypted = encryptionService.decrypt(encrypted);

        assertEquals(original, decrypted);
    }

    @Test
    void encrypt_nullInput_returnsNull() {
        assertNull(encryptionService.encrypt(null));
    }

    @Test
    void encrypt_blankInput_returnsNull() {
        assertNull(encryptionService.encrypt("   "));
    }

    @Test
    void decrypt_nullInput_returnsNull() {
        assertNull(encryptionService.decrypt(null));
    }

    @Test
    void encrypt_differentInputs_produceDifferentOutputs() {
        String encrypted1 = encryptionService.encrypt("secret-one");
        String encrypted2 = encryptionService.encrypt("secret-two");

        assertNotEquals(encrypted1, encrypted2);
    }

    @Test
    void encrypt_sameInput_producesDifferentOutputs() {
        String input = "same-secret";

        String encrypted1 = encryptionService.encrypt(input);
        String encrypted2 = encryptionService.encrypt(input);

        assertNotEquals(encrypted1, encrypted2, "Same input should produce different ciphertexts due to random IV");
        // Both should still decrypt to the same value
        assertEquals(input, encryptionService.decrypt(encrypted1));
        assertEquals(input, encryptionService.decrypt(encrypted2));
    }
}
