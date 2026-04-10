package org.remus.giteabot.github;

import org.junit.jupiter.api.Test;
import org.remus.giteabot.gitea.model.WebhookPayload;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for the GitHub → WebhookPayload translation logic in
 * {@link GitHubWebhookController#translatePayload(String, Map)}.
 */
class GitHubPayloadTranslationTest {

    private final GitHubWebhookController controller = new GitHubWebhookController(null, null);

    @Test
    void translatePullRequestOpened_mapsAllFields() {
        Map<String, Object> raw = Map.of(
                "action", "opened",
                "number", 5,
                "pull_request", Map.of(
                        "id", 100,
                        "number", 5,
                        "title", "Add feature",
                        "body", "Description",
                        "state", "open",
                        "head", Map.of("ref", "feature", "sha", "abc123"),
                        "base", Map.of("ref", "main", "sha", "def456")
                ),
                "repository", Map.of(
                        "id", 1,
                        "name", "myrepo",
                        "full_name", "owner/myrepo",
                        "owner", Map.of("login", "owner")
                ),
                "sender", Map.of("login", "developer")
        );

        WebhookPayload payload = controller.translatePayload("pull_request", raw);

        assertNotNull(payload);
        assertEquals("opened", payload.getAction());
        assertEquals("developer", payload.getSender().getLogin());
        assertEquals("myrepo", payload.getRepository().getName());
        assertEquals("owner/myrepo", payload.getRepository().getFullName());
        assertEquals("owner", payload.getRepository().getOwner().getLogin());
        assertNotNull(payload.getPullRequest());
        assertEquals(5L, payload.getPullRequest().getNumber());
        assertEquals("Add feature", payload.getPullRequest().getTitle());
        assertEquals("feature", payload.getPullRequest().getHead().getRef());
        assertEquals("main", payload.getPullRequest().getBase().getRef());
    }

    @Test
    void translatePullRequestSynchronize_mapsToSynchronized() {
        Map<String, Object> raw = Map.of(
                "action", "synchronize",
                "pull_request", Map.of(
                        "number", 5,
                        "title", "Add feature"
                ),
                "repository", Map.of(
                        "name", "myrepo",
                        "full_name", "owner/myrepo",
                        "owner", Map.of("login", "owner")
                ),
                "sender", Map.of("login", "developer")
        );

        WebhookPayload payload = controller.translatePayload("pull_request", raw);

        assertNotNull(payload);
        assertEquals("synchronized", payload.getAction());
    }

    @Test
    void translateIssueComment_mapsCommentAndIssue() {
        Map<String, Object> raw = Map.of(
                "action", "created",
                "comment", Map.of(
                        "id", 42,
                        "body", "@ai_bot help",
                        "user", Map.of("login", "developer")
                ),
                "issue", Map.of(
                        "number", 10,
                        "title", "Bug report",
                        "body", "Description"
                ),
                "repository", Map.of(
                        "name", "myrepo",
                        "full_name", "owner/myrepo",
                        "owner", Map.of("login", "owner")
                ),
                "sender", Map.of("login", "developer")
        );

        WebhookPayload payload = controller.translatePayload("issue_comment", raw);

        assertNotNull(payload);
        assertEquals("created", payload.getAction());
        assertEquals(42L, payload.getComment().getId());
        assertEquals("@ai_bot help", payload.getComment().getBody());
        assertEquals("developer", payload.getComment().getUser().getLogin());
        assertEquals(10L, payload.getIssue().getNumber());
        assertNull(payload.getIssue().getPullRequest());
    }

    @Test
    void translateIssueComment_onPullRequest_setsIssuePullRequest() {
        Map<String, Object> raw = Map.of(
                "action", "created",
                "comment", Map.of(
                        "id", 42,
                        "body", "@ai_bot help",
                        "user", Map.of("login", "developer")
                ),
                "issue", Map.of(
                        "number", 5,
                        "title", "PR Title",
                        "pull_request", Map.of("merged_at", "null_placeholder")
                ),
                "repository", Map.of(
                        "name", "myrepo",
                        "full_name", "owner/myrepo",
                        "owner", Map.of("login", "owner")
                ),
                "sender", Map.of("login", "developer")
        );

        WebhookPayload payload = controller.translatePayload("issue_comment", raw);

        assertNotNull(payload);
        assertNotNull(payload.getIssue().getPullRequest());
    }

    @Test
    void translatePullRequestReview_mapsReviewAndMapsAction() {
        Map<String, Object> raw = Map.of(
                "action", "submitted",
                "pull_request", Map.of(
                        "number", 5,
                        "title", "Add feature"
                ),
                "review", Map.of(
                        "id", 10,
                        "state", "commented",
                        "body", "Some review"
                ),
                "repository", Map.of(
                        "name", "myrepo",
                        "full_name", "owner/myrepo",
                        "owner", Map.of("login", "owner")
                ),
                "sender", Map.of("login", "reviewer")
        );

        WebhookPayload payload = controller.translatePayload("pull_request_review", raw);

        assertNotNull(payload);
        // GitHub "submitted" maps to Gitea "reviewed"
        assertEquals("reviewed", payload.getAction());
        assertNotNull(payload.getReview());
        assertEquals(10L, payload.getReview().getId());
        assertEquals("commented", payload.getReview().getType());
        assertEquals("Some review", payload.getReview().getContent());
    }

    @Test
    void translatePullRequestReviewComment_mapsInlineCommentFields() {
        Map<String, Object> raw = Map.of(
                "action", "created",
                "comment", Map.of(
                        "id", 55,
                        "body", "@ai_bot explain this",
                        "user", Map.of("login", "developer"),
                        "path", "src/Main.java",
                        "line", 10,
                        "diff_hunk", "@@ -1,5 +1,5 @@",
                        "pull_request_review_id", 20
                ),
                "pull_request", Map.of(
                        "number", 5,
                        "title", "Add feature"
                ),
                "repository", Map.of(
                        "name", "myrepo",
                        "full_name", "owner/myrepo",
                        "owner", Map.of("login", "owner")
                ),
                "sender", Map.of("login", "developer")
        );

        WebhookPayload payload = controller.translatePayload("pull_request_review_comment", raw);

        assertNotNull(payload);
        assertNotNull(payload.getComment());
        assertEquals("src/Main.java", payload.getComment().getPath());
        assertEquals(10, payload.getComment().getLine());
        assertEquals("@@ -1,5 +1,5 @@", payload.getComment().getDiffHunk());
        assertEquals(20L, payload.getComment().getPullRequestReviewId());
        // Should also have a synthetic issue with pullRequest
        assertNotNull(payload.getIssue());
        assertNotNull(payload.getIssue().getPullRequest());
    }

    @Test
    void translateIssuesAssigned_mapsAssignee() {
        Map<String, Object> raw = Map.of(
                "action", "assigned",
                "issue", Map.of(
                        "number", 10,
                        "title", "Feature request",
                        "assignee", Map.of("login", "ai_bot"),
                        "assignees", java.util.List.of(Map.of("login", "ai_bot"))
                ),
                "repository", Map.of(
                        "name", "myrepo",
                        "full_name", "owner/myrepo",
                        "owner", Map.of("login", "owner")
                ),
                "sender", Map.of("login", "manager")
        );

        WebhookPayload payload = controller.translatePayload("issues", raw);

        assertNotNull(payload);
        assertEquals("assigned", payload.getAction());
        assertEquals("ai_bot", payload.getIssue().getAssignee().getLogin());
        assertEquals(1, payload.getIssue().getAssignees().size());
        assertEquals("ai_bot", payload.getIssue().getAssignees().getFirst().getLogin());
    }

    @Test
    void translateUnknownEvent_returnsNull() {
        assertNull(controller.translatePayload("check_run", Map.of("action", "completed")));
    }
}
