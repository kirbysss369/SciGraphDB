package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kirbysss369/SciGraphDB/internal/openalex"
)

func main() {
	search := flag.String("search", "", "words to search for in OpenAlex works")
	limit := flag.Int("limit", 10, "maximum number of works (1-10000)")
	flag.Parse()

	if *search == "" {
		fmt.Fprintln(os.Stderr, "--search is required")
		os.Exit(2)
	}
	cfg, err := openalex.LoadConfig()
	if err != nil {
		fail(err)
	}
	if cfg.APIKey == "" && *limit > 10 {
		fail(fmt.Errorf("OPENALEX_API_KEY is required for probes larger than 10 works"))
	}
	client, err := openalex.New(cfg, nil)
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	works, err := client.Search(ctx, *search, *limit)
	if err != nil {
		fail(err)
	}
	for _, work := range works {
		year := "unknown"
		if work.PublicationYear != nil {
			year = fmt.Sprint(*work.PublicationYear)
		}
		fmt.Printf("%s (%s) %q\n", work.ID, year, work.Title)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
