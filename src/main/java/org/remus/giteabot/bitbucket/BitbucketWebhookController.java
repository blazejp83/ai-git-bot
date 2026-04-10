package org.remus.giteabot.bitbucket;

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
 * Webhook controller for Bitbucket Cloud events.
 * <p>
 * Receives Bitbucket Cloud webhook payloads and translates them into the common
 * {@link WebhookPayload} model used by the rest of the application, then
 * delegates to {@link BotWebhookService} for actual processing.
 * <p>
 * Bitbucket Cloud event types are delivered via the {@code X-Event-Key} header.
 * Supported events: pullrequest:created, pullrequest:updated,
 * pullrequest:fulfilled, pullrequest:rejected, pullrequest:comment_created.
 */
@Slf4j
@RestController
@RequestMapping("/api/bitbucket-webhook")
public class BitbucketWebhookController {

    private final BotService botService;
    private final BotWebhookService botWebhookService;

    public BitbucketWebhookController(BotService botService,
                                      BotWebhookService botWebhookService) {
        this.botService = botService;
        this.botWebhookService = botWebhookService;
    }

    /**
     * Per-bot Bitbucket webhook endpoint. Routes by webhook secret like the Gitea/GitHub controllers.
     * Bitbucket event type is determined from the {@code X-Event-Key} header.
     */
    @PostMapping("/{webhookSecret}")
    public ResponseEntity<String> handleBitbucketWebhook(
            @PathVariable String webhookSecret,
            @RequestHeader(value = "X-Event-Key", required = false) String eventKey,
            @RequestBody Map<String, Object> payload) {
        return botService.findByWebhookSecret(webhookSecret)
                .map(bot -> {
                    if (!bot.isEnabled()) {
                        log.debug("Bot '{}' is disabled, ignoring Bitbucket webhook", bot.getName());
                        return ResponseEntity.ok("bot disabled");
                    }
                    botService.incrementWebhookCallCount(bot);
                    log.info("Bitbucket webhook received for bot '{}' (event={}, git={})",
                            bot.getName(), eventKey, bot.getGitIntegration().getName());
                    return handleEvent(bot, eventKey, payload);
                })
                .orElseGet(() -> {
                    log.warn("No bot found for webhook secret: {}...",
                            webhookSecret.substring(0, Math.min(8, webhookSecret.length())));
                    return ResponseEntity.notFound().build();
                });
    }

    private ResponseEntity<String> handleEvent(Bot bot, String eventKey, Map<String, Object> raw) {
        if (eventKey == null) {
            log.warn("Missing X-Event-Key header");
            return ResponseEntity.ok("ignored");
        }

        WebhookPayload payload = translatePayload(eventKey, raw);
        if (payload == null) {
            return ResponseEntity.ok("ignored");
        }

        // Ignore events triggered by the bot itself
        if (botWebhookService.isBotUser(bot, payload)) {
            log.debug("Ignoring Bitbucket webhook event from bot's own user '{}'", bot.getUsername());
            return ResponseEntity.ok("ignored");
        }

        String botAlias = botWebhookService.getBotAlias(bot);

        return switch (eventKey) {
            case "pullrequest:created", "pullrequest:updated" ->
                    handlePullRequestOpenedOrUpdated(bot, payload);
            case "pullrequest:fulfilled", "pullrequest:rejected" ->
                    handlePullRequestClosed(bot, payload);
            case "pullrequest:comment_created" ->
                    handlePullRequestComment(bot, payload, botAlias);
            default -> {
                log.debug("Unhandled Bitbucket event key: {}", eventKey);
                yield ResponseEntity.ok("ignored");
            }
        };
    }

    private ResponseEntity<String> handlePullRequestOpenedOrUpdated(Bot bot, WebhookPayload payload) {
        botWebhookService.reviewPullRequest(bot, payload);
        return ResponseEntity.ok("review triggered");
    }

    private ResponseEntity<String> handlePullRequestClosed(Bot bot, WebhookPayload payload) {
        botWebhookService.handlePrClosed(bot, payload);
        return ResponseEntity.ok("session closed");
    }

