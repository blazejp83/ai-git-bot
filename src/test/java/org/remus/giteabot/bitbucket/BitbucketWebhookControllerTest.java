package org.remus.giteabot.bitbucket;

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

@WebMvcTest(BitbucketWebhookController.class)
@ActiveProfiles("test")
class BitbucketWebhookControllerTest {

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private BotService botService;

    @MockitoBean
    private BotWebhookService botWebhookService;

    @Test
    void handleBitbucketWebhook_prCreated_triggersReview() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));

        String payload = createPullRequestPayload();

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:created")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("review triggered"));

        verify(botWebhookService).reviewPullRequest(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleBitbucketWebhook_prUpdated_triggersReview() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));

        String payload = createPullRequestPayload();

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:updated")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("review triggered"));

        verify(botWebhookService).reviewPullRequest(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleBitbucketWebhook_prFulfilled_closesSession() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));

        String payload = createPullRequestPayload();

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:fulfilled")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("session closed"));

        verify(botWebhookService).handlePrClosed(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleBitbucketWebhook_prRejected_closesSession() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));

        String payload = createPullRequestPayload();

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:rejected")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("session closed"));

        verify(botWebhookService).handlePrClosed(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleBitbucketWebhook_commentWithBotMention_triggersCommand() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "actor": {"nickname": "developer", "display_name": "Developer"},
                    "pullrequest": {
                        "id": 5,
                        "title": "Add feature",
                        "source": {"branch": {"name": "feature"}, "commit": {"hash": "abc"}},
                        "destination": {"branch": {"name": "main"}, "commit": {"hash": "def"}}
                    },
                    "comment": {
                        "id": 42,
                        "content": {"raw": "@ai_bot please review"},
                        "user": {"nickname": "developer", "display_name": "Developer"}
                    },
                    "repository": {
                        "name": "myrepo",
                        "full_name": "workspace/myrepo",
                        "owner": {"nickname": "workspace"}
                    }
                }
                """;

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:comment_created")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("command received"));

        verify(botWebhookService).handleBotCommand(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleBitbucketWebhook_commentWithoutBotMention_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "actor": {"nickname": "developer", "display_name": "Developer"},
                    "pullrequest": {
                        "id": 5,
                        "title": "Add feature",
                        "source": {"branch": {"name": "feature"}, "commit": {"hash": "abc"}},
                        "destination": {"branch": {"name": "main"}, "commit": {"hash": "def"}}
                    },
                    "comment": {
                        "id": 42,
                        "content": {"raw": "just a regular comment"},
                        "user": {"nickname": "developer", "display_name": "Developer"}
                    },
                    "repository": {
                        "name": "myrepo",
                        "full_name": "workspace/myrepo",
                        "owner": {"nickname": "workspace"}
                    }
                }
                """;

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:comment_created")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));

        verify(botWebhookService, never()).handleBotCommand(any(), any());
    }

    @Test
    void handleBitbucketWebhook_inlineCommentWithBotMention_triggersInline() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.getBotAlias(bot)).thenReturn("@ai_bot");

        String payload = """
                {
                    "actor": {"nickname": "developer", "display_name": "Developer"},
                    "pullrequest": {
                        "id": 5,
                        "title": "Add feature",
                        "source": {"branch": {"name": "feature"}, "commit": {"hash": "abc"}},
                        "destination": {"branch": {"name": "main"}, "commit": {"hash": "def"}}
                    },
                    "comment": {
                        "id": 55,
                        "content": {"raw": "@ai_bot explain this"},
                        "user": {"nickname": "developer", "display_name": "Developer"},
                        "inline": {"path": "src/Main.java", "to": 10}
                    },
                    "repository": {
                        "name": "myrepo",
                        "full_name": "workspace/myrepo",
                        "owner": {"nickname": "workspace"}
                    }
                }
                """;

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:comment_created")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("inline comment response triggered"));

        verify(botWebhookService).handleInlineComment(eq(bot), any(WebhookPayload.class));
    }

    @Test
    void handleBitbucketWebhook_botDisabled_returnsBotDisabled() throws Exception {
        Bot bot = createTestBot();
        bot.setEnabled(false);
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));

        String payload = createPullRequestPayload();

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:created")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("bot disabled"));

        verify(botWebhookService, never()).reviewPullRequest(any(), any());
    }

    @Test
    void handleBitbucketWebhook_botNotFound_returns404() throws Exception {
        when(botService.findByWebhookSecret("unknown")).thenReturn(Optional.empty());

        String payload = createPullRequestPayload();

        mockMvc.perform(post("/api/bitbucket-webhook/unknown")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:created")
                        .content(payload))
                .andExpect(status().isNotFound());
    }

    @Test
    void handleBitbucketWebhook_botSender_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));
        when(botWebhookService.isBotUser(eq(bot), any())).thenReturn(true);

        String payload = """
                {
                    "actor": {"nickname": "ai_bot", "display_name": "AI Bot"},
                    "pullrequest": {
                        "id": 5,
                        "title": "Add feature",
                        "source": {"branch": {"name": "feature"}, "commit": {"hash": "abc"}},
                        "destination": {"branch": {"name": "main"}, "commit": {"hash": "def"}}
                    },
                    "repository": {
                        "name": "myrepo",
                        "full_name": "workspace/myrepo",
                        "owner": {"nickname": "workspace"}
                    }
                }
                """;

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "pullrequest:created")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));

        verify(botWebhookService, never()).reviewPullRequest(any(), any());
    }

    @Test
    void handleBitbucketWebhook_missingEventHeader_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));

        String payload = createPullRequestPayload();

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));
    }

    @Test
    void handleBitbucketWebhook_unknownEventKey_ignored() throws Exception {
        Bot bot = createTestBot();
        when(botService.findByWebhookSecret("bb-secret")).thenReturn(Optional.of(bot));

        String payload = """
                {"action": "completed"}
                """;

        mockMvc.perform(post("/api/bitbucket-webhook/bb-secret")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("X-Event-Key", "repo:push")
                        .content(payload))
                .andExpect(status().isOk())
                .andExpect(content().string("ignored"));
    }

    private String createPullRequestPayload() {
        return """
                {
                    "actor": {"nickname": "developer", "display_name": "Developer User"},
                    "pullrequest": {
                        "id": 5,
                        "title": "Add feature",
                        "description": "Feature description",
                        "state": "OPEN",
                        "source": {
                            "branch": {"name": "feature-branch"},
                            "commit": {"hash": "abc123"}
                        },
                        "destination": {
                            "branch": {"name": "main"},
                            "commit": {"hash": "def456"}
                        }
                    },
                    "repository": {
                        "name": "myrepo",
                        "full_name": "workspace/myrepo",
                        "uuid": "{12345}",
                        "owner": {"nickname": "workspace", "display_name": "Workspace Owner"}
                    }
                }
                """;
    }

    private Bot createTestBot() {
        Bot bot = new Bot();
        bot.setId(1L);
        bot.setName("Test Bot");
        bot.setUsername("ai_bot");
        bot.setWebhookSecret("bb-secret");
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
        git.setName("Test Bitbucket");
        git.setProviderType(RepositoryType.BITBUCKET);
        git.setUrl("https://api.bitbucket.org/2.0");
        git.setToken("bb_test_token");
        git.setCreatedAt(Instant.now());
        git.setUpdatedAt(Instant.now());
        bot.setGitIntegration(git);

        return bot;
    }
}
