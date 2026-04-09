package org.remus.giteabot.admin;

import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Controller;
import org.springframework.ui.Model;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.servlet.mvc.support.RedirectAttributes;

import java.util.List;

@Slf4j
@Controller
@RequestMapping("/bots")
public class BotController {

    private final BotService botService;
    private final AiIntegrationService aiIntegrationService;
    private final GitIntegrationService gitIntegrationService;

    public BotController(BotService botService,
                         AiIntegrationService aiIntegrationService,
                         GitIntegrationService gitIntegrationService) {
        this.botService = botService;
        this.aiIntegrationService = aiIntegrationService;
        this.gitIntegrationService = gitIntegrationService;
    }

    @GetMapping
    public String list(Model model) {
        List<Bot> bots = botService.findAll();
        model.addAttribute("bots", bots);
        model.addAttribute("activeNav", "bots");
        return "bots/list";
    }

    @GetMapping("/new")
    public String newForm(Model model) {
        model.addAttribute("bot", new Bot());
        model.addAttribute("aiIntegrations", aiIntegrationService.findAll());
        model.addAttribute("gitIntegrations", gitIntegrationService.findAll());
        model.addAttribute("activeNav", "bots");
        return "bots/form";
    }

    @GetMapping("/{id}/edit")
    public String editForm(@PathVariable Long id, Model model, RedirectAttributes redirectAttributes) {
        return botService.findById(id)
                .map(bot -> {
                    model.addAttribute("bot", bot);
                    model.addAttribute("aiIntegrations", aiIntegrationService.findAll());
                    model.addAttribute("gitIntegrations", gitIntegrationService.findAll());
                    model.addAttribute("activeNav", "bots");
                    return "bots/form";
                })
                .orElseGet(() -> {
                    redirectAttributes.addFlashAttribute("error", "Bot not found");
                    return "redirect:/bots";
                });
    }

    @PostMapping("/save")
    public String save(@ModelAttribute Bot bot,
                       @RequestParam Long aiIntegrationId,
                       @RequestParam Long gitIntegrationId,
                       RedirectAttributes redirectAttributes) {
        try {
            AiIntegration aiIntegration = aiIntegrationService.findById(aiIntegrationId)
                    .orElseThrow(() -> new IllegalArgumentException("AI Integration not found"));
            GitIntegration gitIntegration = gitIntegrationService.findById(gitIntegrationId)
                    .orElseThrow(() -> new IllegalArgumentException("Git Integration not found"));

            bot.setAiIntegration(aiIntegration);
            bot.setGitIntegration(gitIntegration);
            botService.save(bot);
            redirectAttributes.addFlashAttribute("success", "Bot saved successfully");
        } catch (Exception e) {
            log.error("Failed to save Bot", e);
            redirectAttributes.addFlashAttribute("error", "Failed to save: " + e.getMessage());
        }
        return "redirect:/bots";
    }

    @PostMapping("/{id}/delete")
    public String delete(@PathVariable Long id, RedirectAttributes redirectAttributes) {
        try {
            botService.deleteById(id);
            redirectAttributes.addFlashAttribute("success", "Bot deleted successfully");
        } catch (Exception e) {
            log.error("Failed to delete Bot", e);
            redirectAttributes.addFlashAttribute("error", "Failed to delete: " + e.getMessage());
        }
        return "redirect:/bots";
    }
}
