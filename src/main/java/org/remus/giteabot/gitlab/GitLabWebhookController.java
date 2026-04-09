package org.remus.giteabot.gitlab;

import lombok.extern.slf4j.Slf4j;
import org.remus.giteabot.admin.Bot;
import org.remus.giteabot.admin.BotService;
import org.remus.giteabot.admin.BotWebhookService;
import org.remus.giteabot.gitea.model.WebhookPayload;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;

import java.util.List;
import java.util.Map;

/**
 * Webhook controller for GitLab events.
 * Receives GitLab-formatted webhooks and translates them to the common {@link WebhookPayload}
 * format before routing through {@link BotWebhookService}.
 * <p>
 * GitLab webhooks use a different payload structure than Gitea, so this controller handles
 * the translation layer while reusing the same bot routing and business logic.
 */
@Slf4j
@RestController
@RequestMapping("/api/gitlab-webhook")
public class GitLabWebhookController {

    private final BotService botService;
    private final BotWebhookService botWebhookService;

    public GitLabWebhookController(BotService botService,
                                   BotWebhookService botWebhookService) {
        this.botService = botService;
        this.botWebhookService = botWebhookService;
    }

    /**
     * Per-bot GitLab webhook endpoint. Uses the same bot webhook secret as path parameter.
     * Translates GitLab events into the common WebhookPayload format.
     */
    @PostMapping("/{webhookSecret}")
    public ResponseEntity<String> handleGitLabWebhook(
            @PathVariable String webhookSecret,
            @RequestHeader(value = "X-Gitlab-Event", required = false) String gitlabEvent,
            @RequestBody Map<String, Object> gitlabPayload) {

        return botService.findByWebhookSecret(webhookSecret)
                .map(bot -> {
                    if (!bot.isEnabled()) {
                        log.debug("Bot '{}' is disabled, ignoring GitLab webhook", bot.getName());
                        return ResponseEntity.ok("bot disabled");
                    }
                    botService.incrementWebhookCallCount(bot);
                    log.info("GitLab webhook received for bot '{}' (event={})",
                            bot.getName(), gitlabEvent);
                    return handleGitLabEvent(bot, gitlabEvent, gitlabPayload);
                })
                .orElseGet(() -> {
                    log.warn("No bot found for webhook secret: {}...",
                            webhookSecret.substring(0, Math.min(8, webhookSecret.length())));
                    return ResponseEntity.notFound().build();
                });
    }

    /**
     * Routes a GitLab webhook event by translating the payload and delegating to BotWebhookService.
     */
    @SuppressWarnings("unchecked")
    private ResponseEntity<String> handleGitLabEvent(Bot bot, String gitlabEvent,
                                                      Map<String, Object> gitlabPayload) {
        if (gitlabEvent == null) {
            return ResponseEntity.ok("ignored");
        }

        return switch (gitlabEvent) {
            case "Merge Request Hook" -> handleMergeRequestEvent(bot, gitlabPayload);
            case "Note Hook" -> handleNoteEvent(bot, gitlabPayload);
            default -> {
                log.debug("Ignoring unsupported GitLab event: {}", gitlabEvent);
                yield ResponseEntity.ok("ignored");
            }
        };
    }

    /**
     * Handles GitLab Merge Request Hook events.
     * Maps to PR opened/synchronized/closed events.
     */
    @SuppressWarnings("unchecked")
    private ResponseEntity<String> handleMergeRequestEvent(Bot bot, Map<String, Object> gitlabPayload) {
        Map<String, Object> attrs = (Map<String, Object>) gitlabPayload.get("object_attributes");
        if (attrs == null) {
            return ResponseEntity.ok("ignored");
        }

        String gitlabAction = (String) attrs.get("action");
        WebhookPayload payload = translateMergeRequestPayload(gitlabPayload, attrs);

        // Ignore events from the bot itself
        if (botWebhookService.isBotUser(bot, payload)) {
            log.debug("Ignoring GitLab event from bot's own user '{}'", bot.getUsername());
            return ResponseEntity.ok("ignored");
        }

        return switch (gitlabAction != null ? gitlabAction : "") {
            case "open" -> {
                payload.setAction("opened");
                botWebhookService.reviewPullRequest(bot, payload);
                yield ResponseEntity.ok("review triggered");
            }
            case "update" -> {
                payload.setAction("synchronized");
                botWebhookService.reviewPullRequest(bot, payload);
                yield ResponseEntity.ok("review triggered");
            }
            case "close", "merge" -> {
                payload.setAction("closed");
                botWebhookService.handlePrClosed(bot, payload);
                yield ResponseEntity.ok("session closed");
            }
            default -> ResponseEntity.ok("ignored");
        };
    }

