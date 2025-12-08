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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
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
		&cli.DurationFlag{
			Name:  "reload-interval",
			Value: 5 * time.Minute,
			Usage: "interval to reload the ADIF file (e.g., 5m, 1h, 30s)",
		},
	},
	Action: start,
}

// ReloadableParser wraps ADIFParser with automatic reloading capability
type ReloadableParser struct {
	parser   *utils.ADIFParser
	filePath string
	mutex    sync.RWMutex
}

// NewReloadableParser creates a new reloadable parser
func NewReloadableParser(filePath string) (*ReloadableParser, error) {
	rp := &ReloadableParser{
		filePath: filePath,
	}
	
	if err := rp.reload(); err != nil {
		return nil, err
	}
	
	return rp, nil
}

// reload reloads the ADIF file
func (rp *ReloadableParser) reload() error {
	file, err := os.Open(rp.filePath)
	if err != nil {
		return fmt.Errorf("failed to open ADIF file: %w", err)
	}
	defer file.Close()

	parser := utils.NewADIFParser()
	if err := parser.ParseFile(file); err != nil {
		return fmt.Errorf("failed to parse ADIF file: %w", err)
	}

	rp.mutex.Lock()
	rp.parser = parser
	rp.mutex.Unlock()

	log.Printf("Reloaded %d QSOs from %s", len(parser.GetQSOs()), rp.filePath)
	return nil
}

// startReloading starts the periodic reload goroutine
func (rp *ReloadableParser) startReloading(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		
		for range ticker.C {
			if err := rp.reload(); err != nil {
				log.Printf("Failed to reload ADIF file: %v", err)
			}
		}
	}()
}

// getParser returns the current parser (thread-safe)
func (rp *ReloadableParser) getParser() *utils.ADIFParser {
	rp.mutex.RLock()
	defer rp.mutex.RUnlock()
	return rp.parser
}

// populateHomeData fills the template data with common home page data
func populateHomeData(data template.Data, parser *utils.ADIFParser, csrf csrf.CSRF) {
	data["TotalQSOs"] = parser.GetTotalQSOCount()
	data["UniqueCountries"] = parser.GetUniqueCountriesCount()
	data["LatestQSOs"] = parser.GetLatestQSOs(30)
	data["PaperQSLHallOfFame"] = parser.GetPaperQSLHallOfFame()
	data["CSRFToken"] = csrf.Token()

	// Add latest QSO information
	latestQSO := parser.GetLatestQSO()
	if latestQSO != nil && !latestQSO.Timestamp.IsZero() {
		data["LatestQSODate"] = latestQSO.FormatDate()
		data["LatestQSOTimeAgo"] = humanize.Time(latestQSO.Timestamp)
	}
}

// generateMapIfNeeded generates a map image if it doesn't already exist
func generateMapIfNeeded(fileName, myGrid, theirGrid string) {
	mapPath := filepath.Join("maps", fileName)
	
	// Check if map already exists
	if _, err := os.Stat(mapPath); err == nil {
		return
	}
	
	// Generate the map
	if err := generateMap(fileName, myGrid, theirGrid); err != nil {
		log.Printf("Failed to generate map %s: %v", fileName, err)
	}
}

// generateMap creates a map image showing the two grid locations
func generateMap(fileName, myGrid, theirGrid string) error {
	config := utils.MapConfig{
		Width:      600,
		Height:     400,
		Zoom:       0, // Will be auto-calculated
		OutputPath: filepath.Join("maps", fileName),
	}
	
	return utils.CreateGridMap(myGrid, theirGrid, config)
}

