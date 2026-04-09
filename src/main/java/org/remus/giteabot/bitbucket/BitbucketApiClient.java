package org.remus.giteabot.bitbucket;

import lombok.extern.slf4j.Slf4j;
import org.remus.giteabot.bitbucket.model.BitbucketReview;
import org.remus.giteabot.bitbucket.model.BitbucketReviewComment;
import org.remus.giteabot.repository.RepositoryApiClient;
import org.remus.giteabot.repository.model.Review;
import org.remus.giteabot.repository.model.ReviewComment;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.web.client.RestClient;

import java.util.List;
import java.util.Map;

/**
 * Bitbucket Cloud implementation of {@link RepositoryApiClient}.
 * Uses the Bitbucket Cloud REST API 2.0.
 * <p>
 * Bitbucket Cloud uses workspace/repo-slug identifiers (mapped from owner/repo parameters).
 * Authentication is via Bearer token (OAuth access token or repository/workspace access token).
 */
@Slf4j
public class BitbucketApiClient implements RepositoryApiClient {

    private final RestClient restClient;
    private final String baseUrl;
    private final String token;

    public BitbucketApiClient(RestClient restClient, String baseUrl, String token) {
        this.restClient = restClient;
        this.baseUrl = baseUrl;
        this.token = token;
    }

    @Override
    public String getBaseUrl() {
        return baseUrl;
    }

    @Override
    public String getToken() {
        return token;
    }

