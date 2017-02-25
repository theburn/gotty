package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/codegangsta/cli"

	"github.com/yudai/gotty/backend/localcommand"
	"github.com/yudai/gotty/pkg/homedir"
	"github.com/yudai/gotty/server"
	"github.com/yudai/gotty/utils"
)

func main() {
	app := cli.NewApp()
	app.Name = "gotty"
	app.Version = Version
	app.Usage = "Share your terminal as a web application"
	app.HideHelp = true
	cli.AppHelpTemplate = helpTemplate

	appOptions := &server.Options{}
	if err := utils.ApplyDefaultValues(appOptions); err != nil {
		exit(err, 1)
	}
	backendOptions := &localcommand.Options{}
	if err := utils.ApplyDefaultValues(backendOptions); err != nil {
		exit(err, 1)
	}

	cliFlags, flagMappings, err := utils.GenerateFlags(appOptions, backendOptions)
	if err != nil {
		exit(err, 3)
	}

	app.Flags = append(
		cliFlags,
		cli.StringFlag{
			Name:   "config",
			Value:  "~/.gotty",
			Usage:  "Config file path",
			EnvVar: "GOTTY_CONFIG",
		},
	)

	app.Action = func(c *cli.Context) {
		if len(c.Args()) == 0 {
			msg := "Error: No command given."
			cli.ShowAppHelp(c)
			exit(fmt.Errorf(msg), 1)
		}

		configFile := c.String("config")
		_, err := os.Stat(homedir.Expand(configFile))
		if configFile != "~/.gotty" || !os.IsNotExist(err) {
			if err := utils.ApplyConfigFile(configFile, appOptions, backendOptions); err != nil {
				exit(err, 2)
			}
		}

		utils.ApplyFlags(cliFlags, flagMappings, c, appOptions, backendOptions)

		appOptions.EnableBasicAuth = c.IsSet("credential")
		appOptions.EnableTLSClientAuth = c.IsSet("tls-ca-crt")

		err = appOptions.Validate()
		if err != nil {
			exit(err, 6)
		}

		factory, err := localcommand.NewFactory(c.Args(), backendOptions)
		if err != nil {
			exit(err, 3)
		}
		srv, err := server.New(factory, appOptions)
		if err != nil {
			exit(err, 3)
		}

		ctx, cancel := context.WithCancel(context.Background())

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer cancel()
			defer wg.Done()

			err := srv.Run(ctx)
			if err != nil {
				//exit(err, 4)
			}
		}()
		waitSignals(ctx, cancel)
		wg.Wait()
		fmt.Println("exiting")

	}
	app.Run(os.Args)
}

func exit(err error, code int) {
	if err != nil {
		fmt.Println(err)
	}
	os.Exit(code)
}

func waitSignals(ctx context.Context, cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(
		sigChan,
		syscall.SIGINT,
		syscall.SIGTERM,
	)

	for {
		select {
		case s := <-sigChan:
			switch s {
			case syscall.SIGINT, syscall.SIGTERM:
				cancel()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
