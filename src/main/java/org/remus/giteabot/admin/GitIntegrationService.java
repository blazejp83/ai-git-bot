package org.remus.giteabot.admin;

import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.util.List;
import java.util.Optional;

@Slf4j
@Service
@Transactional
public class GitIntegrationService {

    private final GitIntegrationRepository gitIntegrationRepository;
    private final EncryptionService encryptionService;

    public GitIntegrationService(GitIntegrationRepository gitIntegrationRepository, EncryptionService encryptionService) {
        this.gitIntegrationRepository = gitIntegrationRepository;
        this.encryptionService = encryptionService;
    }

    @Transactional(readOnly = true)
    public List<GitIntegration> findAll() {
        return gitIntegrationRepository.findAll();
    }

    @Transactional(readOnly = true)
    public Optional<GitIntegration> findById(Long id) {
        return gitIntegrationRepository.findById(id);
    }

    public GitIntegration save(GitIntegration integration) {
        String token = integration.getToken();
        if (token != null && !token.isBlank()) {
            integration.setToken(encryptionService.encrypt(token));
        }
        return gitIntegrationRepository.save(integration);
    }

    public void deleteById(Long id) {
        gitIntegrationRepository.deleteById(id);
    }

    public String decryptToken(GitIntegration integration) {
        String token = integration.getToken();
        if (token == null || token.isBlank()) {
            return null;
        }
        return encryptionService.decrypt(token);
    }
}