    private ResponseEntity<String> handlePullRequestComment(Bot bot, WebhookPayload payload,
                                                             String botAlias) {
        String body = payload.getComment() != null ? payload.getComment().getBody() : null;
        if (body == null || !body.contains(botAlias)) {
            return ResponseEntity.ok("ignored");
        }

        // Bitbucket inline comments have a path set via the "inline" field
        if (payload.getComment().getPath() != null) {
            botWebhookService.handleInlineComment(bot, payload);
            return ResponseEntity.ok("inline comment response triggered");
        }

        // General PR comment mentioning the bot
        botWebhookService.handleBotCommand(bot, payload);
        return ResponseEntity.ok("command received");
    }

    // ---- Bitbucket → WebhookPayload translation ----

    /**
     * Translates a raw Bitbucket Cloud webhook JSON payload into the common {@link WebhookPayload} model.
     * Returns {@code null} if the event key is unsupported.
     */
    @SuppressWarnings("unchecked")
    WebhookPayload translatePayload(String eventKey, Map<String, Object> raw) {
        return switch (eventKey) {
            case "pullrequest:created" -> translatePullRequestEvent(raw, "opened");
            case "pullrequest:updated" -> translatePullRequestEvent(raw, "synchronized");
            case "pullrequest:fulfilled" -> translatePullRequestEvent(raw, "closed");
            case "pullrequest:rejected" -> translatePullRequestEvent(raw, "closed");
            case "pullrequest:comment_created" -> translatePullRequestCommentEvent(raw);
            default -> null;
        };
    }

    @SuppressWarnings("unchecked")
    private WebhookPayload translatePullRequestEvent(Map<String, Object> raw, String action) {
        WebhookPayload payload = new WebhookPayload();
        payload.setAction(action);
        payload.setSender(extractActor(raw));
        payload.setRepository(extractRepository(raw));
        payload.setPullRequest(extractPullRequest(
                (Map<String, Object>) raw.get("pullrequest")));
        if (payload.getPullRequest() != null) {
            payload.setNumber(payload.getPullRequest().getNumber());
        }
        return payload;
    }

    @SuppressWarnings("unchecked")
    private WebhookPayload translatePullRequestCommentEvent(Map<String, Object> raw) {
        WebhookPayload payload = new WebhookPayload();
        payload.setAction("created");
        payload.setSender(extractActor(raw));
        payload.setRepository(extractRepository(raw));
        payload.setPullRequest(extractPullRequest(
                (Map<String, Object>) raw.get("pullrequest")));
        payload.setComment(extractComment((Map<String, Object>) raw.get("comment")));

        if (payload.getPullRequest() != null) {
            payload.setNumber(payload.getPullRequest().getNumber());
            // Build a synthetic issue for consistency with the Gitea webhook model
            WebhookPayload.Issue issue = new WebhookPayload.Issue();
            issue.setNumber(payload.getPullRequest().getNumber());
            issue.setTitle(payload.getPullRequest().getTitle());
            WebhookPayload.IssuePullRequest ipr = new WebhookPayload.IssuePullRequest();
            issue.setPullRequest(ipr);
            payload.setIssue(issue);
        }
        return payload;
    }

    // ---- Extraction helpers ----

    @SuppressWarnings("unchecked")
    private WebhookPayload.Owner extractActor(Map<String, Object> raw) {
        Map<String, Object> actor = (Map<String, Object>) raw.get("actor");
        if (actor == null) return null;
        WebhookPayload.Owner owner = new WebhookPayload.Owner();
        // Bitbucket Cloud uses "nickname" or "display_name" for user identification
        String nickname = (String) actor.get("nickname");
        owner.setLogin(nickname != null ? nickname : (String) actor.get("display_name"));
        return owner;
    }

