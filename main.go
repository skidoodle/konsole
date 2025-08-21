package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	botToken = os.Getenv("BOT_TOKEN")
	ownerID  = os.Getenv("OWNER_ID")

	allowedContexts = []discordgo.InteractionContextType{
		discordgo.InteractionContextGuild,
		discordgo.InteractionContextBotDM,
		discordgo.InteractionContextPrivateChannel,
	}

	commands = []*discordgo.ApplicationCommand{
		{
			Name:        "exec",
			Description: "Execute a shell command in a secure, unprivileged sandbox.",
			Contexts:    &allowedContexts,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "command",
					Description: "The command to execute",
					Required:    true,
				},
			},
		},
		{
			Name:        "execroot",
			Description: "Execute a shell command as root. (BOT OWNER ONLY)",
			Contexts:    &allowedContexts,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "command",
					Description: "The command to execute as root",
					Required:    true,
				},
			},
		},
	}

	commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"exec":     handleExecCommand,
		"execroot": handleExecRootCommand,
	}
)

func main() {
	if botToken == "" || ownerID == "" {
		log.Fatal("FATAL: BOT_TOKEN and OWNER_ID environment variables must be set")
	}

	dg, err := discordgo.New("Bot " + botToken)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
	}

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as: %v#%v", s.State.User.Username, s.State.User.Discriminator)

		log.Println("Registering commands...")
		for _, v := range commands {
			_, err := s.ApplicationCommandCreate(s.State.User.ID, "", v)
			if err != nil {
				log.Panicf("Cannot create command '%v': %v", v.Name, err)
			}
		}
		log.Println("All commands registered successfully.")
	})

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
			h(s, i)
		}
	})

	err = dg.Open()
	if err != nil {
		log.Fatalf("Error opening connection: %v", err)
	}
	defer dg.Close()

	fmt.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

func handleExecCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error sending initial response: %v", err)
		return
	}

	commandStr := i.ApplicationCommandData().Options[0].StringValue()

	initialContent := fmt.Sprintf("`$ %s`\n```bash\nExecuting as 'user'...\n```", commandStr)
	msg, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: initialContent,
	})
	if err != nil {
		log.Printf("Error creating followup message: %v", err)
		return
	}

	go streamCommandOutput(s, i, msg, commandStr, "user")
}

func handleExecRootCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.User.ID != ownerID {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ You do not have permission to use this command.",
				Flags:   discordgo.MessageFlagsEphemeral, // Message is only visible to the user
			},
		})
		return
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err != nil {
		log.Printf("Error sending initial response: %v", err)
		return
	}

	commandStr := i.ApplicationCommandData().Options[0].StringValue()

	initialContent := fmt.Sprintf("`$ %s`\n```bash\nExecuting as 'root'...\n```", commandStr)
	msg, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: initialContent,
	})
	if err != nil {
		log.Printf("Error creating followup message: %v", err)
		return
	}

	go streamCommandOutput(s, i, msg, commandStr, "root")
}

func streamCommandOutput(s *discordgo.Session, i *discordgo.InteractionCreate, msg *discordgo.Message, commandStr string, execUser string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "su", "-", execUser, "-c", commandStr)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("Error creating stdout pipe: %v", err)
		return
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		log.Printf("Error starting command: %v", err)
		return
	}

	var outputBuffer strings.Builder
	scanner := bufio.NewScanner(stdout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var hasNewOutput bool

	commandDone := make(chan struct{})
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			outputBuffer.WriteString(line + "\n")
			hasNewOutput = true
		}
		close(commandDone)
	}()

	for {
		select {
		case <-ticker.C:
			if hasNewOutput {
				hasNewOutput = false
				editMessageWithContent(s, i, msg, commandStr, outputBuffer.String(), false)
			}
		case <-commandDone:
			status := "Command finished."
			if ctx.Err() != nil {
				status = "Command killed (60s timeout)."
			}
			editMessageWithContent(s, i, msg, commandStr, outputBuffer.String(), true, status)
			cmd.Wait()
			return
		}
	}
}

func editMessageWithContent(s *discordgo.Session, i *discordgo.InteractionCreate, msg *discordgo.Message, commandStr, content string, finished bool, status ...string) {
	finalStatus := ""
	if finished {
		finalStatus = "\n" + strings.Join(status, " ")
	}

	formattedOutput := fmt.Sprintf("`$ %s`\n```bash\n%s%s\n```", commandStr, content, finalStatus)

	if len(formattedOutput) > 2000 {
		chrome := fmt.Sprintf("`$ %s`\n```bash\n\n... (truncated)%s\n```", commandStr, finalStatus)
		safeOutputLength := 2000 - len(chrome)
		if safeOutputLength > 0 {
			content = content[:safeOutputLength]
		}
		formattedOutput = fmt.Sprintf("`$ %s`\n```bash\n%s\n... (truncated)%s\n```", commandStr, content, finalStatus)
	}

	_, err := s.FollowupMessageEdit(i.Interaction, msg.ID, &discordgo.WebhookEdit{
		Content: &formattedOutput,
	})
	if err != nil {
		if !strings.Contains(err.Error(), "429") {
			log.Printf("Error editing message: %v", err)
		}
	}
}
