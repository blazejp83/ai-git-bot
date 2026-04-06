package org.remus.giteabot.agent;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.remus.giteabot.agent.model.FileChange;
import org.remus.giteabot.agent.model.ImplementationPlan;
import org.remus.giteabot.ai.AiClient;
import org.remus.giteabot.ai.AiMessage;
import org.remus.giteabot.config.AgentConfigProperties;
import org.remus.giteabot.config.PromptService;
import org.remus.giteabot.gitea.GiteaApiClient;
import org.remus.giteabot.gitea.model.WebhookPayload;
import org.springframework.scheduling.annotation.Async;
import org.springframework.stereotype.Service;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.stream.Collectors;

@Slf4j
@Service
public class IssueImplementationService {

    private static final String AGENT_PROMPT_NAME = "agent";
    private static final Pattern JSON_BLOCK_PATTERN = Pattern.compile("```json\\s*\\n(.*?)\\n\\s*```", Pattern.DOTALL);
    private static final int MAX_FILE_CONTENT_CHARS = 50000;
    private static final int MAX_TREE_FILES_FOR_CONTEXT = 200;

    private final GiteaApiClient giteaApiClient;
    private final AiClient aiClient;
    private final PromptService promptService;
    private final AgentConfigProperties agentConfig;
    private final ObjectMapper objectMapper;

    public IssueImplementationService(GiteaApiClient giteaApiClient, AiClient aiClient,
                                      PromptService promptService, AgentConfigProperties agentConfig) {
        this.giteaApiClient = giteaApiClient;
        this.aiClient = aiClient;
        this.promptService = promptService;
        this.agentConfig = agentConfig;
        this.objectMapper = new ObjectMapper();
    }

