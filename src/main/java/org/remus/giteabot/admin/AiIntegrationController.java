package org.remus.giteabot.admin;

import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Controller;
import org.springframework.ui.Model;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.servlet.mvc.support.RedirectAttributes;

import java.util.List;

@Slf4j
@Controller
@RequestMapping("/ai-integrations")
public class AiIntegrationController {

    private final AiIntegrationService aiIntegrationService;

    public AiIntegrationController(AiIntegrationService aiIntegrationService) {
        this.aiIntegrationService = aiIntegrationService;
    }

    @GetMapping
    public String list(Model model) {
        List<AiIntegration> integrations = aiIntegrationService.findAll();
        model.addAttribute("integrations", integrations);
        model.addAttribute("activeNav", "ai-integrations");
        return "ai-integrations/list";
    }

    @GetMapping("/new")
    public String newForm(Model model) {
        model.addAttribute("integration", new AiIntegration());
        model.addAttribute("providerTypes", List.of("anthropic", "openai", "ollama", "llamacpp"));
        model.addAttribute("activeNav", "ai-integrations");
        return "ai-integrations/form";
    }

    @GetMapping("/{id}/edit")
    public String editForm(@PathVariable Long id, Model model, RedirectAttributes redirectAttributes) {
        return aiIntegrationService.findById(id)
                .map(integration -> {
                    model.addAttribute("integration", integration);
                    model.addAttribute("providerTypes", List.of("anthropic", "openai", "ollama", "llamacpp"));
                    model.addAttribute("activeNav", "ai-integrations");
                    return "ai-integrations/form";
                })
                .orElseGet(() -> {
                    redirectAttributes.addFlashAttribute("error", "AI Integration not found");
                    return "redirect:/ai-integrations";
                });
    }

    @PostMapping("/save")
    public String save(@ModelAttribute AiIntegration integration, RedirectAttributes redirectAttributes) {
        try {
            aiIntegrationService.save(integration);
            redirectAttributes.addFlashAttribute("success", "AI Integration saved successfully");
        } catch (Exception e) {
            log.error("Failed to save AI Integration", e);
            redirectAttributes.addFlashAttribute("error", "Failed to save: " + e.getMessage());
        }
        return "redirect:/ai-integrations";
    }

    @PostMapping("/{id}/delete")
    public String delete(@PathVariable Long id, RedirectAttributes redirectAttributes) {
        try {
            aiIntegrationService.deleteById(id);
            redirectAttributes.addFlashAttribute("success", "AI Integration deleted successfully");
        } catch (Exception e) {
            log.error("Failed to delete AI Integration", e);
            redirectAttributes.addFlashAttribute("error", "Failed to delete: " + e.getMessage());
        }
        return "redirect:/ai-integrations";
    }
}
