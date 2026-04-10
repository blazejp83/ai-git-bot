package org.remus.giteabot.repository;

import org.remus.giteabot.admin.GitIntegration;
import org.springframework.web.client.RestClient;

/**
 * Metadata and factory interface for Git repository provider integrations.
 * Each repository provider (GitHub, Gitea, GitLab, etc.) implements this interface
 * to define its type, URL transformations, authentication, and client creation.
 */
public interface RepositoryProviderMetadata {

    /**
     * Returns the repository provider type this metadata handles.
     */
    RepositoryType getProviderType();

    /**
     * Returns the default web URL for this provider (e.g., "https://github.com").
     */
    String getDefaultWebUrl();

    /**
     * Resolves the API base URL from the configured integration URL.
     * For example, transforms "https://github.com" to "https://api.github.com".
     *
     * @param integration the Git integration configuration
     * @return the resolved API URL
     */
    String resolveApiUrl(GitIntegration integration);

    /**
     * Resolves the web/clone URL from the configured integration URL.
     * This is the URL used for git clone operations.
     * For example, transforms "https://api.github.com" back to "https://github.com".
     *
     * @param integration the Git integration configuration
     * @return the resolved web URL for cloning
     */
    String resolveCloneUrl(GitIntegration integration);

    /**
     * Builds the Authorization header value for this provider.
     *
     * @param token the decrypted access token
     * @return the Authorization header value (e.g., "Bearer token" or "token xyz")
     */
    String buildAuthorizationHeader(String token);

    /**
     * Builds a configured RestClient for this provider.
     *
     * @param integration the Git integration configuration
     * @param decryptedToken the decrypted access token
     * @return configured RestClient pointing at the API URL
     */
    RestClient buildRestClient(GitIntegration integration, String decryptedToken);

    /**
     * Creates a RepositoryApiClient instance for this provider.
     *
     * @param restClient the configured RestClient
     * @param integration the Git integration configuration
     * @param decryptedToken the decrypted access token
     * @return configured RepositoryApiClient
     */
    RepositoryApiClient createClient(RestClient restClient, GitIntegration integration, String decryptedToken);
}

