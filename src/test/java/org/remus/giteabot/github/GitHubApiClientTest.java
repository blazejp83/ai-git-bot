package org.remus.giteabot.github;

import org.junit.jupiter.api.Test;
import org.remus.giteabot.repository.RepositoryApiClient;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link GitHubApiClient} verifying that it correctly implements
 * {@link RepositoryApiClient} and exposes the expected base URL, clone URL, and token.
 */
class GitHubApiClientTest {

    @Test
    void implementsRepositoryApiClient() {
        GitHubApiClient client = new GitHubApiClient(null, "https://api.github.com", "https://github.com", "ghp_token");
        assertInstanceOf(RepositoryApiClient.class, client);
    }

    @Test
    void getBaseUrl_returnsConfiguredUrl() {
        GitHubApiClient client = new GitHubApiClient(null, "https://api.github.com", "https://github.com", "ghp_token");
        assertEquals("https://api.github.com", client.getBaseUrl());
    }

    @Test
    void getCloneUrl_returnsConfiguredUrl() {
        GitHubApiClient client = new GitHubApiClient(null, "https://api.github.com", "https://github.com", "ghp_token");
        assertEquals("https://github.com", client.getCloneUrl());
    }

    @Test
    void getToken_returnsConfiguredToken() {
        GitHubApiClient client = new GitHubApiClient(null, "https://api.github.com", "https://github.com", "ghp_token");
        assertEquals("ghp_token", client.getToken());
    }

    @Test
    void constructorWithEnterpriseUrl() {
        GitHubApiClient client = new GitHubApiClient(null, "https://github.example.com/api/v3", "https://github.example.com", "token123");
        assertEquals("https://github.example.com/api/v3", client.getBaseUrl());
        assertEquals("https://github.example.com", client.getCloneUrl());
    }
}