    /**
     * Handles GitLab Note Hook events (comments on MRs and issues).
     * Maps to PR comment or issue comment events.
     */
    @SuppressWarnings("unchecked")
    private ResponseEntity<String> handleNoteEvent(Bot bot, Map<String, Object> gitlabPayload) {
        Map<String, Object> attrs = (Map<String, Object>) gitlabPayload.get("object_attributes");
        if (attrs == null) {
            return ResponseEntity.ok("ignored");
        }

        String noteableType = (String) attrs.get("noteable_type");
        String noteBody = (String) attrs.get("note");
        String botAlias = botWebhookService.getBotAlias(bot);

        if (noteBody == null || !noteBody.contains(botAlias)) {
            return ResponseEntity.ok("ignored");
        }

        if ("MergeRequest".equals(noteableType)) {
            return handleMergeRequestNote(bot, gitlabPayload, attrs);
        } else if ("Issue".equals(noteableType)) {
            return handleIssueNote(bot, gitlabPayload, attrs);
        }

        return ResponseEntity.ok("ignored");
    }

    /**
     * Handles a comment on a GitLab merge request.
     */
    @SuppressWarnings("unchecked")
    private ResponseEntity<String> handleMergeRequestNote(Bot bot, Map<String, Object> gitlabPayload,
                                                           Map<String, Object> noteAttrs) {
        WebhookPayload payload = translateNotePayload(gitlabPayload, noteAttrs);
        payload.setAction("created");

        // Ignore events from the bot itself
        if (botWebhookService.isBotUser(bot, payload)) {
            return ResponseEntity.ok("ignored");
        }

        // Check if this is an inline comment (has position info)
        Map<String, Object> position = (Map<String, Object>) noteAttrs.get("position");
        if (position != null) {
            String path = (String) position.get("new_path");
            if (path != null && !path.isBlank()) {
                payload.getComment().setPath(path);
                Object newLine = position.get("new_line");
                if (newLine instanceof Number) {
                    payload.getComment().setLine(((Number) newLine).intValue());
                }
                botWebhookService.handleInlineComment(bot, payload);
                return ResponseEntity.ok("inline comment response triggered");
            }
        }

        // Mark as a PR comment (set a dummy pull_request on the issue)
        if (payload.getIssue() != null) {
            WebhookPayload.IssuePullRequest issuePr = new WebhookPayload.IssuePullRequest();
            issuePr.setMerged(false);
            payload.getIssue().setPullRequest(issuePr);
        }

        botWebhookService.handleBotCommand(bot, payload);
        return ResponseEntity.ok("command received");
    }

    /**
     * Handles a comment on a GitLab issue (non-MR).
     */
    @SuppressWarnings("unchecked")
    private ResponseEntity<String> handleIssueNote(Bot bot, Map<String, Object> gitlabPayload,
                                                    Map<String, Object> noteAttrs) {
        WebhookPayload payload = translateNotePayload(gitlabPayload, noteAttrs);
        payload.setAction("created");

        // Ignore events from the bot itself
        if (botWebhookService.isBotUser(bot, payload)) {
            return ResponseEntity.ok("ignored");
        }

        botWebhookService.handleIssueComment(bot, payload);
        return ResponseEntity.ok("issue comment received");
    }

    // ---- Payload translation helpers ----

    /**
     * Translates a GitLab merge request webhook payload to the common WebhookPayload format.
     */
    @SuppressWarnings("unchecked")
    static WebhookPayload translateMergeRequestPayload(Map<String, Object> gitlabPayload,
                                                               Map<String, Object> attrs) {
        WebhookPayload payload = new WebhookPayload();

        // Pull request
        WebhookPayload.PullRequest pr = new WebhookPayload.PullRequest();
        pr.setId(toLong(attrs.get("id")));
        pr.setNumber(toLong(attrs.get("iid")));
        pr.setTitle((String) attrs.get("title"));
        pr.setBody((String) attrs.get("description"));
        pr.setState(mapMrState((String) attrs.get("state")));

        // Head and base
        WebhookPayload.Head head = new WebhookPayload.Head();
        head.setRef((String) attrs.get("source_branch"));
        Map<String, Object> lastCommit = (Map<String, Object>) attrs.get("last_commit");
        if (lastCommit != null) {
            head.setSha((String) lastCommit.get("id"));
        }
        pr.setHead(head);

        WebhookPayload.Head base = new WebhookPayload.Head();
        base.setRef((String) attrs.get("target_branch"));
        pr.setBase(base);

        payload.setPullRequest(pr);
        payload.setNumber(pr.getNumber());

        // Repository
        Map<String, Object> project = (Map<String, Object>) gitlabPayload.get("project");
        if (project != null) {
            payload.setRepository(translateRepository(project));
        }

        // Sender
        Map<String, Object> user = (Map<String, Object>) gitlabPayload.get("user");
        if (user != null) {
            WebhookPayload.Owner sender = new WebhookPayload.Owner();
            sender.setLogin((String) user.get("username"));
            payload.setSender(sender);
        }

        return payload;
    }

