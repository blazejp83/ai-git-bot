package org.remus.giteabot.agent.model;

import lombok.Builder;
import lombok.Data;

import java.util.List;

@Data
@Builder
public class ImplementationPlan {

    /**
     * Short summary of the planned implementation.
     */
    private String summary;

    /**
     * List of file changes to implement.
     */
    private List<FileChange> fileChanges;

    /**
     * Branch name to be created for the implementation.
     */
    private String branchName;
}
