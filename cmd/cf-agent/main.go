// cf-agent draait op elke node: hij meldt zich aan bij ClusterForge, stuurt
// daarna heartbeats, metrics en facts over één uitgaande NATS-verbinding en
// voert de commando's van de server uit.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// version wordt bij het bouwen gezet met -ldflags "-X main.version=...".
var version = "dev"

const usage = `cf-agent: de ClusterForge-agent

Gebruik:
  cf-agent enroll -server https://clusterforge.example -token cfe_...
  cf-agent run       verbindt met ClusterForge (zo start systemd hem); nog niet
                     aangemeld, dan wacht hij op /etc/clusterforge/enroll.json
  cf-agent facts     toont de facts van deze machine
  cf-agent verify -  leest een controle-aanvraag (JSON) van stdin en schrijft het
                     resultaat naar stdout; de back-upcontrole start dit in een
                     teruggezette sandbox
  cf-agent version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	var err error
	switch os.Args[1] {
	case "enroll":
		err = enroll(os.Args[2:])
	case "run":
		err = run(os.Args[2:], log)
	case "verify":
		os.Exit(verify(os.Args[2:]))
	case "facts":
		f := (&agent.Collector{}).Facts(context.Background())
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(f)
	case "version", "-version", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fout:", err)
		os.Exit(1)
	}
}

// verify voert de diepe back-upcontrole uit en geeft de exitcode: 0, ook als
// controles falen, of 3 bij een ongeldige aanvraag.
func verify(args []string) int {
	if len(args) != 1 || args[0] != "-" {
		fmt.Fprintln(os.Stderr, "gebruik: cf-agent verify - (de aanvraag komt als JSON op stdin)")
		return protocol.VerifyInvalid
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	v := &agent.Verifier{Version: version}
	return v.Main(ctx, os.Stdin, os.Stdout)
}

func enroll(args []string) error {
	fl := flag.NewFlagSet("enroll", flag.ExitOnError)
	server := fl.String("server", "", "URL van ClusterForge, bijvoorbeeld https://clusterforge.example")
	token := fl.String("token", "", "enrollmenttoken uit de webinterface")
	caFile := fl.String("ca", "", "extra CA-certificaat (PEM) voor de HTTPS-verbinding")
	config := fl.String("config", agent.DefaultConfigPath, "pad van het configuratiebestand")
	_ = fl.Parse(args)
	if *token == "" {
		*token = os.Getenv("CF_ENROLL_TOKEN")
	}
	if *server == "" || *token == "" {
		return errors.New("-server en -token zijn verplicht")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return doEnroll(ctx, *server, *token, *caFile, *config)
}

func doEnroll(ctx context.Context, server, token, caFile, config string) error {
	c, err := agent.Enroll(ctx, agent.EnrollOptions{ServerURL: server, Token: token, CAFile: caFile, Version: version})
	if err != nil {
		return err
	}
	if err := c.Save(config); err != nil {
		return err
	}
	fmt.Printf("aangemeld als node %s; configuratie in %s\n", c.NodeID, config)
	return nil
}

func run(args []string, log *slog.Logger) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	config := fl.String("config", agent.DefaultConfigPath, "pad van het configuratiebestand")
	enrollFile := fl.String("enroll-file", agent.DefaultEnrollPath, "eenmalig aanmeldbestand als er nog geen configuratie is")
	statePath := fl.String("state", agent.DefaultStatePath, "bestand waarin de agent onderhoud en herstarts onthoudt")
	_ = fl.Parse(args)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Een sandbox van de back-upcontrole is een kopie van een node, met
	// dezelfde sleutel. Die mag nooit als de node binnenkomen.
	if agent.InSandbox("") {
		log.Info("dit is een sandbox van de back-upcontrole (serienummer " + protocol.SandboxSerial + "); de agent verbindt niet en wacht tot hij gestopt wordt")
		<-ctx.Done()
		return nil
	}
	c, err := agent.LoadConfig(*config)
	if errors.Is(err, fs.ErrNotExist) {
		c, err = waitEnroll(ctx, log, *config, *enrollFile)
		if ctx.Err() != nil {
			return nil
		}
	}
	if err != nil {
		return err
	}

	a := &agent.Agent{Config: c, Version: version, Log: log, StatePath: *statePath}
	log.Info("cf-agent gestart", "version", version, "node", c.NodeID, "nats", c.NatsURL)
	return a.Run(ctx)
}

// waitEnroll wacht tot de agent aangemeld is: via een aanmeldbestand, dat
// ClusterForge in een nieuwe VM uit een golden image zet, of via cf-agent
// enroll.
func waitEnroll(ctx context.Context, log *slog.Logger, config, enrollFile string) (*agent.Config, error) {
	log.Info("nog niet aangemeld; wachten op een aanmeldbestand of cf-agent enroll", "bestand", enrollFile)
	for {
		delay := 2 * time.Second
		if c, err := agent.LoadConfig(config); err == nil {
			return c, nil
		}
		e, err := agent.LoadEnrollFile(enrollFile)
		switch {
		case err == nil:
			log.Info("aanmelden met het aanmeldbestand", "server", e.Server)
			if err := doEnroll(ctx, e.Server, e.Token, "", config); err != nil {
				log.Error("aanmelden mislukt; straks opnieuw", "err", err)
				delay = 15 * time.Second
				break
			}
			_ = os.Remove(enrollFile)
			return agent.LoadConfig(config)
		case !errors.Is(err, fs.ErrNotExist):
			// Misschien nog half geschreven.
			log.Error("aanmeldbestand onleesbaar", "err", err)
			delay = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}
