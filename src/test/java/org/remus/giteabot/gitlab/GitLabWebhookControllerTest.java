package org.remus.giteabot.gitlab;

import org.junit.jupiter.api.Test;
import org.remus.giteabot.admin.*;
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

@WebMvcTest(GitLabWebhookController.class)
@ActiveProfiles("test")
class GitLabWebhookControllerTest {

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private BotService botService;

    @MockitoBean
    private BotWebhookService botWebhookService;

    @Test
    void handleGitLabWebhook_mergeRequestOpened_triggersReview() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "object_kind": "merge_request",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 100,
                        "iid": 1,
                        "title": "Test MR",
                        "description": "Some changes",
                        "state": "opened",
                        "action": "open",
                        "source_branch": "feature",
                        "target_branch": "main",
                        "last_commit": {"id": "abc123"}
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Merge Request Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("review triggered"));

        verify(botService).incrementWebhookCallCount(bot);
        verify(botWebhookService).reviewPullRequest(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitLabWebhook_mergeRequestUpdated_triggersReview() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "object_kind": "merge_request",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 100,
                        "iid": 1,
                        "title": "Test MR",
                        "description": "Some changes",
                        "state": "opened",
                        "action": "update",
                        "source_branch": "feature",
                        "target_branch": "main",
                        "last_commit": {"id": "def456"}
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Merge Request Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("review triggered"));

        verify(botWebhookService).reviewPullRequest(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitLabWebhook_mergeRequestClosed_closesSession() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "object_kind": "merge_request",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 100,
                        "iid": 1,
                        "title": "Test MR",
                        "state": "closed",
                        "action": "close",
                        "source_branch": "feature",
                        "target_branch": "main"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Merge Request Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("session closed"));

        verify(botWebhookService).handlePrClosed(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitLabWebhook_mergeRequestMerged_closesSession() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "object_kind": "merge_request",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 100,
                        "iid": 1,
                        "title": "Test MR",
                        "state": "merged",
                        "action": "merge",
                        "source_branch": "feature",
                        "target_branch": "main"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Merge Request Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("session closed"));

        verify(botWebhookService).handlePrClosed(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitLabWebhook_noteOnMergeRequest_triggersCommand() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "object_kind": "note",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 42,
                        "note": "@ai_bot please explain this code",
                        "noteable_type": "MergeRequest",
                        "author": {"username": "testuser"}
                    },
                    "merge_request": {
                        "id": 100,
                        "iid": 1,
                        "title": "Test MR",
                        "description": "Some changes"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Note Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("command received"));

        verify(botWebhookService).handleBotCommand(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitLabWebhook_noteOnIssue_triggersIssueComment() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "object_kind": "note",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 55,
                        "note": "@ai_bot implement this feature",
                        "noteable_type": "Issue",
                        "author": {"username": "testuser"}
                    },
                    "issue": {
                        "iid": 5,
                        "title": "Add feature X",
                        "description": "Please add X"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Note Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("issue comment received"));

        verify(botWebhookService).handleIssueComment(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitLabWebhook_noteWithoutBotMention_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "object_kind": "note",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 42,
                        "note": "just a regular comment",
                        "noteable_type": "MergeRequest",
                        "author": {"username": "testuser"}
                    },
                    "merge_request": {
                        "iid": 1,
                        "title": "Test MR"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Note Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));

        verify(botWebhookService, never()).handleBotCommand(any(), any());
    }

    @Test
    void handleGitLabWebhook_inlineNoteOnMergeRequest_triggersInlineHandler() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "object_kind": "note",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 55,
                        "note": "@ai_bot explain this",
                        "noteable_type": "MergeRequest",
                        "author": {"username": "testuser"},
                        "position": {
                            "new_path": "src/main/java/Foo.java",
                            "new_line": 15,
                            "position_type": "text"
                        }
                    },
                    "merge_request": {
                        "iid": 3,
                        "title": "Refactor PR"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Note Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("inline comment response triggered"));

        verify(botWebhookService).handleInlineComment(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleGitLabWebhook_botDisabled_returnsBotDisabled() throws Exception {
        Bot bot = createTestBot();
        bot.setEnabled(false);
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "object_kind": "merge_request",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo"
                    },
                    "object_attributes": {
                        "iid": 1,
                        "action": "open",
                        "source_branch": "feature",
                        "target_branch": "main"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Merge Request Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("bot disabled"));

        verify(botWebhookService, never()).reviewPullRequest(any(), any());
    }

    @Test
    void handleGitLabWebhook_botNotFound_returns404() throws Exception {
        when(botService.findByWebhookSecret("unknown-secret")).thenReturn(Optional.empty());

        String payload = """
                {
                    "object_kind": "merge_request",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo"
                    },
                    "object_attributes": {
                        "iid": 1,
                        "action": "open"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/unknown-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Merge Request Hook")
                        .content(payload))
                .andExpect(status().isNotFound());

        verify(botWebhookService, never()).reviewPullRequest(any(), any());
    }

    @Test
    void handleGitLabWebhook_unsupportedEvent_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {
                    "object_kind": "push",
                    "user": {"username": "testuser"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Push Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));

        verify(botWebhookService, never()).reviewPullRequest(any(), any());
    }

    @Test
    void handleGitLabWebhook_botSender_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("test-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.isBotUser(eq(bot), any())).thenReturn(true);

        String payload = """
                {
                    "object_kind": "merge_request",
                    "user": {"username": "ai_bot"},
                    "project": {
                        "id": 1,
                        "name": "testrepo",
                        "path_with_namespace": "testowner/testrepo",
                        "namespace": {"path": "testowner"}
                    },
                    "object_attributes": {
                        "id": 100,
                        "iid": 1,
                        "title": "Test MR",
                        "state": "opened",
                        "action": "open",
                        "source_branch": "feature",
                        "target_branch": "main"
                    }
                }
                """;

        mockMvc.perform(post("/api/gitlab-webhook/test-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Gitlab-Event", "Merge Request Hook")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));

        verify(botWebhookService, never()).reviewPullRequest(any(), any());
    }

    private Bot createTestBot() {
        Bot bot = new Bot();
        bot.setId(1L);
        bot.setName("Test Bot");
        bot.setUsername("ai_bot");
        bot.setWebhookSecret("test-secret");
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
        git.setName("Test GitLab");
        git.setProviderType(RepositoryType.GITLAB);
        git.setUrl("http://localhost:8929");
        git.setToken("test-token");
        git.setCreatedAt(Instant.now());
        git.setUpdatedAt(Instant.now());
        bot.setGitIntegration(git);

        return bot;
    }
}
