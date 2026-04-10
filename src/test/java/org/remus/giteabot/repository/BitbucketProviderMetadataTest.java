package org.remus.giteabot.repository;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.remus.giteabot.admin.GitIntegration;

import static org.assertj.core.api.Assertions.assertThat;

class BitbucketProviderMetadataTest {

    private BitbucketProviderMetadata metadata;

    @BeforeEach
    void setUp() {
        metadata = new BitbucketProviderMetadata();
    }

    @Test
    void getProviderType_returnsBitbucket() {
        assertThat(metadata.getProviderType()).isEqualTo(RepositoryType.BITBUCKET);
    }

    @Test
    void resolveApiUrl_publicBitbucket_convertsToApi() {
        GitIntegration integration = new GitIntegration();
        integration.setUrl("https://bitbucket.org");
        integration.setProviderType(RepositoryType.BITBUCKET);

        String apiUrl = metadata.resolveApiUrl(integration);

        assertThat(apiUrl).isEqualTo("https://api.bitbucket.org/2.0");
    }

    @Test
    void resolveApiUrl_alreadyApiUrl_unchanged() {
        GitIntegration integration = new GitIntegration();
        integration.setUrl("https://api.bitbucket.org/2.0");
        integration.setProviderType(RepositoryType.BITBUCKET);

        String apiUrl = metadata.resolveApiUrl(integration);

        assertThat(apiUrl).isEqualTo("https://api.bitbucket.org/2.0");
    }

    @Test
    void resolveApiUrl_selfHosted_addsRestApi() {
        GitIntegration integration = new GitIntegration();
        integration.setUrl("https://bitbucket.example.com");
        integration.setProviderType(RepositoryType.BITBUCKET);

        String apiUrl = metadata.resolveApiUrl(integration);

        assertThat(apiUrl).isEqualTo("https://bitbucket.example.com/rest/api/1.0");
    }

    @Test
    void resolveCloneUrl_publicBitbucketApi_convertsToWeb() {
        GitIntegration integration = new GitIntegration();
        integration.setUrl("https://api.bitbucket.org/2.0");
        integration.setProviderType(RepositoryType.BITBUCKET);

        String cloneUrl = metadata.resolveCloneUrl(integration);

        assertThat(cloneUrl).isEqualTo("https://bitbucket.org");
    }

    @Test
    void resolveCloneUrl_selfHostedApi_removesRestApi() {
        GitIntegration integration = new GitIntegration();
        integration.setUrl("https://bitbucket.example.com/rest/api/1.0");
        integration.setProviderType(RepositoryType.BITBUCKET);

        String cloneUrl = metadata.resolveCloneUrl(integration);

        assertThat(cloneUrl).isEqualTo("https://bitbucket.example.com");
    }

    @Test
    void resolveCloneUrl_regularUrl_unchanged() {
        GitIntegration integration = new GitIntegration();
        integration.setUrl("https://bitbucket.org");
        integration.setProviderType(RepositoryType.BITBUCKET);

        String cloneUrl = metadata.resolveCloneUrl(integration);

        assertThat(cloneUrl).isEqualTo("https://bitbucket.org");
    }

    @Test
    void buildAuthorizationHeader_usesBearer() {
        String header = metadata.buildAuthorizationHeader("bb_token123");

        assertThat(header).isEqualTo("Bearer bb_token123");
    }

    @Test
    void resolveApiUrl_nullUrl_returnsDefault() {
        GitIntegration integration = new GitIntegration();
        integration.setUrl(null);
        integration.setProviderType(RepositoryType.BITBUCKET);

        String apiUrl = metadata.resolveApiUrl(integration);

        assertThat(apiUrl).isEqualTo("https://api.bitbucket.org/2.0");
    }

    @Test
    void resolveCloneUrl_nullUrl_returnsDefault() {
        GitIntegration integration = new GitIntegration();
        integration.setUrl(null);
        integration.setProviderType(RepositoryType.BITBUCKET);

        String cloneUrl = metadata.resolveCloneUrl(integration);

        assertThat(cloneUrl).isEqualTo("https://bitbucket.org");
    }
}