    @Async
    public void handleIssueAssigned(WebhookPayload payload) {
        String owner = payload.getRepository().getOwner().getLogin();
        String repo = payload.getRepository().getName();
        String repoFullName = payload.getRepository().getFullName();
        Long issueNumber = payload.getIssue().getNumber();
        String issueTitle = payload.getIssue().getTitle();
        String issueBody = payload.getIssue().getBody();

        log.info("Starting implementation for issue #{} '{}' in {}", issueNumber, issueTitle, repoFullName);

        String branchName = null;
        try {
            // Post initial progress comment
            giteaApiClient.postComment(owner, repo, issueNumber,
                    "🤖 **AI Agent**: I've been assigned to this issue. Analyzing requirements and starting implementation...",
                    null);

            // Get default branch
            String defaultBranch = giteaApiClient.getDefaultBranch(owner, repo, null);
            log.info("Default branch for {}: {}", repoFullName, defaultBranch);

            // Fetch repository tree for context
            List<Map<String, Object>> tree = giteaApiClient.getRepositoryTree(owner, repo, defaultBranch, null);
            String treeContext = buildTreeContext(tree);

            // Fetch relevant file contents for context
            String fileContext = fetchRelevantFileContents(owner, repo, defaultBranch, tree, issueTitle, issueBody);

            // Build AI prompt
            String userMessage = buildImplementationPrompt(issueTitle, issueBody, treeContext, fileContext);

            // Get system prompt for agent
            String systemPrompt = promptService.getSystemPrompt(AGENT_PROMPT_NAME);

            // Call AI to generate implementation plan
            log.info("Requesting AI to generate implementation for issue #{}", issueNumber);
            String aiResponse = aiClient.chat(new ArrayList<>(), userMessage, systemPrompt, null);

            // Parse AI response
            ImplementationPlan plan = parseAiResponse(aiResponse);
            if (plan == null || plan.getFileChanges() == null || plan.getFileChanges().isEmpty()) {
                giteaApiClient.postComment(owner, repo, issueNumber,
                        "🤖 **AI Agent**: I was unable to generate a valid implementation plan for this issue. " +
                        "The issue may be too complex or ambiguous for automated implementation.",
                        null);
                return;
            }

            // Enforce max files limit
            if (plan.getFileChanges().size() > agentConfig.getMaxFiles()) {
                giteaApiClient.postComment(owner, repo, issueNumber,
                        String.format("🤖 **AI Agent**: The generated plan requires %d file changes, " +
                                "but the maximum allowed is %d. Please break this issue into smaller tasks.",
                                plan.getFileChanges().size(), agentConfig.getMaxFiles()),
                        null);
                return;
            }

            // Create branch name
            branchName = agentConfig.getBranchPrefix() + "issue-" + issueNumber;

            // Create feature branch
            giteaApiClient.createBranch(owner, repo, branchName, defaultBranch, null);
            log.info("Created branch '{}' for issue #{}", branchName, issueNumber);

            // Commit file changes
            for (FileChange change : plan.getFileChanges()) {
                String commitMessage = String.format("agent: %s %s (issue #%d)",
                        change.getOperation().name().toLowerCase(), change.getPath(), issueNumber);

                switch (change.getOperation()) {
                    case CREATE -> giteaApiClient.createOrUpdateFile(owner, repo, change.getPath(),
                            change.getContent(), commitMessage, branchName, null, null);
                    case UPDATE -> {
                        String sha = giteaApiClient.getFileSha(owner, repo, change.getPath(), branchName, null);
                        giteaApiClient.createOrUpdateFile(owner, repo, change.getPath(),
                                change.getContent(), commitMessage, branchName, sha, null);
                    }
                    case DELETE -> {
                        String sha = giteaApiClient.getFileSha(owner, repo, change.getPath(), branchName, null);
                        giteaApiClient.deleteFile(owner, repo, change.getPath(),
                                commitMessage, branchName, sha, null);
                    }
                }
            }

            // Create pull request
            String prTitle = String.format("AI Agent: %s (fixes #%d)", issueTitle, issueNumber);
            String prBody = buildPrBody(issueNumber, plan);
            Long prNumber = giteaApiClient.createPullRequest(owner, repo, prTitle, prBody,
                    branchName, defaultBranch, null);

            // Comment on issue with link to PR
            String successComment = String.format(
                    "🤖 **AI Agent**: Implementation complete! I've created PR #%d with the following changes:\n\n" +
                    "**Summary**: %s\n\n" +
                    "**Files changed** (%d):\n%s\n\n" +
                    "Please review the changes carefully before merging.",
                    prNumber, plan.getSummary(), plan.getFileChanges().size(),
                    plan.getFileChanges().stream()
                            .map(fc -> String.format("- `%s` (%s)", fc.getPath(), fc.getOperation()))
                            .collect(Collectors.joining("\n")));

            giteaApiClient.postComment(owner, repo, issueNumber, successComment, null);
            log.info("Successfully created PR #{} for issue #{} in {}", prNumber, issueNumber, repoFullName);

        } catch (Exception e) {
            log.error("Failed to implement issue #{} in {}: {}", issueNumber, repoFullName, e.getMessage(), e);

            // Clean up branch on failure
            if (branchName != null) {
                giteaApiClient.deleteBranch(owner, repo, branchName, null);
            }

            // Post failure comment
            try {
                giteaApiClient.postComment(owner, repo, issueNumber,
                        String.format("🤖 **AI Agent**: Implementation failed with error: `%s`\n\n" +
                                "The created branch has been cleaned up. Please review the issue and try again.",
                                e.getMessage()),
                        null);
            } catch (Exception commentError) {
                log.error("Failed to post failure comment on issue #{}: {}", issueNumber, commentError.getMessage());
            }
        }
    }

    String buildTreeContext(List<Map<String, Object>> tree) {
        if (tree == null || tree.isEmpty()) {
            return "No files found in repository.";
        }
        StringBuilder sb = new StringBuilder("Repository file tree:\n");
        int count = 0;
        for (Map<String, Object> entry : tree) {
            if (count >= MAX_TREE_FILES_FOR_CONTEXT) {
                sb.append("... (truncated, ").append(tree.size() - count).append(" more files)\n");
                break;
            }
            String type = (String) entry.getOrDefault("type", "blob");
            String path = (String) entry.getOrDefault("path", "");
            if ("blob".equals(type)) {
                sb.append("  ").append(path).append("\n");
            }
            count++;
        }
        return sb.toString();
    }

