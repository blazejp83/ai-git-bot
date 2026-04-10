package org.remus.giteabot.admin;

import lombok.extern.slf4j.Slf4j;
import org.remus.giteabot.repository.RepositoryApiClient;
import org.remus.giteabot.repository.RepositoryProviderMetadata;
import org.remus.giteabot.repository.RepositoryProviderRegistry;
import org.springframework.stereotype.Service;
import org.springframework.web.client.RestClient;

import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ConcurrentMap;

/**
 * Factory that creates and caches {@link RestClient} and {@link RepositoryApiClient}
 * instances from persisted {@link GitIntegration} entities.
 * <p>
 * Clients are cached by integration ID and {@link GitIntegration#getUpdatedAt()}
 * so that configuration changes automatically produce fresh clients.
 * <p>
 * Provider-specific logic (URL resolution, authentication) is delegated to
 * {@link RepositoryProviderMetadata} implementations via {@link RepositoryProviderRegistry}.
 */
@Slf4j
@Service
public class GiteaClientFactory {

    private final GitIntegrationService gitIntegrationService;
    private final RepositoryProviderRegistry providerRegistry;

    /** Cache key = integrationId, value = (updatedAt-millis, restClient, apiClient). */
    private final ConcurrentMap<Long, CachedClient> cache = new ConcurrentHashMap<>();

    public GiteaClientFactory(GitIntegrationService gitIntegrationService,
                              RepositoryProviderRegistry providerRegistry) {
        this.gitIntegrationService = gitIntegrationService;
        this.providerRegistry = providerRegistry;
    }

    /**
     * Returns a {@link RestClient} configured for the given Git integration.
     * Results are cached and re-created when the integration's updatedAt changes.
     */
    public RestClient getRestClient(GitIntegration integration) {
        return getCachedClient(integration).restClient;
    }

    /**
     * Returns a {@link RepositoryApiClient} for the given Git integration.
     * Results are cached and re-created when the integration's updatedAt changes.
     */
    public RepositoryApiClient getApiClient(GitIntegration integration) {
        return getCachedClient(integration).apiClient;
    }

    /**
     * Returns the decrypted token for the given integration.
     */
    public String getDecryptedToken(GitIntegration integration) {
        return gitIntegrationService.decryptToken(integration);
    }

    /**
     * Returns the resolved API URL for the given integration.
     */
    public String getApiUrl(GitIntegration integration) {
        RepositoryProviderMetadata provider = providerRegistry.getProvider(integration.getProviderType());
        return provider.resolveApiUrl(integration);
    }

    /**
     * Returns the clone/web URL for the given integration.
     * This URL is used for git clone operations.
     */
    public String getCloneUrl(GitIntegration integration) {
        RepositoryProviderMetadata provider = providerRegistry.getProvider(integration.getProviderType());
        return provider.resolveCloneUrl(integration);
    }

    /**
     * Returns the provider metadata for the given integration.
     */
    public RepositoryProviderMetadata getProviderMetadata(GitIntegration integration) {
        return providerRegistry.getProvider(integration.getProviderType());
    }

    public void evict(Long integrationId) {
        cache.remove(integrationId);
    }

    private CachedClient getCachedClient(GitIntegration integration) {
        CachedClient cached = cache.get(integration.getId());
        long updatedMillis = integration.getUpdatedAt().toEpochMilli();

        if (cached != null && cached.updatedAtMillis == updatedMillis) {
            return cached;
        }

        CachedClient newClient = buildClients(integration);
        cache.put(integration.getId(), newClient);
        log.info("Built new clients for integration '{}' (type={}, url={})",
                integration.getName(), integration.getProviderType(), integration.getUrl());
        return newClient;
    }

    private CachedClient buildClients(GitIntegration integration) {
        RepositoryProviderMetadata provider = providerRegistry.getProvider(integration.getProviderType());
        String decryptedToken = gitIntegrationService.decryptToken(integration);

        log.debug("Building clients for '{}': apiUrl={}, tokenLength={}",
                integration.getName(),
                provider.resolveApiUrl(integration),
                decryptedToken != null ? decryptedToken.length() : 0);

        RestClient restClient = provider.buildRestClient(integration, decryptedToken);
        RepositoryApiClient apiClient = provider.createClient(restClient, integration, decryptedToken);

        return new CachedClient(integration.getUpdatedAt().toEpochMilli(), restClient, apiClient);
    }

    private record CachedClient(long updatedAtMillis, RestClient restClient, RepositoryApiClient apiClient) {}
}
