package main

import (
	"context"

	"github.com/astriaorg/messenger-rollup/erc20"
	log "github.com/sirupsen/logrus"

	"github.com/sethvargo/go-envconfig"
)

func main() {
	log.SetLevel(log.DebugLevel)
	log.SetFormatter(&log.TextFormatter{})

	// load env vars
	var cfg erc20.Config
	if err := envconfig.Process(context.Background(), &cfg); err != nil {
		log.Fatal(err)
	}
	log.Debugf("Read config from env: %+v\n", cfg)

	// init from cfg
	app := erc20.NewApp(cfg)

	// run messenger
	app.Run()
}
