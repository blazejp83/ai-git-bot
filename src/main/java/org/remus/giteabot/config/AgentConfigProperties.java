package org.remus.giteabot.config;

import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.stereotype.Component;

import java.util.List;

@Data
@Component
@ConfigurationProperties(prefix = "agent")
public class AgentConfigProperties {

    /**
     * Whether the issue implementation agent feature is enabled.
     */
    private boolean enabled = false;

    /**
     * Maximum number of files the agent can modify in a single implementation.
     */
    private int maxFiles = 10;

    /**
     * Whitelist of repositories (in "owner/repo" format) where the agent is active.
     * If empty, the agent is active on all repositories.
     */
    private List<String> allowedRepos = List.of();

    /**
     * Prefix for branches created by the agent.
     */
    private String branchPrefix = "ai-agent/";
}
