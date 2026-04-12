package org.remus.giteabot.admin;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
class AdminServiceTest {

    @Mock
    private AdminUserRepository adminUserRepository;

    @InjectMocks
    private AdminService adminService;

    @Test
    void isSetupRequired_noUsers_returnsTrue() {
        when(adminUserRepository.count()).thenReturn(0L);

        assertTrue(adminService.isSetupRequired());
    }

    @Test
    void isSetupRequired_usersExist_returnsFalse() {
        when(adminUserRepository.count()).thenReturn(1L);

        assertFalse(adminService.isSetupRequired());
    }

    @Test
    void createAdmin_savesUserWithHashedPassword() {
        when(adminUserRepository.save(any(AdminUser.class))).thenAnswer(invocation -> invocation.getArgument(0));

        AdminUser result = adminService.createAdmin("admin", "password123");

        assertNotNull(result);
        assertEquals("admin", result.getUsername());
        // Password should be BCrypt hashed, not plain text
        assertNotEquals("password123", result.getPasswordHash());
        assertTrue(result.getPasswordHash().startsWith("$2a$") || result.getPasswordHash().startsWith("$2b$"));
        verify(adminUserRepository).save(any(AdminUser.class));
    }

    @Test
    void findByUsername_delegatesToRepository() {
        AdminUser admin = new AdminUser();
        admin.setUsername("testuser");
        when(adminUserRepository.findByUsername("testuser")).thenReturn(Optional.of(admin));

        Optional<AdminUser> result = adminService.findByUsername("testuser");

        assertTrue(result.isPresent());
        assertEquals("testuser", result.get().getUsername());
        verify(adminUserRepository).findByUsername("testuser");
    }
}
