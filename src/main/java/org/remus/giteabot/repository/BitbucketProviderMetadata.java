package org.remus.giteabot.repository;

import lombok.extern.slf4j.Slf4j;
import org.remus.giteabot.admin.GitIntegration;
import org.remus.giteabot.bitbucket.BitbucketApiClient;
import org.springframework.stereotype.Component;
import org.springframework.web.client.RestClient;

/**
 * Metadata and factory for Bitbucket Cloud repository integration.
 * Handles URL transformations between bitbucket.org and api.bitbucket.org,
 * and creates properly configured Bitbucket API clients.
 */
@Slf4j
@Component
public class BitbucketProviderMetadata implements RepositoryProviderMetadata {

    private static final String DEFAULT_WEB_URL = "https://bitbucket.org";
    private static final String DEFAULT_API_URL = "https://api.bitbucket.org/2.0";

    @Override
    public RepositoryType getProviderType() {
        return RepositoryType.BITBUCKET;
    }

    @Override
    public String getDefaultWebUrl() {
        return DEFAULT_WEB_URL;
    }

    @Override
    public String resolveApiUrl(GitIntegration integration) {
        String url = integration.getUrl();
        if (url == null || url.isBlank()) {
            return DEFAULT_API_URL;
        }

        // Already an API URL
        if (url.contains("api.bitbucket.org")) {
            return url;
        }

        // Public Bitbucket: bitbucket.org -> api.bitbucket.org/2.0
        if (url.contains("bitbucket.org")) {
            String replaced = url.replace("bitbucket.org", "api.bitbucket.org");
            String baseUrl = replaced.endsWith("/") ? replaced.substring(0, replaced.length() - 1) : replaced;
            return baseUrl + "/2.0";
        }

        // Self-hosted Bitbucket: add /rest/api/1.0 suffix
        String baseUrl = url.endsWith("/") ? url.substring(0, url.length() - 1) : url;
        return baseUrl + "/rest/api/1.0";
    }

    @Override
    public String resolveCloneUrl(GitIntegration integration) {
        String url = integration.getUrl();
        if (url == null || url.isBlank()) {
            return DEFAULT_WEB_URL;
        }

        // Convert API URL back to web URL
        if (url.contains("api.bitbucket.org")) {
            return url.replaceAll("api\\.bitbucket\\.org(/2\\.0)?", "bitbucket.org")
                    .replaceAll("/$", "");
        }

        // Self-hosted: remove /rest/api/1.0 suffix
        if (url.contains("/rest/api/1.0")) {
            return url.replaceAll("/rest/api/1\\.0/?$", "");
        }

        return url;
    }

    @Override
    public String buildAuthorizationHeader(String token) {
        return "Bearer " + token;
    }

    @Override
    public RestClient buildRestClient(GitIntegration integration, String decryptedToken) {
        String apiUrl = resolveApiUrl(integration);
        String authHeader = buildAuthorizationHeader(decryptedToken);

        log.debug("Building Bitbucket RestClient: apiUrl={}", apiUrl);

        return RestClient.builder()
                .baseUrl(apiUrl)
                .defaultHeader("Authorization", authHeader)
                .defaultHeader("Accept", "application/json")
                .build();
    }

    @Override
    public RepositoryApiClient createClient(RestClient restClient, GitIntegration integration, String decryptedToken) {
        String apiUrl = resolveApiUrl(integration);
        String cloneUrl = resolveCloneUrl(integration);
        return new BitbucketApiClient(restClient, apiUrl, cloneUrl, decryptedToken);
    }
}
