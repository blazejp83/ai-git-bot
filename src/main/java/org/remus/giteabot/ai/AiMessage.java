package org.remus.giteabot.ai;

import lombok.Builder;
import lombok.Data;

@Data
@Builder
public class AiMessage {
    private String role;
    private String content;
}