    @SuppressWarnings("unchecked")
    private WebhookPayload.Repository extractRepository(Map<String, Object> raw) {
        Map<String, Object> repo = (Map<String, Object>) raw.get("repository");
        if (repo == null) return null;
        WebhookPayload.Repository repository = new WebhookPayload.Repository();
        repository.setName((String) repo.get("name"));
        repository.setFullName((String) repo.get("full_name"));

        // Extract UUID as ID if available
        String uuid = (String) repo.get("uuid");
        if (uuid != null) {
            repository.setId((long) uuid.hashCode());
        }

        // Extract workspace owner
        Map<String, Object> ownerMap = (Map<String, Object>) repo.get("owner");
        if (ownerMap != null) {
            WebhookPayload.Owner owner = new WebhookPayload.Owner();
            String nickname = (String) ownerMap.get("nickname");
            owner.setLogin(nickname != null ? nickname : (String) ownerMap.get("display_name"));
            repository.setOwner(owner);
        } else {
            // Fallback: extract workspace from full_name
            String fullName = (String) repo.get("full_name");
            if (fullName != null && fullName.contains("/")) {
                WebhookPayload.Owner owner = new WebhookPayload.Owner();
                owner.setLogin(fullName.substring(0, fullName.indexOf("/")));
                repository.setOwner(owner);
            }
        }

        return repository;
    }

    @SuppressWarnings("unchecked")
    private WebhookPayload.PullRequest extractPullRequest(Map<String, Object> pr) {
        if (pr == null) return null;
        WebhookPayload.PullRequest pullRequest = new WebhookPayload.PullRequest();
        pullRequest.setId(toLong(pr.get("id")));
        pullRequest.setNumber(toLong(pr.get("id"))); // Bitbucket uses "id" as PR number
        pullRequest.setTitle((String) pr.get("title"));
        pullRequest.setBody((String) pr.get("description"));
        pullRequest.setState((String) pr.get("state"));

        // Head (source branch)
        Map<String, Object> source = (Map<String, Object>) pr.get("source");
        if (source != null) {
            WebhookPayload.Head head = new WebhookPayload.Head();
            Map<String, Object> branch = (Map<String, Object>) source.get("branch");
            if (branch != null) {
                head.setRef((String) branch.get("name"));
            }
            Map<String, Object> commit = (Map<String, Object>) source.get("commit");
            if (commit != null) {
                head.setSha((String) commit.get("hash"));
            }
            pullRequest.setHead(head);
        }

        // Base (destination branch)
        Map<String, Object> destination = (Map<String, Object>) pr.get("destination");
        if (destination != null) {
            WebhookPayload.Head base = new WebhookPayload.Head();
            Map<String, Object> branch = (Map<String, Object>) destination.get("branch");
            if (branch != null) {
                base.setRef((String) branch.get("name"));
            }
            Map<String, Object> commit = (Map<String, Object>) destination.get("commit");
            if (commit != null) {
                base.setSha((String) commit.get("hash"));
            }
            pullRequest.setBase(base);
        }

        // Merged state
        pullRequest.setMerged("MERGED".equals(pr.get("state")));

        return pullRequest;
    }

    @SuppressWarnings("unchecked")
    private WebhookPayload.Comment extractComment(Map<String, Object> comment) {
        if (comment == null) return null;
        WebhookPayload.Comment c = new WebhookPayload.Comment();
        c.setId(toLong(comment.get("id")));

        // Bitbucket wraps comment body in "content" -> "raw"
        Map<String, Object> content = (Map<String, Object>) comment.get("content");
        if (content != null) {
            c.setBody((String) content.get("raw"));
        }

        // User
        Map<String, Object> user = (Map<String, Object>) comment.get("user");
        if (user != null) {
            WebhookPayload.Owner u = new WebhookPayload.Owner();
            String nickname = (String) user.get("nickname");
            u.setLogin(nickname != null ? nickname : (String) user.get("display_name"));
            c.setUser(u);
        }

        // Inline comment fields (path, line)
        Map<String, Object> inline = (Map<String, Object>) comment.get("inline");
        if (inline != null) {
            c.setPath((String) inline.get("path"));
            c.setLine(inline.get("to") instanceof Number n ? n.intValue() : null);
        }

        return c;
    }

    private Long toLong(Object value) {
        if (value instanceof Number n) {
            return n.longValue();
        }
        return null;
    }
}
