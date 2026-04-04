package org.remus.giteabot.ai.ollama;

import lombok.Builder;
import lombok.Data;

import java.util.List;

@Data
@Builder
public class OllamaRequest {

    private String model;

    private List<Message> messages;

    private boolean stream;

    @Data
    @Builder
    public static class Message {
        private String role;
        private String content;
    }
}
