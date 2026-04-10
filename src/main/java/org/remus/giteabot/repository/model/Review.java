package org.remus.giteabot.repository.model;

/**
 * Provider-agnostic interface for a pull request review.
 * Implementations exist for Gitea ({@link org.remus.giteabot.gitea.model.GiteaReview})
 * and GitHub ({@link org.remus.giteabot.github.model.GitHubReview}),
 * with future support for GitLab, Bitbucket, etc.
 */
public interface Review {

    Long getId();

    String getBody();

    String getState();

    String getUserLogin();

    String getSubmittedAt();

    Integer getCommentsCount();
}
