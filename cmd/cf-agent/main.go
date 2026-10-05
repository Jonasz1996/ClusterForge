// cf-agent draait op elke node: hij meldt zich aan bij ClusterForge en stuurt
// daarna heartbeats en facts over één uitgaande NATS-verbinding.
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

	"github.com/Jonasz1996/clusterforge/internal/agent"
)

// version wordt bij het bouwen gezet met -ldflags "-X main.version=...".
var version = "dev"

const usage = `cf-agent: de ClusterForge-agent

Gebruik:
  cf-agent enroll -server https://clusterforge.example -token cfe_...
  cf-agent run       verbindt met ClusterForge (zo start systemd hem)
  cf-agent facts     toont de facts van deze machine
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
	return doEnroll(*server, *token, *caFile, *config)
}

func doEnroll(server, token, caFile, config string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
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
	_ = fl.Parse(args)

	c, err := agent.LoadConfig(*config)
	if errors.Is(err, fs.ErrNotExist) {
		// Nog niet aangemeld: een aanmeldbestand (bijvoorbeeld van Proxmox) gebruiken.
		e, ferr := agent.LoadEnrollFile(*enrollFile)
		if ferr != nil {
			return fmt.Errorf("niet aangemeld; draai eerst cf-agent enroll (%s ontbreekt)", *config)
		}
		if err := doEnroll(e.Server, e.Token, "", *config); err != nil {
			return err
		}
		_ = os.Remove(*enrollFile)
		c, err = agent.LoadConfig(*config)
	}
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a := &agent.Agent{Config: c, Version: version, Log: log}
	log.Info("cf-agent gestart", "version", version, "node", c.NodeID, "nats", c.NatsURL)
	return a.Run(ctx)
}
