package org.remus.giteabot.ai;

import java.util.List;

/**
 * Provider-agnostic interface for AI-powered code review.
 * Implementations exist for Anthropic, OpenAI, and Ollama.
 */
public interface AiClient {

    String reviewDiff(String prTitle, String prBody, String diff);

    String reviewDiff(String prTitle, String prBody, String diff, String systemPrompt, String modelOverride);

    /**
     * Sends a multi-turn conversation to the AI provider and returns the assistant's response.
     */
    String chat(List<AiMessage> conversationHistory, String newUserMessage,
                String systemPrompt, String modelOverride);
}
