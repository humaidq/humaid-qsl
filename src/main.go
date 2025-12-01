/*
 * Copyright 2025 Humaid Alqasimi
 * SPDX-License-Identifier: Apache-2.0
 */
package main

import (
	"context"
	"log"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/humaidq/humaid-qsl/cmd"
)

func main() {
	cmd := &cli.Command{
		Name:  "humaid-qsl",
		Usage: "Humaid's QSL site",
		Commands: []*cli.Command{
			cmd.CmdStart,
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
