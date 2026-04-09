package org.remus.giteabot.gitlab;

import org.junit.jupiter.api.Test;
import org.remus.giteabot.gitea.model.WebhookPayload;

import java.util.HashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

class GitLabWebhookPayloadTranslationTest {

    @Test
    void translateMergeRequestPayload_mapsAllFields() {
        Map<String, Object> attrs = new HashMap<>();
        attrs.put("id", 100);
        attrs.put("iid", 1);
        attrs.put("title", "Test MR");
        attrs.put("description", "Some changes");
        attrs.put("state", "opened");
        attrs.put("source_branch", "feature");
        attrs.put("target_branch", "main");
        attrs.put("last_commit", Map.of("id", "abc123"));

        Map<String, Object> project = new HashMap<>();
        project.put("id", 1);
        project.put("name", "testrepo");
        project.put("path_with_namespace", "testowner/testrepo");
        project.put("namespace", Map.of("path", "testowner"));

        Map<String, Object> gitlabPayload = new HashMap<>();
        gitlabPayload.put("object_attributes", attrs);
        gitlabPayload.put("project", project);
        gitlabPayload.put("user", Map.of("username", "testuser"));

        WebhookPayload result = GitLabWebhookController.translateMergeRequestPayload(gitlabPayload, attrs);

        assertNotNull(result.getPullRequest());
        assertEquals(100L, result.getPullRequest().getId());
        assertEquals(1L, result.getPullRequest().getNumber());
        assertEquals("Test MR", result.getPullRequest().getTitle());
        assertEquals("Some changes", result.getPullRequest().getBody());
        assertEquals("open", result.getPullRequest().getState());
        assertEquals("feature", result.getPullRequest().getHead().getRef());
        assertEquals("abc123", result.getPullRequest().getHead().getSha());
        assertEquals("main", result.getPullRequest().getBase().getRef());

        assertNotNull(result.getRepository());
        assertEquals("testrepo", result.getRepository().getName());
        assertEquals("testowner/testrepo", result.getRepository().getFullName());
        assertEquals("testowner", result.getRepository().getOwner().getLogin());

        assertNotNull(result.getSender());
        assertEquals("testuser", result.getSender().getLogin());
    }

    @Test
    void translateMergeRequestPayload_closedState_mappedCorrectly() {
        Map<String, Object> attrs = new HashMap<>();
        attrs.put("id", 100);
        attrs.put("iid", 1);
        attrs.put("title", "Closed MR");
        attrs.put("state", "closed");
        attrs.put("source_branch", "feature");
        attrs.put("target_branch", "main");

        Map<String, Object> gitlabPayload = new HashMap<>();
        gitlabPayload.put("object_attributes", attrs);
        gitlabPayload.put("project", Map.of("id", 1, "name", "repo",
                "path_with_namespace", "owner/repo",
                "namespace", Map.of("path", "owner")));

        WebhookPayload result = GitLabWebhookController.translateMergeRequestPayload(gitlabPayload, attrs);

        assertEquals("closed", result.getPullRequest().getState());
    }

    @Test
    void translateMergeRequestPayload_mergedState_mappedToClosed() {
        Map<String, Object> attrs = new HashMap<>();
        attrs.put("id", 100);
        attrs.put("iid", 1);
        attrs.put("title", "Merged MR");
        attrs.put("state", "merged");
        attrs.put("source_branch", "feature");
        attrs.put("target_branch", "main");

        Map<String, Object> gitlabPayload = new HashMap<>();
        gitlabPayload.put("object_attributes", attrs);
        gitlabPayload.put("project", Map.of("id", 1, "name", "repo",
                "path_with_namespace", "owner/repo",
                "namespace", Map.of("path", "owner")));

        WebhookPayload result = GitLabWebhookController.translateMergeRequestPayload(gitlabPayload, attrs);

        assertEquals("closed", result.getPullRequest().getState());
    }

    @Test
    void translateNotePayload_mergeRequestNote_mapsAllFields() {
        Map<String, Object> noteAttrs = new HashMap<>();
        noteAttrs.put("id", 42);
        noteAttrs.put("note", "@bot please review");
        noteAttrs.put("noteable_type", "MergeRequest");
        noteAttrs.put("author", Map.of("username", "testuser"));

        Map<String, Object> mr = new HashMap<>();
        mr.put("id", 100);
        mr.put("iid", 1);
        mr.put("title", "Test MR");
        mr.put("description", "Some changes");

        Map<String, Object> project = new HashMap<>();
        project.put("id", 1);
        project.put("name", "testrepo");
        project.put("path_with_namespace", "testowner/testrepo");
        project.put("namespace", Map.of("path", "testowner"));

        Map<String, Object> gitlabPayload = new HashMap<>();
        gitlabPayload.put("object_attributes", noteAttrs);
        gitlabPayload.put("merge_request", mr);
        gitlabPayload.put("project", project);
        gitlabPayload.put("user", Map.of("username", "testuser"));

        WebhookPayload result = GitLabWebhookController.translateNotePayload(gitlabPayload, noteAttrs);

        assertNotNull(result.getComment());
        assertEquals(42L, result.getComment().getId());
        assertEquals("@bot please review", result.getComment().getBody());
        assertEquals("testuser", result.getComment().getUser().getLogin());

        assertNotNull(result.getIssue());
        assertEquals(1L, result.getIssue().getNumber());
        assertEquals("Test MR", result.getIssue().getTitle());

        assertNotNull(result.getPullRequest());
        assertEquals(1L, result.getPullRequest().getNumber());
    }

    @Test
    void translateNotePayload_issueNote_mapsIssueFields() {
        Map<String, Object> noteAttrs = new HashMap<>();
        noteAttrs.put("id", 55);
        noteAttrs.put("note", "@bot implement this");
        noteAttrs.put("noteable_type", "Issue");
        noteAttrs.put("author", Map.of("username", "devuser"));

        Map<String, Object> issue = new HashMap<>();
        issue.put("iid", 5);
        issue.put("title", "Add feature X");
        issue.put("description", "Please add X");

        Map<String, Object> project = new HashMap<>();
        project.put("id", 1);
        project.put("name", "testrepo");
        project.put("path_with_namespace", "testowner/testrepo");
        project.put("namespace", Map.of("path", "testowner"));

        Map<String, Object> gitlabPayload = new HashMap<>();
        gitlabPayload.put("object_attributes", noteAttrs);
        gitlabPayload.put("issue", issue);
        gitlabPayload.put("project", project);
        gitlabPayload.put("user", Map.of("username", "devuser"));

        WebhookPayload result = GitLabWebhookController.translateNotePayload(gitlabPayload, noteAttrs);

        assertNotNull(result.getIssue());
        assertEquals(5L, result.getIssue().getNumber());
        assertEquals("Add feature X", result.getIssue().getTitle());
        assertEquals("Please add X", result.getIssue().getBody());
        assertNull(result.getPullRequest());
    }

    @Test
    void encodeProjectPath_encodesCorrectly() {
        assertEquals("owner%2Frepo", GitLabApiClient.encodeProjectPath("owner", "repo"));
        assertEquals("my-org%2Fmy-project", GitLabApiClient.encodeProjectPath("my-org", "my-project"));
    }
}
