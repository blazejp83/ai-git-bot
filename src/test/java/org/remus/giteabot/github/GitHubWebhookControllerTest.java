package org.remus.giteabot.github;

import tools.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;
import org.remus.giteabot.admin.AiIntegration;
import org.remus.giteabot.admin.Bot;
import org.remus.giteabot.admin.BotService;
import org.remus.giteabot.admin.BotWebhookService;
import org.remus.giteabot.admin.GitIntegration;
import org.remus.giteabot.gitea.model.WebhookPayload;
import org.remus.giteabot.repository.RepositoryType;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest;
import org.springframework.http.MediaType;
import org.springframework.test.context.ActiveProfiles;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;

import java.time.Instant;
import java.util.Optional;

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.*;

@WebMvcTest(GitHubWebhookController.class)
@ActiveProfiles("test")
class GitHubWebhookControllerTest {

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private BotService botService;

    @MockitoBean
    private BotWebhookService botWebhookService;

    @Test
    void handleGitHubWebhook_prOpened_triggersReview() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "action": "opened",
                    "number": 5,
                    "pull_request": {
                        "id": 100,
                        "number": 5,
                        "title": "Add feature",
                        "state": "open",
                        "head": {"ref": "feature-branch", "sha": "abc123"},
                        "base": {"ref": "main", "sha": "def456"}
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "developer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "pull_request")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("review triggered"));

        verify(botWebhookService).reviewPullRequest(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitHubWebhook_prSynchronize_triggersReview() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "action": "synchronize",
                    "number": 5,
                    "pull_request": {
                        "id": 100,
                        "number": 5,
                        "title": "Add feature",
                        "head": {"ref": "feature-branch", "sha": "abc123"},
                        "base": {"ref": "main", "sha": "def456"}
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "developer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "pull_request")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("review triggered"));

        verify(botWebhookService).reviewPullRequest(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitHubWebhook_prClosed_closesSession() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "action": "closed",
                    "number": 5,
                    "pull_request": {
                        "id": 100,
                        "number": 5,
                        "title": "Add feature"
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "developer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "pull_request")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("session closed"));

        verify(botWebhookService).handlePrClosed(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitHubWebhook_issueCommentWithBotMention_triggersCommand() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "action": "created",
                    "comment": {
                        "id": 42,
                        "body": "@ai_bot please review",
                        "user": {"login": "developer"}
                    },
                    "issue": {
                        "number": 5,
                        "title": "Add feature",
                        "pull_request": {"merged_at": null}
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "developer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "issue_comment")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("command received"));

        verify(botWebhookService).handleBotCommand(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitHubWebhook_issueCommentWithoutBotMention_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "action": "created",
                    "comment": {
                        "id": 42,
                        "body": "just a regular comment",
                        "user": {"login": "developer"}
                    },
                    "issue": {
                        "number": 5,
                        "title": "Add feature",
                        "pull_request": {}
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "developer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "issue_comment")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));

        verify(botWebhookService, never()).handleBotCommand(any(), any());
    }

    @Test
    void handleGitHubWebhook_pullRequestReview_submitted_triggersReviewHandler() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "action": "submitted",
                    "pull_request": {
                        "number": 5,
                        "title": "Add feature"
                    },
                    "review": {
                        "id": 10,
                        "state": "commented",
                        "body": "Some comments"
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "reviewer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "pull_request_review")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("review comments processing triggered"));

        verify(botWebhookService).handleReviewSubmitted(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitHubWebhook_pullRequestReviewComment_withBotMention_triggersInline() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "action": "created",
                    "comment": {
                        "id": 55,
                        "body": "@ai_bot explain this",
                        "user": {"login": "developer"},
                        "path": "src/Main.java",
                        "line": 10,
                        "diff_hunk": "@@ -1,5 +1,5 @@"
                    },
                    "pull_request": {
                        "number": 5,
                        "title": "Add feature"
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "developer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "pull_request_review_comment")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("inline comment response triggered"));

        verify(botWebhookService).handleInlineComment(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitHubWebhook_issueAssigned_triggersAgent() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "action": "assigned",
                    "issue": {
                        "number": 10,
                        "title": "Implement feature X",
                        "assignee": {"login": "ai_bot"},
                        "assignees": [{"login": "ai_bot"}]
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "manager"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "issues")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("agent triggered"));

        verify(botWebhookService).handleIssueAssigned(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitHubWebhook_botDisabled_returnsBotDisabled() throws Exception {
        Bot bot = createTestBot();
        bot.setEnabled(false);
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "action": "opened",
                    "pull_request": {
                        "number": 5,
                        "title": "Add feature"
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "developer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "pull_request")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("bot disabled"));

        verify(botWebhookService, never()).reviewPullRequest(any(), any());
    }

    @Test
    void handleGitHubWebhook_botNotFound_returns404() throws Exception {
        when(botService.findByWebhookSecret("unknown")).thenReturn(Optional.empty());

        String payload = """
                {
                    "action": "opened",
                    "pull_request": {"number": 1}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/unknown")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "pull_request")
                        .content(payload))
                .andExpect(status().isNotFound());
    }

    @Test
    void handleGitHubWebhook_botSender_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.isBotUser(eq(bot), any())).thenReturn(true);

        String payload = """
                {
                    "action": "opened",
                    "pull_request": {
                        "number": 5,
                        "title": "Add feature"
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "ai_bot"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "pull_request")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));

        verify(botWebhookService, never()).reviewPullRequest(any(), any());
    }

    @Test
    void handleGitHubWebhook_missingEventHeader_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "action": "opened",
                    "pull_request": {"number": 5}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));
    }

    @Test
    void handleGitHubWebhook_unknownEventType_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {"action": "completed"}
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "check_run")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));
    }

    @Test
    void handleGitHubWebhook_issueCommentOnPlainIssue_routesToIssueComment() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("gh-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "action": "created",
                    "comment": {
                        "id": 42,
                        "body": "@ai_bot do this task",
                        "user": {"login": "developer"}
                    },
                    "issue": {
                        "number": 10,
                        "title": "Feature request"
                    },
                    "repository": {
                        "id": 1,
                        "name": "myrepo",
                        "full_name": "owner/myrepo",
                        "owner": {"login": "owner"}
                    },
                    "sender": {"login": "developer"}
                }
                """;

        mockMvc.perform(post("/api/github-webhook/gh-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-GitHub-Event", "issue_comment")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("issue comment received"));

        verify(botWebhookService).handleIssueComment(eq(bot), any(WebhookPayload.class));
    }

    private Bot createTestBot() {
        Bot bot = new Bot();
        bot.setId(1L);
        bot.setName("Test Bot");
        bot.setUsername("ai_bot");
        bot.setWebhookSecret("gh-secret");
        bot.setEnabled(true);

        AiIntegration ai = new AiIntegration();
        ai.setId(1L);
        ai.setName("Test AI");
        ai.setProviderType("anthropic");
        ai.setApiUrl("http://localhost:8081");
        ai.setModel("claude-sonnet-4-20250514");
        ai.setMaxTokens(4096);
        ai.setMaxDiffCharsPerChunk(120000);
        ai.setMaxDiffChunks(8);
        ai.setRetryTruncatedChunkChars(60000);
        ai.setCreatedAt(Instant.now());
        ai.setUpdatedAt(Instant.now());
        bot.setAiIntegration(ai);

        GitIntegration git = new GitIntegration();
        git.setId(1L);
        git.setName("Test GitHub");
        git.setProviderType(RepositoryType.GITHUB);
        git.setUrl("https://api.github.com");
        git.setToken("ghp_test_token");
        git.setCreatedAt(Instant.now());
        git.setUpdatedAt(Instant.now());
        bot.setGitIntegration(git);

        return bot;
    }
}
