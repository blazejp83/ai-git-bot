package org.remus.giteabot.agent.model;

import lombok.Builder;
import lombok.Data;

@Data
@Builder
public class FileChange {

    /**
     * Relative file path within the repository.
     */
    private String path;

    /**
     * The full content of the file after changes.
     */
    private String content;

    /**
     * The operation to perform: CREATE, UPDATE, or DELETE.
     */
    private Operation operation;

    public enum Operation {
        CREATE,
        UPDATE,
        DELETE
    }
}
