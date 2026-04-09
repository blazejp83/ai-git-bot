package org.remus.giteabot.admin;

import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Controller;
import org.springframework.ui.Model;
import org.springframework.web.bind.annotation.GetMapping;

import java.util.List;

@Slf4j
@Controller
public class DashboardController {

    private final BotService botService;

    public DashboardController(BotService botService) {
        this.botService = botService;
    }

    @GetMapping("/")
    public String index() {
        return "redirect:/dashboard";
    }

    @GetMapping("/dashboard")
    public String dashboard(Model model) {
        List<Bot> bots = botService.findAll();
        model.addAttribute("bots", bots);
        model.addAttribute("totalBots", bots.size());
        model.addAttribute("activeBots", bots.stream().filter(Bot::isEnabled).count());
        model.addAttribute("totalWebhookCalls", bots.stream().mapToLong(Bot::getWebhookCallCount).sum());
        model.addAttribute("totalTokensSent", bots.stream().mapToLong(Bot::getAiTokensSent).sum());
        model.addAttribute("totalTokensReceived", bots.stream().mapToLong(Bot::getAiTokensReceived).sum());
        return "dashboard";
    }
}
