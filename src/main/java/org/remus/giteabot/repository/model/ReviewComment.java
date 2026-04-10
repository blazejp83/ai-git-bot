package org.remus.giteabot.repository.model;

/**
 * Provider-agnostic interface for a pull request review comment.
 * Implementations exist for Gitea ({@link org.remus.giteabot.gitea.model.GiteaReviewComment})
 * and GitHub ({@link org.remus.giteabot.github.model.GitHubReviewComment}),
 * with future support for GitLab, Bitbucket, etc.
 */
public interface ReviewComment {

    Long getId();

    String getBody();

    String getPath();

    String getDiffHunk();

    Integer getLine();

    String getUserLogin();
}