    String fetchRelevantFileContents(String owner, String repo, String ref,
                                             List<Map<String, Object>> tree,
                                             String issueTitle, String issueBody) {
        // Pick source files mentioned in the issue or common configuration files
        List<String> relevantPaths = new ArrayList<>();
        String issueLower = (issueTitle + " " + (issueBody != null ? issueBody : "")).toLowerCase();

        for (Map<String, Object> entry : tree) {
            String path = (String) entry.getOrDefault("path", "");
            String type = (String) entry.getOrDefault("type", "blob");
            if (!"blob".equals(type)) continue;

            // Include files explicitly mentioned in the issue
            if (issueLower.contains(path.toLowerCase())) {
                relevantPaths.add(path);
                continue;
            }

            // Include key configuration files
            if (path.endsWith("pom.xml") || path.endsWith("build.gradle")
                    || path.equals("README.md") || path.endsWith("application.properties")) {
                relevantPaths.add(path);
            }
        }

        // Limit to a reasonable number
        if (relevantPaths.size() > 15) {
            relevantPaths = relevantPaths.subList(0, 15);
        }

        StringBuilder sb = new StringBuilder();
        int totalChars = 0;
        for (String path : relevantPaths) {
            if (totalChars > MAX_FILE_CONTENT_CHARS) {
                sb.append("\n(File context truncated due to size limits)\n");
                break;
            }
            try {
                String content = giteaApiClient.getFileContent(owner, repo, path, ref, null);
                if (content != null && !content.isEmpty()) {
                    sb.append("\n--- File: ").append(path).append(" ---\n");
                    sb.append(content).append("\n");
                    totalChars += content.length();
                }
            } catch (Exception e) {
                log.debug("Could not fetch file content for {}: {}", path, e.getMessage());
            }
        }
        return sb.toString();
    }

    private String buildImplementationPrompt(String issueTitle, String issueBody,
                                             String treeContext, String fileContext) {
        return String.format("""
                ## Issue to Implement
                
                **Title**: %s
                
                **Description**:
                %s
                
                ## Repository Context
                
                %s
                
                ## Relevant File Contents
                
                %s
                
                ## Instructions
                
                Please analyze the issue and generate the implementation. Output your response as a JSON \
                object with the structure described in the system prompt.
                """, issueTitle, issueBody != null ? issueBody : "(no description)", treeContext, fileContext);
    }

    ImplementationPlan parseAiResponse(String aiResponse) {
        if (aiResponse == null || aiResponse.isBlank()) {
            log.warn("Empty AI response");
            return null;
        }

        // Extract JSON from markdown code block
        Matcher matcher = JSON_BLOCK_PATTERN.matcher(aiResponse);
        String jsonStr;
        if (matcher.find()) {
            jsonStr = matcher.group(1);
        } else {
            // Try parsing the entire response as JSON
            jsonStr = aiResponse.strip();
        }

        try {
            AiImplementationResponse response = objectMapper.readValue(jsonStr, AiImplementationResponse.class);
            if (response == null || response.getFileChanges() == null) {
                log.warn("Parsed AI response has no file changes");
                return null;
            }

            List<FileChange> fileChanges = response.getFileChanges().stream()
                    .map(fc -> FileChange.builder()
                            .path(fc.getPath())
                            .content(fc.getContent() != null ? fc.getContent() : "")
                            .operation(parseOperation(fc.getOperation()))
                            .build())
                    .toList();

            return ImplementationPlan.builder()
                    .summary(response.getSummary())
                    .fileChanges(fileChanges)
                    .build();
        } catch (JacksonException e) {
            log.error("Failed to parse AI response as JSON: {}", e.getMessage());
            return null;
        }
    }

    private FileChange.Operation parseOperation(String operation) {
        if (operation == null) return FileChange.Operation.CREATE;
        return switch (operation.toUpperCase()) {
            case "UPDATE" -> FileChange.Operation.UPDATE;
            case "DELETE" -> FileChange.Operation.DELETE;
            default -> FileChange.Operation.CREATE;
        };
    }

    private String buildPrBody(Long issueNumber, ImplementationPlan plan) {
        StringBuilder sb = new StringBuilder();
        sb.append(String.format("Fixes #%d%n%n", issueNumber));
        sb.append("## Summary\n\n");
        sb.append(plan.getSummary()).append("\n\n");
        sb.append("## Changes\n\n");
        for (FileChange fc : plan.getFileChanges()) {
            sb.append(String.format("- **%s**: `%s`%n", fc.getOperation(), fc.getPath()));
        }
        sb.append("\n---\n");
        sb.append("*This PR was automatically generated by the AI implementation agent. Please review carefully before merging.*\n");
        return sb.toString();
    }

    @Data
    @JsonIgnoreProperties(ignoreUnknown = true)
    static class AiImplementationResponse {
        private String summary;
        private List<AiFileChange> fileChanges;
    }

    @Data
    @JsonIgnoreProperties(ignoreUnknown = true)
    static class AiFileChange {
        private String path;
        private String operation;
        private String content;
    }
}
