/*
 * Copyright 2025 Humaid Alqasimi
 * SPDX-License-Identifier: Apache-2.0
 */
package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flamego/csrf"
	"github.com/flamego/flamego"
	"github.com/flamego/session"
	"github.com/flamego/template"
	"github.com/urfave/cli/v3"

	"github.com/humaidq/humaid-qsl/static"
	"github.com/humaidq/humaid-qsl/templates"
	"github.com/humaidq/humaid-qsl/utils"
)

var CmdStart = &cli.Command{
	Name:    "start",
	Aliases: []string{"run"},
	Usage:   "Start the web server",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "port",
			Value: "8080",
			Usage: "the web server port",
		},
		&cli.BoolFlag{
			Name:  "dev",
			Value: false,
			Usage: "enables development mode (for templates)",
		},
		&cli.StringFlag{
			Name:     "adif",
			Usage:    "path to ADIF file containing QSO logs",
			Required: true,
		},
	},
	Action: start,
}

func start(ctx context.Context, cmd *cli.Command) (err error) {
	// Load ADIF file
	adifPath := cmd.String("adif")
	adifFile, err := os.Open(adifPath)
	if err != nil {
		return fmt.Errorf("failed to open ADIF file: %w", err)
	}
	defer adifFile.Close()

	parser := utils.NewADIFParser()
	if err := parser.ParseFile(adifFile); err != nil {
		return fmt.Errorf("failed to parse ADIF file: %w", err)
	}

	log.Printf("Loaded %d QSOs from %s", len(parser.GetQSOs()), adifPath)

	f := flamego.Classic()

	// Setup flamego
	fs, err := template.EmbedFS(templates.Templates, ".", []string{".html"})
	if err != nil {
		panic(err)
	}
	f.Use(session.Sessioner())
	f.Use(csrf.Csrfer())
	f.Use(template.Templater(template.Options{
		FileSystem: fs,
	}))
	f.Use(flamego.Static(flamego.StaticOptions{
		FileSystem: http.FS(static.Static),
	}))

	// Inject ADIF parser into context
	f.Use(func(c flamego.Context) {
		c.Map(parser)
	})

	// Add request logging middleware
	f.Use(func(c flamego.Context) {
		start := time.Now()
		c.Next()

		// Log the request
		logEntry := fmt.Sprintf("[%s] %s %s %s - %v\n",
			start.Format("2006-01-02 15:04:05"),
			c.Request().Method,
			c.Request().URL.Path,
			c.Request().RemoteAddr,
			time.Since(start))

		// Append to log file
		logFile, err := os.OpenFile("qsl-access.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logFile.WriteString(logEntry)
			logFile.Close()
		}
	})

	f.Get("/", func(t template.Template, data template.Data, parser *utils.ADIFParser, x csrf.CSRF) {
		data["TotalQSOs"] = parser.GetTotalQSOCount()
		data["UniqueCountries"] = parser.GetUniqueCountriesCount()
		data["LatestQSOs"] = parser.GetLatestQSOs(30)
		data["CSRFToken"] = x.Token()
		t.HTML(http.StatusOK, "home")
	})

	f.Get("/qrz", func(t template.Template, data template.Data, parser *utils.ADIFParser) {
		data["LatestQSOs"] = parser.GetLatestQSOs(30)
		t.HTML(http.StatusOK, "qrz")
	})

	f.Get("/{callsign}-{timestamp}", func(c flamego.Context, t template.Template, data template.Data, parser *utils.ADIFParser) {
		callsign := strings.ToUpper(c.Param("callsign"))
		timestampStr := c.Param("timestamp")

		// Parse Unix timestamp
		timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
		if err != nil {
			c.Redirect("/", http.StatusFound)
			return
		}

		searchTime := time.Unix(timestamp, 0)

		// Search QSOs with 10-minute tolerance
		qsos := parser.SearchQSO(callsign, searchTime, 10)

		if len(qsos) == 0 {
			c.Redirect("/", http.StatusFound)
			return
		}

		// Return single result and all QSOs for this callsign
		currentQSO := qsos[0]
		allQSOs := parser.GetQSOsByCallsign(callsign)

		data["QSO"] = currentQSO
		data["AllQSOs"] = allQSOs
		data["Callsign"] = callsign
		t.HTML(http.StatusOK, "result")
	})

	f.Post("/", csrf.Validate, func(c flamego.Context, t template.Template, data template.Data, parser *utils.ADIFParser, x csrf.CSRF) {
		callsign := strings.TrimSpace(strings.ToUpper(c.Request().FormValue("callsign")))
		year := strings.TrimSpace(c.Request().FormValue("year"))
		month := strings.TrimSpace(c.Request().FormValue("month"))
		day := strings.TrimSpace(c.Request().FormValue("day"))
		hour := strings.TrimSpace(c.Request().FormValue("hour"))
		minute := strings.TrimSpace(c.Request().FormValue("minute"))

		// Validate inputs
		if callsign == "" {
			data["Error"] = "Call sign is required"
			data["CSRFToken"] = x.Token()
			t.HTML(http.StatusBadRequest, "home")
			return
		}

		if year == "" || month == "" || day == "" || hour == "" || minute == "" {
			data["Error"] = "All date and time fields are required"
			data["CSRFToken"] = x.Token()
			t.HTML(http.StatusBadRequest, "home")
			return
		}

		// Parse timestamp from separate fields
		timestampStr := fmt.Sprintf("%s-%02s-%02sT%02s:%02s", year, month, day, hour, minute)
		searchTime, err := time.Parse("2006-01-02T15:04", timestampStr)
		if err != nil {
			data["Error"] = "Invalid date and time values"
			data["CSRFToken"] = x.Token()
			t.HTML(http.StatusBadRequest, "home")
			return
		}

		// Search QSOs with 10-minute tolerance
		qsos := parser.SearchQSO(callsign, searchTime, 10)

		// Log QSO lookup
		logEntry := fmt.Sprintf("[%s] QSO_SEARCH %s %s %s - %s\n",
			time.Now().Format("2006-01-02 15:04:05"),
			callsign,
			searchTime.Format("2006-01-02 15:04"),
			c.Request().RemoteAddr,
			func() string {
				if len(qsos) > 0 {
					return "SUCCESS"
				}
				return "NOT_FOUND"
			}())

		logFile, err := os.OpenFile("qsl-lookups.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logFile.WriteString(logEntry)
			logFile.Close()
		}

		if len(qsos) == 0 {
			data["Error"] = fmt.Sprintf("No QSO found for %s around %s UTC", callsign, searchTime.Format("2006-01-02 15:04"))
			data["CSRFToken"] = x.Token()
			t.HTML(http.StatusOK, "home")
			return
		}

		// Redirect to unique QSO URL
		qso := qsos[0]
		unixTimestamp := qso.Timestamp.Unix()
		url := fmt.Sprintf("/%s-%d", qso.Call, unixTimestamp)
		c.Redirect(url, http.StatusFound)
	})

	port := cmd.String("port")

	log.Printf("Starting web server on port %s\n", port)
	srv := &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%s", port),
		Handler:      f,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Fatal(srv.ListenAndServe())

	return nil
}