func start(ctx context.Context, cmd *cli.Command) (err error) {
	// Create maps directory if it doesn't exist
	if err := os.MkdirAll("maps", 0755); err != nil {
		return fmt.Errorf("failed to create maps directory: %w", err)
	}

	// Load ADIF file with reloading capability
	adifPath := cmd.String("adif")
	reloadInterval := cmd.Duration("reload-interval")
	
	reloadableParser, err := NewReloadableParser(adifPath)
	if err != nil {
		return fmt.Errorf("failed to initialize reloadable parser: %w", err)
	}
	
	// Start automatic reloading
	reloadableParser.startReloading(reloadInterval)
	log.Printf("Started ADIF file reloading every %v", reloadInterval)

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
		c.Map(reloadableParser.getParser())
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
		populateHomeData(data, parser, x)
		t.HTML(http.StatusOK, "home")
	})

	f.Get("/qrz", func(t template.Template, data template.Data, parser *utils.ADIFParser) {
		data["LatestQSOs"] = parser.GetLatestQSOs(30)
		data["PaperQSLHallOfFame"] = parser.GetPaperQSLHallOfFame()
		t.HTML(http.StatusOK, "qrz")
	})

	// PNG route handler for serving cached map images (must be before the general route)
	f.Get("/{path}.png", func(c flamego.Context, w http.ResponseWriter, parser *utils.ADIFParser) (int, error) {
		path := c.Param("path")
		
		// Split on the last dash to separate callsign and timestamp
		lastDash := strings.LastIndex(path, "-")
		if lastDash == -1 {
			return http.StatusNotFound, nil
		}
		
		encodedCallsign := path[:lastDash]
		timestampStr := path[lastDash+1:]
		
		callsign, err := url.QueryUnescape(encodedCallsign)
		if err != nil {
			return http.StatusNotFound, nil
		}
		callsign = strings.ToUpper(callsign)
		
		// Use URL-safe filename by replacing special characters
		safeCallsign := strings.ReplaceAll(callsign, "/", "_")
		mapFileName := fmt.Sprintf("%s-%s.png", safeCallsign, timestampStr)
		mapPath := filepath.Join("maps", mapFileName)
		
		// Check if map file exists
		if _, err := os.Stat(mapPath); os.IsNotExist(err) {
			// Try to find the QSO and generate the map
			timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
			if err != nil {
				return http.StatusNotFound, nil
			}
			
			searchTime := time.Unix(timestamp, 0)
			qsos := parser.SearchQSO(callsign, searchTime, 10)
			
			if len(qsos) == 0 || qsos[0].MyGridSquare == "" || qsos[0].GridSquare == "" {
				return http.StatusNotFound, nil
			}
			
			// Generate map synchronously for immediate serving
			if err := generateMap(mapFileName, qsos[0].MyGridSquare, qsos[0].GridSquare); err != nil {
				log.Printf("Failed to generate map for %s: %v", mapFileName, err)
				return http.StatusInternalServerError, nil
			}
		}
		
		// Serve the map file
		w.Header().Set("Content-Type", "image/png")
		http.ServeFile(w, c.Request().Request, mapPath)
		return http.StatusOK, nil
	})

	f.Get("/{path}", func(c flamego.Context, t template.Template, data template.Data, parser *utils.ADIFParser) {
		path := c.Param("path")
		
		// Split on the last dash to separate callsign and timestamp
		lastDash := strings.LastIndex(path, "-")
		if lastDash == -1 {
			c.Redirect("/", http.StatusFound)
			return
		}
		
		encodedCallsign := path[:lastDash]
		timestampStr := path[lastDash+1:]
		
		callsign, err := url.QueryUnescape(encodedCallsign)
		if err != nil {
			c.Redirect("/", http.StatusFound)
			return
		}
		callsign = strings.ToUpper(callsign)

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

		// Generate or check for cached map
		mapURL := ""
		if currentQSO.MyGridSquare != "" && currentQSO.GridSquare != "" {
			safeCallsign := strings.ReplaceAll(callsign, "/", "_")
			mapFileName := fmt.Sprintf("%s-%s.png", safeCallsign, timestampStr)
			// Use encoded callsign for the URL
			encodedCallsign := url.QueryEscape(callsign)
			mapURL = fmt.Sprintf("/%s-%s.png", encodedCallsign, timestampStr)
			
			// Generate map in background if it doesn't exist
			go generateMapIfNeeded(mapFileName, currentQSO.MyGridSquare, currentQSO.GridSquare)
		}

		data["QSO"] = currentQSO
		data["AllQSOs"] = allQSOs
		data["Callsign"] = callsign
		data["MapURL"] = mapURL
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
			populateHomeData(data, parser, x)
			t.HTML(http.StatusBadRequest, "home")
			return
		}

		if year == "" || month == "" || day == "" || hour == "" || minute == "" {
			data["Error"] = "All date and time fields are required"
			populateHomeData(data, parser, x)
			t.HTML(http.StatusBadRequest, "home")
			return
		}

		// Parse timestamp from separate fields
		timestampStr := fmt.Sprintf("%s-%02s-%02sT%02s:%02s", year, month, day, hour, minute)
		searchTime, err := time.Parse("2006-01-02T15:04", timestampStr)
		if err != nil {
			data["Error"] = "Invalid date and time values"
			populateHomeData(data, parser, x)
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
			populateHomeData(data, parser, x)
			t.HTML(http.StatusOK, "home")
			return
		}

		// Redirect to unique QSO URL
		qso := qsos[0]
		unixTimestamp := qso.Timestamp.Unix()
		encodedCallsign := url.QueryEscape(qso.Call)
		redirectURL := fmt.Sprintf("/%s-%d", encodedCallsign, unixTimestamp)
		c.Redirect(redirectURL, http.StatusFound)
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
