// Package main contains a small tool to interact with php-fpm over FastCGI.
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"sync"
	"time"
)

func worker(
	network, address string,
	req map[string]string,
	doneWG *sync.WaitGroup,
	tick <-chan time.Time,
	doneChan <-chan struct{},
	latencyChan chan<- time.Duration,
) {
	defer doneWG.Done()
	fcgi, err := Dial(network, address)
	if err != nil {
		log.Printf("ERR: dial: %v", err)
		return
	}
	fcgi.SetKeepAlive(true)
	defer fcgi.Close()

	for {
		select {
		case <-doneChan:
			return
		case <-tick:
		}
		now := time.Now()
		resp, err := fcgi.Get(req)
		if err != nil {
			log.Printf("ERR: get: %v", err)
			return
		}

		if resp.StatusCode != http.StatusOK {
			log.Printf("ERR: http: %v", resp.StatusCode)
			return
		}

		_, err = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("ERR: read: %v", err)
			return
		}
		latencyChan <- time.Since(now)
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [options] <benchmark|get>\n", os.Args[0])
		flag.PrintDefaults()
	}
	script := flag.String("script", "index.php", "script to execute")
	network := flag.String("network", "tcp", "network to dial (net or unix)")
	address := flag.String("address", "localhost:9000", "address to dial")
	workerCount := flag.Int("workers", 4, "number of worker goroutines")
	rate := flag.Float64("rate", 10, "number of requests per second")
	duration := flag.Duration("duration", 30*time.Second, "duration of the measurement")
	verbose := flag.Bool("verbose", false, "verbose output")
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(1)
	}

	req := map[string]string{
		"SCRIPT_FILENAME":   path.Join("/opt/omegaup/frontend/www", *script),
		"GATEWAY_INTERFACE": "CGI/1.1",
		"SERVER_SOFTWARE":   "go / fcgiclient",
		"REMOTE_ADDR":       "127.0.0.1",
	}

	switch args[0] {
	case "benchmark":
		var doneWG sync.WaitGroup
		tick := time.NewTicker(time.Second / time.Duration(*rate))
		doneChan := make(chan struct{})
		latencyChan := make(chan time.Duration, 1024)
		for i := 0; i < *workerCount; i++ {
			doneWG.Add(1)
			go worker(*network, *address, req, &doneWG, tick.C, doneChan, latencyChan)
		}

		time.AfterFunc(*duration, func() {
			tick.Stop()
			close(doneChan)
		})
		go func() {
			doneWG.Wait()
			close(latencyChan)
		}()
		var latencies []time.Duration
		var averageLatency time.Duration
		for latency := range latencyChan {
			averageLatency += latency
			latencies = append(latencies, latency)
		}

		log.Printf("Done sending %d requests", len(latencies))
		if len(latencies) == 0 {
			return
		}
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		averageLatency /= time.Duration(len(latencies))
		log.Printf("Mean latency: %v", averageLatency)
		log.Printf("Min  latency: %v", latencies[0])
		log.Printf("50th latency: %v", latencies[len(latencies)/2])
		log.Printf("90th latency: %v", latencies[int(float64(len(latencies))*0.9)])
		log.Printf("95th latency: %v", latencies[int(float64(len(latencies))*0.95)])
		log.Printf("99th latency: %v", latencies[int(float64(len(latencies))*0.99)])
		log.Printf("Max  latency: %v", latencies[len(latencies)-1])

	case "get":
		fcgi, err := Dial(*network, *address)
		if err != nil {
			log.Fatalf("ERR: dial: %v", err)
		}
		defer fcgi.Close()

		resp, err := fcgi.Get(req)
		if err != nil {
			log.Fatalf("ERR: get: %v", err)
		}

		if *verbose {
			fmt.Fprintf(os.Stderr, "HTTP/%d.%d %d\n", resp.ProtoMajor, resp.ProtoMinor, resp.StatusCode)
			for k, values := range resp.Header {
				for _, v := range values {
					fmt.Fprintf(os.Stderr, "%v: %v\n", k, v)
				}
			}
			fmt.Fprintf(os.Stderr, "\n")
		}

		contents, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Fatalf("ERR: read: %v", err)
		}
		os.Stdout.Write(contents)
		if resp.StatusCode != 0 && resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}

	default:
		flag.Usage()
		os.Exit(1)
	}
}
