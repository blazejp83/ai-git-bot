package org.remus.giteabot.admin;

import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.stereotype.Repository;

@Repository
public interface AiIntegrationRepository extends JpaRepository<AiIntegration, Long> {
}
