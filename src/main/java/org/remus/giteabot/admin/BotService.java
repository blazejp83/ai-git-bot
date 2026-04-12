package org.remus.giteabot.admin;

import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

@Slf4j
@Service
@Transactional
public class BotService {

    private final BotRepository botRepository;

    public BotService(BotRepository botRepository) {
        this.botRepository = botRepository;
    }

    @Transactional(readOnly = true)
    public List<Bot> findAll() {
        return botRepository.findAllWithIntegrations();
    }

    @Transactional(readOnly = true)
    public Optional<Bot> findById(Long id) {
        return botRepository.findById(id);
    }

    @Transactional(readOnly = true)
    public Optional<Bot> findByWebhookSecret(String secret) {
        return botRepository.findByWebhookSecret(secret);
    }

    public Bot save(Bot bot) {
        if (bot.getWebhookSecret() == null) {
            bot.setWebhookSecret(UUID.randomUUID().toString());
        }
        return botRepository.save(bot);
    }

    public void deleteById(Long id) {
        botRepository.deleteById(id);
    }

    public void incrementWebhookCallCount(Bot bot) {
        bot.setWebhookCallCount(bot.getWebhookCallCount() + 1);
        bot.setLastWebhookAt(Instant.now());
        botRepository.save(bot);
    }

    public void recordError(Bot bot, String errorMessage) {
        bot.setLastErrorMessage(errorMessage);
        bot.setLastErrorAt(Instant.now());
        botRepository.save(bot);
    }
}