    /**
     * Translates a GitLab note (comment) webhook payload to the common WebhookPayload format.
     */
    @SuppressWarnings("unchecked")
    static WebhookPayload translateNotePayload(Map<String, Object> gitlabPayload,
                                                       Map<String, Object> noteAttrs) {
        WebhookPayload payload = new WebhookPayload();

        // Comment
        WebhookPayload.Comment comment = new WebhookPayload.Comment();
        comment.setId(toLong(noteAttrs.get("id")));
        comment.setBody((String) noteAttrs.get("note"));
        Map<String, Object> author = (Map<String, Object>) noteAttrs.get("author");
        if (author != null) {
            WebhookPayload.Owner commentUser = new WebhookPayload.Owner();
            commentUser.setLogin((String) author.get("username"));
            comment.setUser(commentUser);
        }
        payload.setComment(comment);

        // Repository
        Map<String, Object> project = (Map<String, Object>) gitlabPayload.get("project");
        if (project != null) {
            payload.setRepository(translateRepository(project));
        }

        // Sender
        Map<String, Object> user = (Map<String, Object>) gitlabPayload.get("user");
        if (user != null) {
            WebhookPayload.Owner sender = new WebhookPayload.Owner();
            sender.setLogin((String) user.get("username"));
            payload.setSender(sender);
        }

        // Populate issue/MR context from the noteable object
        String noteableType = (String) noteAttrs.get("noteable_type");
        if ("MergeRequest".equals(noteableType)) {
            Map<String, Object> mr = (Map<String, Object>) gitlabPayload.get("merge_request");
            if (mr != null) {
                // Set issue with MR number (needed for bot command routing)
                WebhookPayload.Issue issue = new WebhookPayload.Issue();
                issue.setNumber(toLong(mr.get("iid")));
                issue.setTitle((String) mr.get("title"));
                issue.setBody((String) mr.get("description"));
                payload.setIssue(issue);

                // Also set pullRequest for context
                WebhookPayload.PullRequest pr = new WebhookPayload.PullRequest();
                pr.setId(toLong(mr.get("id")));
                pr.setNumber(toLong(mr.get("iid")));
                pr.setTitle((String) mr.get("title"));
                pr.setBody((String) mr.get("description"));
                payload.setPullRequest(pr);
            }
        } else if ("Issue".equals(noteableType)) {
            Map<String, Object> issue = (Map<String, Object>) gitlabPayload.get("issue");
            if (issue != null) {
                WebhookPayload.Issue webhookIssue = new WebhookPayload.Issue();
                webhookIssue.setNumber(toLong(issue.get("iid")));
                webhookIssue.setTitle((String) issue.get("title"));
                webhookIssue.setBody((String) issue.get("description"));
                payload.setIssue(webhookIssue);
            }
        }

        return payload;
    }

    @SuppressWarnings("unchecked")
    private static WebhookPayload.Repository translateRepository(Map<String, Object> project) {
        WebhookPayload.Repository repo = new WebhookPayload.Repository();
        repo.setId(toLong(project.get("id")));
        repo.setName((String) project.get("name"));

        // GitLab uses path_with_namespace (e.g., "owner/repo")
        String pathWithNamespace = (String) project.get("path_with_namespace");
        repo.setFullName(pathWithNamespace);

        // Extract owner from the namespace
        WebhookPayload.Owner owner = new WebhookPayload.Owner();
        if (pathWithNamespace != null && pathWithNamespace.contains("/")) {
            owner.setLogin(pathWithNamespace.substring(0, pathWithNamespace.lastIndexOf('/')));
        }
        Map<String, Object> namespace = (Map<String, Object>) project.get("namespace");
        if (namespace != null) {
            String path = (String) namespace.get("path");
            if (path != null) {
                owner.setLogin(path);
            }
        }
        repo.setOwner(owner);

        return repo;
    }

    private static String mapMrState(String gitlabState) {
        if (gitlabState == null) return null;
        return switch (gitlabState) {
            case "opened" -> "open";
            case "closed" -> "closed";
            case "merged" -> "closed";
            default -> gitlabState;
        };
    }

    private static Long toLong(Object value) {
        if (value instanceof Number n) {
            return n.longValue();
        }
        return null;
    }
}
