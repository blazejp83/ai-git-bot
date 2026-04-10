package org.remus.giteabot.bitbucket;

import org.junit.jupiter.api.Test;
import org.remus.giteabot.repository.RepositoryApiClient;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link BitbucketApiClient} verifying that it correctly implements
 * {@link RepositoryApiClient} and exposes the expected base URL, clone URL, and token.
 */
class BitbucketApiClientTest {

    @Test
    void implementsRepositoryApiClient() {
        BitbucketApiClient client = new BitbucketApiClient(null,
                "https://api.bitbucket.org/2.0", "https://bitbucket.org", "bb_token");
        assertInstanceOf(RepositoryApiClient.class, client);
    }

    @Test
    void getBaseUrl_returnsConfiguredUrl() {
        BitbucketApiClient client = new BitbucketApiClient(null,
                "https://api.bitbucket.org/2.0", "https://bitbucket.org", "bb_token");
        assertEquals("https://api.bitbucket.org/2.0", client.getBaseUrl());
    }

    @Test
    void getCloneUrl_returnsConfiguredUrl() {
        BitbucketApiClient client = new BitbucketApiClient(null,
                "https://api.bitbucket.org/2.0", "https://bitbucket.org", "bb_token");
        assertEquals("https://bitbucket.org", client.getCloneUrl());
    }

    @Test
    void getToken_returnsConfiguredToken() {
        BitbucketApiClient client = new BitbucketApiClient(null,
                "https://api.bitbucket.org/2.0", "https://bitbucket.org", "bb_token");
        assertEquals("bb_token", client.getToken());
    }

    @Test
    void addReaction_noOp() {
        // Bitbucket doesn't support reactions; verify no exception is thrown
        BitbucketApiClient client = new BitbucketApiClient(null,
                "https://api.bitbucket.org/2.0", "https://bitbucket.org", "bb_token");
        assertDoesNotThrow(() -> client.addReaction("workspace", "repo", 1L, "+1"));
    }
}