    @Override
    public String getPullRequestDiff(String owner, String repo, Long pullNumber) {
        log.info("Fetching diff for PR #{} in {}/{}", pullNumber, owner, repo);
        return restClient.get()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/pullrequests/{pull_request_id}/diff",
                        owner, repo, pullNumber)
                .header("Accept", "text/plain")
                .retrieve()
                .body(String.class);
    }

    @Override
    public void postReviewComment(String owner, String repo, Long pullNumber, String body) {
        log.info("Posting review comment on PR #{} in {}/{}", pullNumber, owner, repo);
        restClient.post()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/pullrequests/{pull_request_id}/comments",
                        owner, repo, pullNumber)
                .body(Map.of("content", Map.of("raw", body)))
                .retrieve()
                .toBodilessEntity();
        log.info("Review comment posted successfully");
    }

    @Override
    public void postComment(String owner, String repo, Long issueNumber, String body) {
        log.info("Posting comment on issue #{} in {}/{}", issueNumber, owner, repo);
        restClient.post()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/issues/{issue_id}/comments",
                        owner, repo, issueNumber)
                .body(Map.of("content", Map.of("raw", body)))
                .retrieve()
                .toBodilessEntity();
        log.info("Comment posted successfully");
    }

    @Override
    public void addReaction(String owner, String repo, Long commentId, String reaction) {
        // Bitbucket Cloud does not support reactions on comments via REST API.
        log.debug("Reactions are not supported on Bitbucket Cloud; ignoring reaction '{}' on comment #{}",
                reaction, commentId);
    }

    @Override
    public void postInlineReviewComment(String owner, String repo, Long pullNumber,
                                        String filePath, int line, String body) {
        log.info("Posting inline comment on PR #{} in {}/{} at {}:{}", pullNumber, owner, repo, filePath, line);
        Map<String, Object> request = Map.of(
                "content", Map.of("raw", body),
                "inline", Map.of("path", filePath, "to", line)
        );
        restClient.post()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/pullrequests/{pull_request_id}/comments",
                        owner, repo, pullNumber)
                .body(request)
                .retrieve()
                .toBodilessEntity();
        log.info("Inline comment posted successfully");
    }

    @Override
    @SuppressWarnings("unchecked")
    public List<Review> getReviews(String owner, String repo, Long pullNumber) {
        log.info("Fetching comments (reviews) for PR #{} in {}/{}", pullNumber, owner, repo);
        Map<String, Object> result = restClient.get()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/pullrequests/{pull_request_id}/comments",
                        owner, repo, pullNumber)
                .retrieve()
                .body(new ParameterizedTypeReference<>() {});
        if (result != null && result.containsKey("values")) {
            // Filter to top-level comments only (no inline path)
            List<Map<String, Object>> values = (List<Map<String, Object>>) result.get("values");
            return values.stream()
                    .filter(v -> v.get("inline") == null)
                    .map(this::mapToReview)
                    .toList();
        }
        return List.of();
    }

    @Override
    @SuppressWarnings("unchecked")
    public List<ReviewComment> getReviewComments(String owner, String repo,
                                                 Long pullNumber, Long reviewId) {
        log.info("Fetching inline comments for PR #{} in {}/{}", pullNumber, owner, repo);
        Map<String, Object> result = restClient.get()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/pullrequests/{pull_request_id}/comments",
                        owner, repo, pullNumber)
                .retrieve()
                .body(new ParameterizedTypeReference<>() {});
        if (result != null && result.containsKey("values")) {
            List<Map<String, Object>> values = (List<Map<String, Object>>) result.get("values");
            return values.stream()
                    .filter(v -> v.get("inline") != null)
                    .map(this::mapToReviewComment)
                    .toList();
        }
        return List.of();
    }

    // ---- Repository operations for the issue implementation agent ----

    @Override
    @SuppressWarnings("unchecked")
    public String getDefaultBranch(String owner, String repo) {
        log.info("Fetching default branch for {}/{}", owner, repo);
        Map<String, Object> repoInfo = restClient.get()
                .uri("/2.0/repositories/{workspace}/{repo_slug}", owner, repo)
                .retrieve()
                .body(new ParameterizedTypeReference<>() {});
        if (repoInfo != null && repoInfo.containsKey("mainbranch")) {
            Map<String, Object> mainBranch = (Map<String, Object>) repoInfo.get("mainbranch");
            if (mainBranch != null && mainBranch.containsKey("name")) {
                return (String) mainBranch.get("name");
            }
        }
        return "main";
    }

    @Override
    @SuppressWarnings("unchecked")
    public List<Map<String, Object>> getRepositoryTree(String owner, String repo, String ref) {
        log.info("Fetching repository tree for {}/{} at ref={}", owner, repo, ref);
        Map<String, Object> result = restClient.get()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/src/{node}/?max_depth=100",
                        owner, repo, ref)
                .retrieve()
                .body(new ParameterizedTypeReference<>() {});
        if (result != null && result.containsKey("values")) {
            List<Map<String, Object>> values = (List<Map<String, Object>>) result.get("values");
            return values.stream()
                    .map(entry -> Map.<String, Object>of(
                            "path", entry.getOrDefault("path", ""),
                            "type", entry.getOrDefault("type", "")))
                    .toList();
        }
        return List.of();
    }

    @Override
    public String getFileContent(String owner, String repo, String path, String ref) {
        log.info("Fetching file content for {}/{}/{} at ref={}", owner, repo, path, ref);
        return restClient.get()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/src/{node}/{path}",
                        owner, repo, ref, path)
                .header("Accept", "application/octet-stream")
                .retrieve()
                .body(String.class);
    }

    @Override
    @SuppressWarnings("unchecked")
    public String getFileSha(String owner, String repo, String path, String ref) {
        log.info("Fetching file SHA for {}/{}/{} at ref={}", owner, repo, path, ref);
        Map<String, Object> result = restClient.get()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/src/{node}/{path}?format=meta",
                        owner, repo, ref, path)
                .retrieve()
                .body(new ParameterizedTypeReference<>() {});
        if (result != null && result.containsKey("commit")) {
            Map<String, Object> commit = (Map<String, Object>) result.get("commit");
            if (commit != null) {
                return (String) commit.get("hash");
            }
        }
        return null;
    }

    @Override
    public void createBranch(String owner, String repo, String branchName, String fromRef) {
        log.info("Creating branch '{}' from '{}' in {}/{}", branchName, fromRef, owner, repo);
        Map<String, Object> request = Map.of(
                "name", branchName,
                "target", Map.of("hash", fromRef)
        );
        restClient.post()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/refs/branches", owner, repo)
                .body(request)
                .retrieve()
                .toBodilessEntity();
        log.info("Branch '{}' created successfully", branchName);
    }

    @Override
    public void createOrUpdateFile(String owner, String repo, String path, String content,
                                   String message, String branch, String sha) {
        log.info("Creating/updating file {} on branch '{}' in {}/{}", path, branch, owner, repo);
        // Bitbucket uses form POST to /src for commits.
        restClient.post()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/src", owner, repo)
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(buildFormBody(message, branch, path, content))
                .retrieve()
                .toBodilessEntity();
        log.info("File {} committed successfully", path);
    }

    @Override
    public void deleteFile(String owner, String repo, String path, String message,
                           String branch, String sha) {
        log.info("Deleting file {} on branch '{}' in {}/{}", path, branch, owner, repo);
        restClient.post()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/src", owner, repo)
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(buildDeleteFormBody(message, branch, path))
                .retrieve()
                .toBodilessEntity();
        log.info("File {} deleted successfully", path);
    }

    @Override
    @SuppressWarnings("unchecked")
    public Long createPullRequest(String owner, String repo, String title, String body,
                                  String head, String base) {
        log.info("Creating pull request '{}' in {}/{} from {} to {}", title, owner, repo, head, base);
        Map<String, Object> request = Map.of(
                "title", title,
                "description", body,
                "source", Map.of("branch", Map.of("name", head)),
                "destination", Map.of("branch", Map.of("name", base)),
                "close_source_branch", true
        );
        Map<String, Object> result = restClient.post()
                .uri("/2.0/repositories/{workspace}/{repo_slug}/pullrequests", owner, repo)
                .body(request)
                .retrieve()
                .body(new ParameterizedTypeReference<>() {});
        Long prId = null;
        if (result != null && result.containsKey("id")) {
            prId = ((Number) result.get("id")).longValue();
        }
        log.info("Pull request created: #{}", prId);
        return prId;
    }

    @Override
    public void deleteBranch(String owner, String repo, String branchName) {
        log.info("Deleting branch '{}' in {}/{}", branchName, owner, repo);
        try {
            restClient.delete()
                    .uri("/2.0/repositories/{workspace}/{repo_slug}/refs/branches/{name}",
                            owner, repo, branchName)
                    .retrieve()
                    .toBodilessEntity();
            log.info("Branch '{}' deleted successfully", branchName);
        } catch (Exception e) {
            log.warn("Failed to delete branch '{}': {}", branchName, e.getMessage());
        }
    }

    // ---- Helper methods ----

    @SuppressWarnings("unchecked")
    private Review mapToReview(Map<String, Object> data) {
        BitbucketReview review = new BitbucketReview();
        review.setId(data.get("id") != null ? ((Number) data.get("id")).longValue() : null);

        if (data.get("content") instanceof Map<?, ?> contentMap) {
            BitbucketReview.BitbucketContent content = new BitbucketReview.BitbucketContent();
            content.setRaw((String) ((Map<String, Object>) contentMap).get("raw"));
            review.setContent(content);
        }

        if (data.get("user") instanceof Map<?, ?> userMap) {
            BitbucketReview.BitbucketUser user = new BitbucketReview.BitbucketUser();
            user.setDisplayName((String) ((Map<String, Object>) userMap).get("display_name"));
            user.setNickname((String) ((Map<String, Object>) userMap).get("nickname"));
            review.setUser(user);
        }

        review.setCreatedOn((String) data.get("created_on"));
        return review;
    }

    @SuppressWarnings("unchecked")
    private ReviewComment mapToReviewComment(Map<String, Object> data) {
        BitbucketReviewComment comment = new BitbucketReviewComment();
        comment.setId(data.get("id") != null ? ((Number) data.get("id")).longValue() : null);

        if (data.get("content") instanceof Map<?, ?> contentMap) {
            BitbucketReview.BitbucketContent content = new BitbucketReview.BitbucketContent();
            content.setRaw((String) ((Map<String, Object>) contentMap).get("raw"));
            comment.setContent(content);
        }

        if (data.get("user") instanceof Map<?, ?> userMap) {
            BitbucketReview.BitbucketUser user = new BitbucketReview.BitbucketUser();
            user.setDisplayName((String) ((Map<String, Object>) userMap).get("display_name"));
            user.setNickname((String) ((Map<String, Object>) userMap).get("nickname"));
            comment.setUser(user);
        }

        if (data.get("inline") instanceof Map<?, ?> inlineMap) {
            BitbucketReviewComment.Inline inline = new BitbucketReviewComment.Inline();
            inline.setPath((String) ((Map<String, Object>) inlineMap).get("path"));
            Object toVal = ((Map<String, Object>) inlineMap).get("to");
            if (toVal instanceof Number num) {
                inline.setTo(num.intValue());
            }
            comment.setInline(inline);
        }

        comment.setCreatedOn((String) data.get("created_on"));
        return comment;
    }

    /**
     * Builds a URL-encoded form body for Bitbucket's /src endpoint (file create/update).
     */
    private String buildFormBody(String message, String branch, String path, String content) {
        return "message=" + urlEncode(message)
                + "&branch=" + urlEncode(branch)
                + "&" + urlEncode(path) + "=" + urlEncode(content);
    }

    /**
     * Builds a URL-encoded form body for Bitbucket's /src endpoint (file delete).
     */
    private String buildDeleteFormBody(String message, String branch, String path) {
        return "message=" + urlEncode(message)
                + "&branch=" + urlEncode(branch)
                + "&files=" + urlEncode(path);
    }

    private static String urlEncode(String value) {
        return java.net.URLEncoder.encode(value, java.nio.charset.StandardCharsets.UTF_8);
    }
}
